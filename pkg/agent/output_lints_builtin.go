package agent

import (
	"os"
	"path/filepath"
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

// --- 1. dispatcher_no_bounce_back ----------------------------------------------
//
// Dispatcher's job is to call route_builtin_request and return the routed
// result inline. It must not narrate "I will route this..." back to the user
// without actually doing it. This lint catches the common failure mode where
// the model writes a routing intention as the final answer.

var dispatcherBounceBackPatterns = []*regexp.Regexp{
	// "I will / I am going to route|dispatch|hand off|..."
	regexp.MustCompile(`(?i)\bi (?:will|am going to)\s+(?:route|dispatch|hand(?:\s|-)?off|forward|delegate|pass)\b`),
	// Contracted "I'll route|dispatch|hand off|..."
	regexp.MustCompile(`(?i)\bi'll\s+(?:route|dispatch|hand(?:\s|-)?off|forward|delegate|pass)\b`),
	regexp.MustCompile(`(?i)\blet me (?:route|dispatch|hand(?:\s|-)?off|forward|delegate)\b`),
	regexp.MustCompile(`(?i)\brouting (?:this|the (?:request|task)) (?:to|over to)\b`),
	// Chinese: 我会让 X 处理 / 将由 X 完成 / 我来转交给 X / 接下来由 X 来
	regexp.MustCompile(`我(?:会|将|来)?(?:让|把|转交|交给|分派|派给)[^。\n]{1,20}(?:处理|完成|来做|执行|负责)`),
	regexp.MustCompile(`(?:接下来|下一步)?(?:将|由)[^。\n]{1,20}(?:负责|完成|处理|来做|来执行)`),
}

// DispatcherNoBounceBack rejects Dispatcher responses that narrate routing
// intent without actually returning a routed result. Register against the
// Dispatcher agent only.
func DispatcherNoBounceBack() OutputLint {
	return LintFunc{
		NameValue: "dispatcher_no_bounce_back",
		Fn: func(text string, ctx LintContext) (bool, string) {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return true, ""
			}
			for _, pat := range dispatcherBounceBackPatterns {
				if pat.MatchString(trimmed) {
					return false, "response narrates routing/delegation instead of returning a concrete result. " +
						"Either call route_builtin_request and inline its result, or answer the user directly. " +
						"Do not announce that you are about to dispatch."
				}
			}
			return true, ""
		},
	}
}

// --- 2. archivist_no_relative_time ---------------------------------------------
//
// Archivist must resolve relative time references (明天 / 后天 / 下周 /
// tomorrow / next Monday / ...) to absolute dates before storing memory.
// The lint fails when the response contains a relative-time keyword AND no
// absolute date marker (YYYY-MM-DD / YYYY/MM/DD / YYYY年MM月DD日) is present
// nearby. This avoids false positives when the agent legitimately echoes a
// relative phrase next to its resolved absolute date.

var (
	archivistRelativeTimePatterns = []*regexp.Regexp{
		// Chinese
		regexp.MustCompile(`(?:明天|后天|大后天|今天|昨天|前天|大前天)`),
		regexp.MustCompile(`(?:本|这|下|上)(?:周|星期|礼拜)(?:[一二三四五六日天])?`),
		regexp.MustCompile(`(?:本|这|下|上)(?:个)?月`),
		// English
		regexp.MustCompile(`(?i)\b(?:tomorrow|yesterday|today|tonight)\b`),
		regexp.MustCompile(`(?i)\b(?:next|last|this)\s+(?:week|month|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`),
		regexp.MustCompile(`(?i)\bin\s+\d+\s+(?:hour|hours|day|days|week|weeks|minute|minutes)\b`),
	}

	absoluteDatePatterns = []*regexp.Regexp{
		regexp.MustCompile(`\b\d{4}[-/.]\d{1,2}[-/.]\d{1,2}\b`),
		regexp.MustCompile(`\d{4}年\s*\d{1,2}\s*月\s*\d{1,2}\s*日`),
		regexp.MustCompile(`(?i)\b(?:january|february|march|april|may|june|july|august|september|october|november|december)\s+\d{1,2}(?:,\s*\d{4})?\b`),
	}
)

