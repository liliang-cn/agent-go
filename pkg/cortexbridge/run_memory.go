package cortexbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// RunMemory is a CortexDB-backed implementation of agent.RunMemory.
//
// Recall: a hybrid (lexical + graph-expanded) context pack for the goal —
// no LLM, no embedder required, millisecond-level.
//
// Capture: deterministic, zero-LLM. Lines starting with a capture marker
// (default "DECISION:") are ingested verbatim into a searchable collection,
// and technical identifiers found in them (`backticked`, snake_case,
// kebab-case tokens) become graph entities with mention edges back to the
// ingested document — so later recalls can travel entity → decision. Runs
// that produce no marker line are not captured at all: predictable, free,
// and no LLM extraction noise in the graph.
type RunMemory struct {
	tb         *cortexdb.GraphRAGToolbox
	markers    []string
	collection string
}

// RunMemoryOption configures NewRunMemory.
type RunMemoryOption func(*RunMemory)

// WithCaptureMarkers replaces the line prefixes that trigger capture
// (default: "DECISION:").
func WithCaptureMarkers(markers ...string) RunMemoryOption {
	return func(r *RunMemory) { r.markers = markers }
}

// WithCaptureCollection sets the knowledge collection captured runs are
// ingested into (default: "decisions").
func WithCaptureCollection(name string) RunMemoryOption {
	return func(r *RunMemory) { r.collection = name }
}

// NewRunMemory wraps an open CortexDB handle as an agent.RunMemory:
//
//	cortex, _ := cortexdb.Open(cortexdb.DefaultConfig("cortex.db"))
//	rm := cortexbridge.NewRunMemory(cortex)
//	svc, _ := agent.New("ops").WithRunMemory(rm).Build()
func NewRunMemory(db *cortexdb.DB, opts ...RunMemoryOption) *RunMemory {
	r := &RunMemory{
		tb:         db.GraphRAGTools(),
		markers:    []string{"DECISION:"},
		collection: "decisions",
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RecallForRun assembles a context pack for the goal. An empty pack returns
// "" so the runtime injects nothing.
func (r *RunMemory) RecallForRun(ctx context.Context, goal string) (string, error) {
	resp, err := r.tb.KnowledgeMemoryBuildContextPack(ctx, cortexdb.KnowledgeMemoryBuildContextPackRequest{
		Query:      goal,
		GraphLight: true,
	})
	if err != nil {
		// CortexDB creates graph tables lazily on first write; on a fresh
		// database the read path reports them missing. That is "nothing
		// remembered yet", not a recall failure.
		if strings.Contains(err.Error(), "no such table") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(resp.ContextPack.Text), nil
}

// CaptureRun ingests the run's marker lines and links their identifiers into
// the graph. No marker line, no write.
func (r *RunMemory) CaptureRun(ctx context.Context, goal, finalText string) error {
	captured := r.markerLines(finalText)
	if len(captured) == 0 {
		return nil
	}
	body := strings.Join(captured, "\n") + "\nGoal: " + goal

	sum := sha256.Sum256([]byte(body))
	docID := "run-capture-" + hex.EncodeToString(sum[:6])

	title := captured[0]
	if len(title) > 120 {
		title = title[:120]
	}
	if _, err := r.tb.IngestDocument(ctx, cortexdb.ToolIngestDocumentRequest{
		DocumentID: docID,
		Title:      title,
		Content:    body,
		Collection: r.collection,
	}); err != nil {
		return fmt.Errorf("ingest captured run: %w", err)
	}

	ids := identifiers(strings.Join(captured, "\n"))
	if len(ids) == 0 {
		return nil
	}
	entities := make([]cortexdb.ToolEntityInput, 0, len(ids))
	for _, id := range ids {
		entities = append(entities, cortexdb.ToolEntityInput{Name: id, Type: "identifier"})
	}
	if _, err := r.tb.UpsertEntities(ctx, cortexdb.ToolUpsertEntitiesRequest{
		DocumentID: docID,
		Entities:   entities,
	}); err != nil {
		return fmt.Errorf("upsert captured entities: %w", err)
	}
	return nil
}

// markerLines returns the lines of text that start with one of the capture
// markers, trimmed.
func (r *RunMemory) markerLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for _, m := range r.markers {
			if strings.HasPrefix(line, m) {
				out = append(out, line)
				break
			}
		}
	}
	return out
}

// identifiers extracts technical identifiers from text, deterministically:
// backtick-quoted tokens plus bare snake_case / kebab-case words. Capped at 8
// so one verbose decision cannot flood the graph.
func identifiers(text string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		tok = strings.Trim(tok, "`.,;:()[]{}\"'")
		if len(tok) < 3 || len(tok) > 64 || seen[tok] || len(out) >= 8 {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}
	// Backticked tokens first — they are explicit author intent.
	parts := strings.Split(text, "`")
	for i := 1; i < len(parts); i += 2 {
		if !strings.ContainsAny(parts[i], " \n") {
			add(parts[i])
		}
	}
	// Then bare snake/kebab identifiers.
	for _, tok := range strings.Fields(text) {
		trimmed := strings.Trim(tok, "`.,;:()[]{}\"'")
		if strings.ContainsAny(trimmed, "_") || strings.Count(trimmed, "-") >= 1 && !strings.HasPrefix(trimmed, "-") {
			if !strings.ContainsAny(trimmed, "/\\") { // skip paths/URLs
				add(trimmed)
			}
		}
	}
	return out
}
