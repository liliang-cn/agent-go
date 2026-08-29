package agent

import (
	"context"
	"fmt"
	"strings"
)

// The notes file: what the agent wants its future self to know.
//
// A segmented run hands over three things — the plan, the workspace, and
// notes. The first two were built and this is the third, and its absence had a
// very specific cost: a segment writing an end-to-end test had to call
// functions defined in files earlier segments wrote, and nothing carried their
// signatures. It guessed one, failed to compile, read the file, fixed it,
// guessed the next — four gate runs in a row red, one signature at a time.
//
// The plan's notes cannot hold that. They are per-step and about progress:
// "AUTH-OK passed, TestAuthService in auth_test.go". What was missing is the
// other kind of knowledge — the shape of things, true across every step and
// every segment. A route's exact path. A table's columns. A function's
// signature. The reason an approach was abandoned.
//
// So the workspace gets one file whose *contents* ride in the hand-off rather
// than just its name, and the agent is told it survives. It is deliberately a
// plain file in the workspace: the agent already has tools to write files, it
// is visible to whoever inspects the run afterwards, and it needs no
// understanding of any language to work.

// DefaultNotesFile is the workspace file whose contents are carried into every
// later run of the same task.
const DefaultNotesFile = "AGENT_NOTES.md"

// notesHandoffMaxBytes bounds what is injected. The notes ride in a system
// prompt that must stay byte-stable for a whole segment, so an agent that
// pastes its entire source tree in there must not be able to drown the run.
const notesHandoffMaxBytes = 8000

// WithNotesFile changes which workspace file is carried into later runs.
// Empty restores the default; the file lives in the sandbox workspace, so this
// does nothing without WithSandbox.
func (b *Builder) WithNotesFile(name string) *Builder {
	b.notesFile = strings.TrimSpace(name)
	return b
}

// notesFileName reports the notes file this service carries.
func (s *Service) notesFileName() string {
	if s == nil || strings.TrimSpace(s.notesFile) == "" {
		return DefaultNotesFile
	}
	return s.notesFile
}

// notesForRun returns the notes section for a run, or "" when there is no
// sandbox. A missing or empty file is not an error — it is the first segment,
// and the section then tells the agent the file exists to be written.
func (s *Service) notesForRun(ctx context.Context) string {
	if s == nil {
		return ""
	}
	sb := s.Sandbox()
	if sb == nil {
		return ""
	}
	name := s.notesFileName()

	data, err := sb.ReadFile(ctx, name)
	body := strings.TrimSpace(string(data))
	if err != nil || body == "" {
		// Say it exists even when it is empty. An agent that is never told
		// where to put durable knowledge does not write any, which is exactly
		// how the signatures went missing.
		return fmt.Sprintf(
			"There is no %s yet. Write one as you go: it is the only file whose "+
				"contents are carried into later stretches of this task verbatim, "+
				"so put in it what a later attempt cannot rediscover cheaply — "+
				"exact function signatures, route paths, table columns, an approach "+
				"you ruled out and why. Keep it short and keep it true.", name)
	}

	truncated := false
	if len(body) > notesHandoffMaxBytes {
		body = body[:notesHandoffMaxBytes]
		truncated = true
	}
	out := fmt.Sprintf("From %s, written by earlier work on this task:\n\n%s", name, body)
	if truncated {
		out += fmt.Sprintf("\n\n[%s is longer than this; read the file for the rest, "+
			"and consider trimming it to what is still true]", name)
	}
	return out
}
