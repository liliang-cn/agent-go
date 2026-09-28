package domain

import (
	"context"
	"strings"
	"time"
)

// MemoryKind is what sort of claim a memory makes, orthogonal to MemoryType
// (which says how it is used: fact, skill, preference…). The distinction
// matters on the read side: a world fact about where someone lives is
// replaced when it changes, an opinion is one person's view and may be
// revised, an experience is something that happened and stays true of the
// past, and an observation is a summary drawn from several of the others.
//
// It is decided by the extraction call that writes the memory — never from
// the wording of the request — and it rides in Metadata under
// MemoryKindMetadataKey, so every backend that round-trips metadata carries
// it without a schema change.
type MemoryKind string

const (
	MemoryKindWorld       MemoryKind = "world"       // an objective fact about the world or the person's circumstances
	MemoryKindExperience  MemoryKind = "experience"  // something that happened, or what was done and learned doing it
	MemoryKindOpinion     MemoryKind = "opinion"     // a subjective judgment or preference someone holds
	MemoryKindObservation MemoryKind = "observation" // a synthesis drawn from several other memories
)

// MemoryKinds lists every valid kind, in the order a schema offers them.
var MemoryKinds = []MemoryKind{MemoryKindWorld, MemoryKindExperience, MemoryKindOpinion, MemoryKindObservation}

// MemoryKindMetadataKey is where a memory's kind lives in Metadata. Not
// "kind": two backends already use that key for their own purposes.
const MemoryKindMetadataKey = "memory_kind"

// ParseMemoryKind returns the kind named by s, or "" if s names none.
func ParseMemoryKind(s string) MemoryKind {
	k := MemoryKind(strings.ToLower(strings.TrimSpace(s)))
	for _, valid := range MemoryKinds {
		if k == valid {
			return k
		}
	}
	return ""
}

// MemoryKindOf reports the kind recorded on m. A consolidated observation
// written before kinds existed is still an observation; anything else with
// no recorded kind is "" — unknown, not guessed.
func MemoryKindOf(m *Memory) MemoryKind {
	if m == nil {
		return ""
	}
	if m.Metadata != nil {
		if v, ok := m.Metadata[MemoryKindMetadataKey].(string); ok {
			if k := ParseMemoryKind(v); k != "" {
				return k
			}
		}
	}
	if m.Type == MemoryTypeObservation {
		return MemoryKindObservation
	}
	return ""
}

// SetMemoryKind records k on m. An empty or unknown kind records nothing.
func SetMemoryKind(m *Memory, k MemoryKind) {
	if m == nil || ParseMemoryKind(string(k)) == "" {
		return
	}
	if m.Metadata == nil {
		m.Metadata = map[string]interface{}{}
	}
	m.Metadata[MemoryKindMetadataKey] = string(k)
}

// Metadata mirrors of the validity fields, for backends that persist a
// metadata blob but not the typed ValidTo/SupersededBy fields.
const (
	MemorySupersededByMetadataKey = "superseded_by"
	MemoryValidToMetadataKey      = "valid_to"
)

// MemoryIsSuperseded reports whether m has been replaced and is no longer
// currently true. It reads the typed fields and their metadata mirrors, so
// the answer is the same whichever of them a backend round-trips.
//
// A ValidTo in the future is not yet an end: the memory is valid until then.
func MemoryIsSuperseded(m *Memory) bool {
	if m == nil {
		return false
	}
	if strings.TrimSpace(m.SupersededBy) != "" {
		return true
	}
	if m.ValidTo != nil && !m.ValidTo.After(time.Now()) {
		return true
	}
	if m.Metadata != nil {
		if v, ok := m.Metadata[MemorySupersededByMetadataKey].(string); ok && strings.TrimSpace(v) != "" {
			return true
		}
		if v, ok := m.Metadata[MemoryValidToMetadataKey].(string); ok && strings.TrimSpace(v) != "" {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil && !t.After(time.Now()) {
				return true
			}
		}
	}
	return false
}

// MemoryStaleMarker is the optional capability a backend implements when it
// can record that one memory has been superseded by another: end its
// validity now and point it at its replacement, without deleting it.
//
// Optional on purpose. A backend without it is not broken — the memory
// service keeps both memories and says so in a log — and a fake
// implementation that silently drops the mark is worse than none, because
// the read path would go on injecting the old fact as current.
type MemoryStaleMarker interface {
	MarkStale(ctx context.Context, id string, supersededByID string) error
}