// ArchivistNoRelativeTime rejects Archivist responses that mention a
// relative time reference (明天 / tomorrow / next Monday / ...) without an
// absolute date in the same response. Register against the Archivist agent.
func ArchivistNoRelativeTime() OutputLint {
	return LintFunc{
		NameValue: "archivist_no_relative_time",
		Fn: func(text string, ctx LintContext) (bool, string) {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return true, ""
			}
			hasRelative := false
			for _, pat := range archivistRelativeTimePatterns {
				if pat.MatchString(trimmed) {
					hasRelative = true
					break
				}
			}
			if !hasRelative {
				return true, ""
			}
			for _, pat := range absoluteDatePatterns {
				if pat.MatchString(trimmed) {
					return true, ""
				}
			}
			return false, "response contains a relative time reference (明天 / tomorrow / next ... ) " +
				"but no absolute date (YYYY-MM-DD or equivalent). Resolve the relative reference using the " +
				"current date in your runtime context, then re-emit the answer with the absolute date."
		},
	}
}

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

var fileOutputIntentPatterns = []*regexp.Regexp{
	// English: a write verb followed (closely) by a file/artifact noun.
	regexp.MustCompile(`(?i)\b(?:write|save|create|generate|export|produce|build|make)\b[\s\S]{0,40}\b(?:file|files|ppt|pptx|powerpoint|slides?|deck|presentation|html|webpage|pdf|docx?|xlsx?|csv|markdown|report|document|spreadsheet)\b`),
	// English: "save/write/export ... to <path>".
	regexp.MustCompile(`(?i)\b(?:save|write|export)\b[\s\S]{0,24}\bto\b\s+[~/.][\w./~-]+`),
	// Chinese: 写/保存/生成/创建/导出/做/输出/存 + 文件/ppt/幻灯片/...
	regexp.MustCompile(`(写|保存|生成|创建|导出|做|输出|存)[^。\n]{0,20}(文件|ppt|pptx|幻灯片|演示文稿|文档|报告|表格|网页|页面|html|pdf|word|excel)`),
	// Chinese: 保存/写/导出 ... 到/为/至 <path>.
	regexp.MustCompile(`(保存|写|导出|存|输出)[^。\n]{0,12}(到|为|至)\s*[~/.][\w./~-]+`),
}

// filesystemWriteTools is the set of tool names that actually mutate a file on
// disk. create_directory alone doesn't count (no content written). Coding-agent
// delegation tools are included because the delegated CLI (codex/claude/agy/...)
// does the actual file writing on the agent's behalf.
var filesystemWriteTools = map[string]bool{
	"mcp_filesystem_write_file":  true,
	"mcp_filesystem_modify_file": true,
	"mcp_filesystem_move_file":   true,
	"mcp_filesystem_copy_file":   true,
	// Operator coding-agent delegation — the sub-CLI writes the files.
	"run_coding_agent_once":      true,
	"send_coding_agent_prompt":   true,
	"start_coding_agent_session": true,
	"start_pty_session":          true,
	"send_pty_input":             true,
}

func goalWantsFileOutput(goal string) bool {
	g := strings.TrimSpace(goal)
	if g == "" {
		return false
	}
	for _, p := range fileOutputIntentPatterns {
		if p.MatchString(g) {
			return true
		}
	}
	return false
}

func usedFilesystemWriteTool(toolCalls []string) bool {
	for _, name := range toolCalls {
		if filesystemWriteTools[strings.TrimSpace(name)] {
			return true
		}
	}
	return false
}

// goalFilePathPattern matches an absolute (/...) or home (~/...) filesystem
// path with an extension. Relative paths are intentionally excluded — we can't
// stat them reliably without the agent's cwd/workspace, and a wrong stat would
// falsely block a legitimate completion. A preceding-byte check in
// extractGoalFilePaths then drops URL paths (".../a.html") and source-file
// references embedded in prose ("pkg/agent/x.go").
var goalFilePathPattern = regexp.MustCompile(`[~/][\w./~-]*\.[A-Za-z0-9]{1,8}`)

