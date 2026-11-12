package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// Checking the file before saying it is done.
//
// Asked to change a fee and a term in a contract PDF, "everything else
// untouched, same layout", a live run redacted the old text and wrote the new
// text over it. The new text came out smaller, grey and in another font, and
// the "3. Term" heading under the redaction was gone. The run answered that
// the rest was preserved. It was not lying so much as not looking: it never
// opened what it had written or compared it with the file it started from.
//
// The delivery contract cannot catch this. It checks that the file exists, and
// the file exists. What is wrong with it is only visible in the file — and
// that is true of any file a run produces through a tool rather than by
// typing its bytes: the script that writes it exits 0 on a report with a
// missing section, a config that does not parse, a program that does not run,
// a picture that is blank.
//
// The runtime cannot open the file and judge it; it does not know what the
// file is, and it does not try to. The model does, and has the tools. So the
// first time a run declares itself done while it owes a file that it has just
// put on disk, it is asked once to check what it made — in whatever way fits
// that kind of file — and to report what it checked. How to check a
// particular format is know-how, and lives in skills; nothing in this file
// knows one format from another.
//
// The second "done" goes to the lints as usual. One extra turn, only on runs
// that produced a file, and a model that had already checked says so and
// moves on.

// producedDeliverables returns the files this run owes that are on disk and
// were written since it began.
//
// "Since it began" is what makes a file this run's work: a path that already
// held something, untouched, was not made here and there is nothing of this
// run's to check. A file that is not there yet is the delivery contract's
// business — it is asked for before anyone is asked to look at it.
func producedDeliverables(constraints RunConstraints, workspace string, since time.Time) []string {
	var out []string
	for _, want := range constraints.Deliverables {
		// Whichever tool the extraction thought would write it. The delivery
		// contract only looks on disk for files written by the filesystem
		// tools, since a file sent through a remote tool is never there — but
		// here a file that is not on disk is simply skipped below, and one
		// that is can be checked whoever wrote it. Asking checksDisk let
		// through exactly the run this exists for: the extraction named the
		// PDF skill as the writer, and the edited contract went unchecked.
		if !strings.EqualFold(strings.TrimSpace(want.Kind), "file") || strings.TrimSpace(want.Path) == "" {
			continue
		}
		if !fileArtifactExistsIn(want.Path, workspace) {
			continue
		}
		if !since.IsZero() && !writtenSince(want.Path, workspace, since) {
			continue
		}
		out = append(out, strings.TrimSpace(want.Path))
	}
	return out
}

// writtenSince reports whether the artifact at path — a file, or anything in
// a directory — was modified at or after since.
func writtenSince(path, workspace string, since time.Time) bool {
	expanded := strings.TrimSpace(path)
	if strings.HasPrefix(expanded, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = home + expanded[1:]
		}
	}
	candidates := []string{expanded}
	if ws := strings.TrimSpace(workspace); ws != "" && !filepath.IsAbs(expanded) {
		candidates = append(candidates, filepath.Join(ws, expanded))
	}
	// File systems keep coarser time than the clock that stamped the start.
	since = since.Add(-2 * time.Second)
	for _, c := range candidates {
		info, err := os.Stat(c)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if !info.ModTime().Before(since) {
				return true
			}
			continue
		}
		fresh := false
		_ = filepath.WalkDir(c, func(_ string, d os.DirEntry, err error) error {
			if err != nil || fresh || d.IsDir() {
				return nil
			}
			if fi, ierr := d.Info(); ierr == nil && !fi.ModTime().Before(since) {
				fresh = true
			}
			return nil
		})
		if fresh {
			return true
		}
	}
	return false
}

// deliverableCheckPrompt is the one-time request to check the finished file.
// It names ways of checking, not kinds of file: which of them fits is for the
// model to say, since it knows what it made.
func deliverableCheckPrompt(paths []string) string {
	var b strings.Builder
	b.WriteString("[system] Before this is done: you produced " + strings.Join(paths, ", ") +
		". That the step which wrote it reported success says nothing about what is in it. " +
		"If you have not examined the finished file since you last wrote it, do so now, in the way that fits what it is:\n")
	b.WriteString("- Read it back from disk — the file itself, not your memory of what you meant to write — and go through the request point by point: " +
		"everything asked for is there, complete, in the form asked for.\n")
	b.WriteString("- If it is meant to be run, parsed or opened by a program, run it, parse it or open it, and read the result.\n")
	b.WriteString("- If how it looks matters, render it and inspect the rendering; when you cannot see it yourself, measure it — " +
		"extract its text, layout and styling and compare those.\n")
	b.WriteString("- If it was made by changing an existing file, compare it with the original: " +
		"the changes asked for are there, and nothing else moved, vanished or changed.\n")
	b.WriteString("If a skill covers this kind of file, it says how to check it; use that. " +
		"Fix what you find and check again. Then finish, and say in your answer what you checked. " +
		"What you could not get right, say plainly; do not report it as done or unchanged.")
	return b.String()
}

// redirectToDeliverableCheck holds back the first "done" of a run that
// produced a file it owes, and asks the loop for another turn with the
// request above.
//
// Returns true when the loop should continue instead of completing.
func (r *Runtime) redirectToDeliverableCheck(ctx context.Context, messages *[]domain.Message, state *queryLoopState) bool {
	if r == nil || r.svc == nil || r.deliverableCheckUsed {
		return false
	}
	if r.cfg != nil && r.cfg.ToolsDisabled {
		return false
	}
	constraints := r.runConstraintsWithin(constraintLintWait)
	if constraints.ForbidTools {
		return false
	}
	// A run that called nothing did not write the file, whatever its date.
	if !attemptedAnything(r.toolNamesUsedSnapshot()) {
		return false
	}
	paths := producedDeliverables(constraints, r.svc.workspaceRoot(), r.startedAt)
	if len(paths) == 0 {
		return false
	}
	r.deliverableCheckUsed = true

	trace.Decide(ctx, "deliverable_check",
		"the run is finishing with a file it produced; it is asked once to check it before the answer is accepted",
		map[string]any{"paths": paths})

	// The draft answer may already be on screen, and the next turn writes
	// another: clear it, as a lint retry does.
	r.emitTombstoneMarked(TombstoneRetry)
	*messages = append(*messages, domain.Message{Role: "user", Content: deliverableCheckPrompt(paths)})
	if state != nil {
		state.Messages = *messages
		state.setLoopTransition(queryLoopTransitionNextTurn,
			"completion held: check the produced file before finishing")
		state.noteRoundCompleted()
	}
	return true
}
