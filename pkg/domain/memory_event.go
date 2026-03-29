package domain

import "encoding/json"

// MemoryEventRelevance describes how strongly an event applies to the user.
type MemoryEventRelevance string

const (
	MemoryEventRelevanceDirect     MemoryEventRelevance = "direct"
	MemoryEventRelevanceIndirect   MemoryEventRelevance = "indirect"
	MemoryEventRelevanceBackground MemoryEventRelevance = "background"
)

// MemoryUserRole describes the user's role relative to a stored event.
type MemoryUserRole string

const (
	MemoryUserRoleOwner       MemoryUserRole = "owner"
	MemoryUserRoleParticipant MemoryUserRole = "participant"
	MemoryUserRoleObserver    MemoryUserRole = "observer"
	MemoryUserRoleNotInvolved MemoryUserRole = "not_involved"
)

// MemoryEventMetadata captures a structured event interpretation for one memory.
type MemoryEventMetadata struct {
	Kind                string               `json:"kind,omitempty"`
	EventType           string               `json:"event_type,omitempty"`
	TimeExpression      string               `json:"time_expression,omitempty"`
	Location            string               `json:"location,omitempty"`
	SubjectProfiles     []string             `json:"subject_profiles,omitempty"`
	ParticipantProfiles []string             `json:"participant_profiles,omitempty"`
	OrganizerProfiles   []string             `json:"organizer_profiles,omitempty"`
	RequiresUser        bool                 `json:"requires_user"`
	UserRole            MemoryUserRole       `json:"user_role,omitempty"`
	RelevanceToUser     MemoryEventRelevance `json:"relevance_to_user,omitempty"`
	Correction          bool                 `json:"correction,omitempty"`
	CorrectionHints     []string             `json:"correction_hints,omitempty"`
	UpdatedByMemoryID   string               `json:"updated_by_memory_id,omitempty"`
}

// GetMemoryEventMetadata loads structured event metadata from a memory metadata map.
func GetMemoryEventMetadata(metadata map[string]interface{}) (*MemoryEventMetadata, bool) {
	if len(metadata) == 0 {
		return nil, false
	}
	raw, ok := metadata["event"]
	if !ok || raw == nil {
		return nil, false
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}

	var event MemoryEventMetadata
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, false
	}
	if event.Kind == "" {
		event.Kind = "event"
	}
	return &event, true
}

// SetMemoryEventMetadata stores structured event metadata into a memory metadata map.
func SetMemoryEventMetadata(metadata map[string]interface{}, event *MemoryEventMetadata) map[string]interface{} {
	cloned := cloneMemoryMetadata(metadata)
	if cloned == nil {
		cloned = make(map[string]interface{})
	}
	if event == nil {
		delete(cloned, "event")
		return cloned
	}

	data, err := json.Marshal(event)
	if err != nil {
		cloned["event"] = map[string]interface{}{
			"kind":             event.Kind,
			"event_type":       event.EventType,
			"time_expression":  event.TimeExpression,
			"location":         event.Location,
			"subject_profiles": append([]string(nil), event.SubjectProfiles...),
		}
		return cloned
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return cloned
	}
	if _, ok := decoded["kind"]; !ok || decoded["kind"] == "" {
		decoded["kind"] = "event"
	}
	cloned["event"] = decoded
	return cloned
}

func cloneMemoryMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
