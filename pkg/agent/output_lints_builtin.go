package agent

import (
	"regexp"
	"strings"
)

// Built-in OutputLint implementations. These encode rules that have lived in
// instruction strings for a long time. Moving them into deterministic checks
// shifts enforcement from "the model has to remember a paragraph" to "the
// runtime rejects and re-prompts on violation".
//
// Each builtin can be wired into a Service via RegisterOutputLint(...). They
// are NOT auto-registered — callers (CLI, library users, tests) opt in. This
// keeps the framework backward-compatible: existing services see no change
// until they explicitly enable a lint.

// --- 3. no_planning_only_finish ------------------------------------------------
//
// Catch the most common failure mode of any agent: ending with "我会做这个 /
// next steps: ... / I will now ..." instead of actually doing it. By the
// time the runtime invokes lints we already know the model did NOT call
// task_complete or task_blocked — those paths terminate earlier. So if the
// final text reads like a plan, the model is stalling.

var planningEndingPatterns = []*regexp.Regexp{
	// English
	regexp.MustCompile(`(?i)\bnext steps?:\s*$`),
	regexp.MustCompile(`(?i)\bnext steps?:\s*[\s\S]{0,200}$`),
	// "let me <verb> ..." as the closing clause — the most common stall.
	// Anchored to a clause boundary (line start, sentence punctuation, or a
	// "now/so" lead-in) so it catches mid-sentence stalls ("Great data! Let
	// me check ...", ". Now let me use ...") without matching relative
	// clauses. The benign "let me know ..." closing is stripped beforehand.
	regexp.MustCompile(`(?i)(?:^|[.!?;]\s+|\n|\bnow\s+|\bso\s+)let me\s+\w[\s\S]{0,200}$`),
	// "I will / I'll / I am going to / I'm going to <verb> ..." closing clause,
	// same boundary anchor so "...what I'll do." (a relative clause inside a
	// refusal/answer) is NOT flagged as a stall.
	regexp.MustCompile(`(?i)(?:^|[.!?;]\s+|\n|\bnow\s+|\bso\s+)i(?:'ll| will| am going to|'m going to)\s+\w[\s\S]{0,200}$`),
	regexp.MustCompile(`(?i)\b(?:i can|i could|i would)\s+(?:do this|handle this|take care of this) (?:next|now)\.?\s*$`),
	// Chinese — common stalling endings
	regexp.MustCompile(`(?:接下来|下一步)(?:我)?(?:会|将|要|准备)?[^。\n]{0,40}[。.\s]*$`),
	regexp.MustCompile(`我(?:会|将|准备|打算|马上|这就|去|要)[^。\n]{0,40}[。.\s]*$`),
}

// benignClosing matches trailing acknowledgments that read like planning but
// are not stalls: the polite "let me know ..." sign-off, and confirmations that
// the agent has noted/remembered something ("I'll remember ...", "我会记住...").
// These are stripped before the planning patterns run, since RE2 has no
// negative lookahead to exclude them inline. Without this, a memory-save
// confirmation like "我会记住这件事。" would be wrongly rejected as a stall.
var benignClosing = regexp.MustCompile(`(?i)(?:\blet me know\b|\bi(?:'ll| will) (?:remember|note|keep in mind)\b|\bnoted\b|我(?:会|将)?(?:记住|记得|记下|记录|留意|注意))[\s\S]*$`)

// NoPlanningOnlyFinish rejects free-form final answers that read like a
// plan ("I'll do X next", "接下来我会..."). Register globally; it applies to
// any agent that produces free-form text instead of a terminal tool call.
func NoPlanningOnlyFinish() OutputLint {
	return LintFunc{
		NameValue: "no_planning_only_finish",
		Fn: func(text string, ctx LintContext) (bool, string) {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return true, ""
			}
			// Don't trip on substantive answers that *contain* future-tense
			// language but also clearly delivered work. Heuristic: if the
			// text is short and ends in a planning phrase, it's stalling.
			// Long answers may legitimately include phrases like "I will
			// monitor..." in the middle and finish with a real result.
			if len(trimmed) > 600 {
				return true, ""
			}
			// Strip benign trailing acknowledgments ("let me know ...",
			// "我会记住...") before scanning so they aren't flagged as stalls.
			scan := strings.TrimSpace(benignClosing.ReplaceAllString(trimmed, ""))
			for _, pat := range planningEndingPatterns {
				if pat.MatchString(scan) {
					return false, "response reads like a plan (\"我会... / next steps: ... / I will...\") " +
						"instead of delivering a completed result. Either complete the work and call task_complete " +
						"with the verified result, or call task_blocked with the concrete blocker."
				}
			}
			return true, ""
		},
	}
}

