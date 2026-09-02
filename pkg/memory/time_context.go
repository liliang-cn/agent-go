package memory

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/timeaware"
)

// Yesterday's "tomorrow" is today.
//
// A memory is written at one moment and read at another, and everything a
// person says about time is relative to the first. "明天要去医院", stored on
// the 1st and recalled on the 2nd, describes today — but the stored text
// still says "明天", and a model reading it with no anchor books the
// appointment for the 3rd. The sentence is correct, and it is correct about
// a day that has passed.
//
// Two halves, and they sit on opposite sides of the latency budget:
//
//   - Writing. One call to pkg/timeaware resolves every item in the batch,
//     on the durable worker, where nothing is waiting for it. What comes
//     back is stored beside the text.
//   - Reading. Nothing is called. The recall path renders the stored answer
//     against the reader's own clock with arithmetic, and a memory nobody
//     managed to resolve still says when it was written — which is the
//     honest degraded answer and is enough for a model to reckon from.
//
// There is no phrase table on either side, here or in timeaware. Deciding
// what a person meant by a day is understanding, and a table of 明天 /
// tomorrow / 내일 only ever serves the languages someone enumerated.

// timeLocation is the timezone this service reasons about days in.
func (s *Service) timeLocation() *time.Location {
	if s != nil && s.timeResolver != nil {
		return s.timeResolver.Location()
	}
	return time.Local
}

// resolveItemTimes resolves a batch of texts with a standalone call.
//
// Unused by the auto-store path, which gets its answer from the extraction
// call for free (timeReferencesFromSummary). It exists for a caller that has
// text and no structured pass of its own to graft onto — an import, a
// backfill of memories written before this existed — and it is the reason
// the resolver is still built.
func (s *Service) resolveItemTimes(ctx context.Context, items []domain.MemoryItem, writtenAt time.Time) *timeaware.Result {
	if s == nil || s.timeResolver == nil || !s.timeResolver.Available() || len(items) == 0 {
		return nil
	}
	texts := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, item.Content)
	}
	res, err := s.timeResolver.Resolve(ctx, writtenAt, texts...)
	if err != nil {
		agentgolog.WithModule("memory.timeaware").Debug("resolve time references", "error", err)
		return nil
	}
	return res
}

// timeNoteFor renders what a recalled memory should say about when it was
// written and what day it named. No model call: this runs on the read path.
func timeNoteFor(m *domain.Memory, now time.Time) string {
	if m == nil {
		return ""
	}
	ref, _ := getMemoryTimeReference(m.Metadata)
	return timeaware.Note(m.CreatedAt, ref, now)
}

// timeHeader is the one line that tells the model what day it is when it
// reads memories written on other days.
//
// The system prompt already carries the date, but the memories are appended
// far below it and a model reconciling "written 2026-09-01" against a date
// it read a thousand tokens ago is doing avoidable work. Saying it again,
// next to the thing it applies to, costs a line.
func timeHeader(now time.Time) string {
	return "Today is " + now.Format("2006-01-02 (Monday)") + " " + now.Format("-07:00") +
		". Each memory below says when it was written; a relative day inside one" +
		" (\"tomorrow\", \"next week\") means what it meant then, and where that has been" +
		" resolved the absolute date is given.\n\n"
}

// hasTimeNotes reports whether any of these memories has something to say
// about time, so the header is only written when it applies.
func hasTimeNotes(memories []*domain.MemoryWithScore, now time.Time) bool {
	for _, m := range memories {
		if m == nil || m.Memory == nil {
			continue
		}
		if strings.TrimSpace(timeNoteFor(m.Memory, now)) != "" {
			return true
		}
	}
	return false
}

// Where a resolved reference lives on a memory.
//
// The accessors are here rather than in pkg/domain because pkg/timeaware
// imports pkg/domain for its Generator, so domain cannot name a
// timeaware.Reference without a cycle. pkg/memory can see both.
//
// It is a separate key from the "event" blob on purpose: that one is derived
// from the text and describes a meeting — who, what kind, whether the user
// has to be there. This is one fact, produced by asking a model what day the
// text meant, and a reader that only wants the date should not have to know
// anything about events.
const memoryTimeReferenceKey = "time_reference"

// setMemoryTimeReference records a resolved reference on a memory's metadata.
func setMemoryTimeReference(metadata map[string]interface{}, ref timeaware.Reference) map[string]interface{} {
	cloned := make(map[string]interface{}, len(metadata)+1)
	for k, v := range metadata {
		cloned[k] = v
	}
	cloned[memoryTimeReferenceKey] = ref
	return cloned
}

// getMemoryTimeReference loads a resolved reference, if one was stored.
func getMemoryTimeReference(metadata map[string]interface{}) (timeaware.Reference, bool) {
	if len(metadata) == 0 {
		return timeaware.Reference{}, false
	}
	raw, ok := metadata[memoryTimeReferenceKey]
	if !ok || raw == nil {
		return timeaware.Reference{}, false
	}
	if ref, ok := raw.(timeaware.Reference); ok {
		return ref, true
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return timeaware.Reference{}, false
	}
	var ref timeaware.Reference
	if err := json.Unmarshal(data, &ref); err != nil {
		return timeaware.Reference{}, false
	}
	return ref, true
}

// applyResolvedTimeToEvent copies the resolved answer into the structured
// event blob, so the schedule machinery downstream (corrections, dedupe)
// sees a time expression and an absolute date without a pattern ever having
// looked at the text.
//
// It reads the reference off the memory rather than taking it as an
// argument, because the event blob is created later than the reference is —
// inside Add, by enrichStructuredMemory — and the earlier of the two cannot
// write into something that does not exist yet. That ordering bug is why
// this is called from Add and not from the writer.
func applyResolvedTimeToEvent(mem *domain.Memory) {
	if mem == nil {
		return
	}
	ref, ok := getMemoryTimeReference(mem.Metadata)
	if !ok || !ref.Resolved() {
		return
	}
	event, ok := domain.GetMemoryEventMetadata(mem.Metadata)
	if !ok || event == nil {
		return
	}
	if strings.TrimSpace(event.TimeExpression) == "" {
		event.TimeExpression = ref.Text
	}
	if start, ok := ref.Start(); ok {
		event.OccursOn = start.Format("2006-01-02")
		event.AnchoredAt = ref.AnchoredAt.Format(time.RFC3339)
	}
	mem.Metadata = domain.SetMemoryEventMetadata(mem.Metadata, event)
}

// timeReferencesFromSummary reads the time fields out of the extraction
// call's own answer.
//
// The alternative was a second model call per memory write, which doubles
// the cost of a background feature to answer a question the first call was
// already looking at the text to answer. pkg/timeaware owns the contract —
// the schema fields, the prompt rules and this parse all come from there —
// so the two routes cannot drift.
func timeReferencesFromSummary(raw string, writtenAt time.Time) *timeaware.Result {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var parsed struct {
		Memories []struct {
			TimeText   string `json:"time_text"`
			TimeKind   string `json:"time_kind"`
			OccursOn   string `json:"occurs_on"`
			OccursAt   string `json:"occurs_at"`
			EndsOn     string `json:"ends_on"`
			EndsAt     string `json:"ends_at"`
			Recurrence string `json:"recurrence"`
		} `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	out := &timeaware.Result{Anchor: writtenAt}
	for i, m := range parsed.Memories {
		out.References = append(out.References, timeaware.ReferenceFromFields(
			i, writtenAt, m.TimeText, m.TimeKind, m.OccursOn, m.OccursAt, m.EndsOn, m.EndsAt, m.Recurrence))
	}
	return out
}