// extractGoalFilePaths pulls explicit absolute/home target file paths out of
// the goal text, skipping matches that are really URL paths or substrings of a
// larger token (RE2 has no lookbehind, so we filter on the preceding byte).
func extractGoalFilePaths(goal string) []string {
	locs := goalFilePathPattern.FindAllStringIndex(goal, -1)
	seen := make(map[string]struct{}, len(locs))
	out := make([]string, 0, len(locs))
	for _, loc := range locs {
		if loc[0] > 0 {
			switch prev := goal[loc[0]-1]; {
			// URL ("://x/a.html"), or a continuation of a longer token
			// ("pkg/agent/x.go", "v1.2/x.json") — not a standalone path.
			case prev == ':' || prev == '.' || prev == '/' || prev == '~' || prev == '-',
				prev >= 'a' && prev <= 'z', prev >= 'A' && prev <= 'Z', prev >= '0' && prev <= '9':
				continue
			}
		}
		p := strings.TrimRight(strings.TrimSpace(goal[loc[0]:loc[1]]), ".,;:)")
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// fileArtifactExists reports whether the path resolves to a regular,
// non-empty file on disk (expanding a leading ~ via the package helper).
func fileArtifactExists(path string) bool {
	p := strings.TrimSpace(path)
	if p == "" {
		return false
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(getHomeDir(), strings.TrimPrefix(p, "~"))
	}
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Size() > 0
}

// FileTaskMustWrite rejects a free-form completion when the goal asked for a
// file/artifact that wasn't actually produced. When the goal names a concrete
// path it verifies the RESULT — the file must exist and be non-empty on disk
// (a write tool call that got truncated by max_tokens does NOT count). When no
// explicit path is given it falls back to checking a write tool was used.
// Register globally; agent-agnostic.
func FileTaskMustWrite() OutputLint {
	return LintFunc{
		NameValue: "file_task_must_write",
		Fn: func(text string, ctx LintContext) (bool, string) {
			if !goalWantsFileOutput(ctx.Goal) {
				return true, ""
			}
			// Prefer verifying the actual artifact (result, not attempt).
			if paths := extractGoalFilePaths(ctx.Goal); len(paths) > 0 {
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
			return false, "the task asked you to create/write/save a file, but no filesystem write tool " +
				"(e.g. mcp_filesystem_write_file) was called this run. Actually write the file to disk and " +
				"verify it exists, then finish; or call task_blocked with the concrete blocker."
		},
	}
}

// --- 5. no_raw_ptc_code --------------------------------------------------------
//
// PTC (Programmatic Tool Calling) lets the model write JS that runs in a sandbox
// and calls tools. Weaker models sometimes emit that JS as their FINAL answer
// instead of letting it execute — the user then sees raw `callTool(...)` code.
// This lint rejects a final answer that still contains PTC sandbox source so the
// runtime re-prompts for the executed result.

// ptcSourcePattern matches the PTC sandbox API surface that should never appear
// in a user-facing final answer. These identifiers are specific to the sandbox
// (callTool / toolData / toolOk / toolError / callMCPTool), so matching them is
// high-precision — ordinary prose and even most code samples don't use them.
var ptcSourcePattern = regexp.MustCompile(`\b(?:callTool|callMCPTool|toolData|toolOk|toolError)\s*\(`)

// NoRawPTCCode rejects a free-form completion that leaked PTC sandbox code
// (e.g. "const r = callTool('list_schedules', {})") instead of the executed
// result. Register globally; agent-agnostic.
func NoRawPTCCode() OutputLint {
	return LintFunc{
		NameValue: "no_raw_ptc_code",
		Fn: func(text string, ctx LintContext) (bool, string) {
			if ptcSourcePattern.MatchString(text) {
				return false, "your final answer contains raw PTC/JavaScript sandbox code " +
					"(callTool / toolData / ...). Do not return the code: let it execute and " +
					"return the actual result in plain language, or call the tools directly."
			}
			return true, ""
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
	svc.RegisterOutputLint(NoRawPTCCode())
	svc.RegisterOutputLint(DispatcherNoBounceBack(), BuiltInDispatcherAgentName)
	svc.RegisterOutputLint(ArchivistNoRelativeTime(), defaultArchivistAgentName)
}

// applyBuiltInOutputLints attaches the matching agent-specific lint when
// TeamManager builds a service for a known built-in agent. This is what
// turns the lint registry from an opt-in primitive into actual enforcement
// for the framework's own agents — the corresponding instruction-string
// rules can then be cut from the agent's system prompt because the runtime
// is doing the enforcement.
//
// The agent-agnostic no_planning_only_finish lint is registered for ALL
// services in builder.build() (including custom agent.New(...).Build()
// ones). This helper only layers on the agent-specific lints for the
// framework's own built-in agents.
func applyBuiltInOutputLints(svc *Service, model *AgentModel) {
	if svc == nil || model == nil {
		return
	}
	name := strings.TrimSpace(model.Name)
	switch {
	case strings.EqualFold(name, defaultDispatcherAgentName):
		svc.RegisterOutputLint(DispatcherNoBounceBack(), defaultDispatcherAgentName)
	case strings.EqualFold(name, defaultArchivistAgentName):
		svc.RegisterOutputLint(ArchivistNoRelativeTime(), defaultArchivistAgentName)
	}
}