// --- 4. file_task_must_write ---------------------------------------------------
//
// Goal-aware completion check (Hashimoto: check the goal at completion). If the
// task asked to create / write / save a file or a concrete artifact (PPT, HTML,
// PDF, ...) but the run never called a filesystem write tool, the agent is
// finishing without doing the work — reject so the runtime re-prompts it to
// actually write the file. This relies on LintContext.Goal + LintContext.ToolCalls,
// which the runtime populates in runFinalLints.

// filesystemWriteTools is the set of tool names that actually mutate a file on
// disk. create_directory alone doesn't count (no content written). Coding-agent
// delegation tools are included because the delegated CLI (codex/claude/agy/...)
// does the actual file writing on the agent's behalf.
var filesystemWriteTools = map[string]bool{
	// Built-in sandbox tools (pkg/sandbox via RegisterSandboxTools) — these
	// write/mutate files in the agent's workspace.
	"fs_write":      true,
	"fs_edit":       true,
	"fs_multi_edit": true,
	"fs_move":       true,
	// Sandbox shell tools can write files too (e.g. a script that emits output
	// to a path); count them so a bash/heredoc-written file isn't falsely
	// flagged as "no file written".
	"bash":        true,
	"shell_send":  true,
	"shell_start": true,
	// Operator coding-agent delegation — the sub-CLI writes the files.
	"run_coding_agent_once":      true,
	"send_coding_agent_prompt":   true,
	"start_coding_agent_session": true,
	"start_pty_session":          true,
	"send_pty_input":             true,
}

func usedFilesystemWriteTool(toolCalls []string) bool {
	for _, name := range toolCalls {
		if filesystemWriteTools[strings.TrimSpace(name)] {
			return true
		}
	}
	return false
}

// FileTaskMustWrite rejects a free-form completion when the run owed the user a
// file that wasn't actually produced. When the deliverable names a concrete
// path it verifies the RESULT — the file must exist and be non-empty on disk
// (a write tool call truncated by max_tokens does NOT count). Otherwise it
// falls back to checking that a write tool was used.
//
// Which runs owe a file comes from LintContext.Deliverables (constraints.go),
// never from matching write-verbs against the goal text.
func FileTaskMustWrite() OutputLint {
	return LintFunc{
		NameValue: "file_task_must_write",
		Fn: func(text string, ctx LintContext) (bool, string) {
			var paths []string
			wantsFile := false
			for _, want := range ctx.Deliverables {
				if !strings.EqualFold(strings.TrimSpace(want.Kind), "file") {
					continue
				}
				wantsFile = true
				if p := strings.TrimSpace(want.Path); p != "" {
					paths = append(paths, p)
				}
			}
			if !wantsFile {
				return true, ""
			}
			// Prefer verifying the actual artifact (result, not attempt).
			if len(paths) > 0 {
				for _, p := range paths {
					if fileArtifactExists(p) {
						return true, ""
					}
				}
				return false, "the task asked you to produce " + strings.Join(paths, ", ") +
					", but no such file exists on disk yet (or it is empty — a write may have been " +
					"truncated). Actually write the file, verify it exists and is complete, then finish; " +
					"or call task_blocked with the concrete blocker."
			}
			// No explicit path named: fall back to "was a write tool used?".
			if usedFilesystemWriteTool(ctx.ToolCalls) {
				return true, ""
			}
			return false, "the task asked you to create/write/save a file, but no file write tool " +
				"(e.g. fs_write) was called this run. Actually write the file to disk and " +
				"verify it exists, then finish; or call task_blocked with the concrete blocker."
		},
	}
}

// RegisterDefaultOutputLints wires the built-in lints into the given service.
// Callers can pick and choose by registering individually if they only want a
// subset.
func RegisterDefaultOutputLints(svc *Service) {
	if svc == nil {
		return
	}
	svc.RegisterOutputLint(NoPlanningOnlyFinish())
	svc.RegisterOutputLint(FileTaskMustWrite())
	svc.RegisterOutputLint(NonEmptyFinalAnswer())
	svc.RegisterOutputLint(TaskDeliveryContract())
	svc.RegisterOutputLint(RequestedActionContract())
	svc.RegisterOutputLint(NoToolScaffoldingAnswer())
	svc.RegisterOutputLint(DeliverableBlockMustCarryWork())
}
