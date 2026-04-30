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
	regexp.MustCompile(`(?i)(?:^|\n)\s*(?:i (?:will|'ll|am going to)|let me)\s+\w[\s\S]{0,200}\.?\s*$`),
	regexp.MustCompile(`(?i)\b(?:i can|i could|i would)\s+(?:do this|handle this|take care of this) (?:next|now)\.?\s*$`),
	// Chinese — common stalling endings
	regexp.MustCompile(`(?:接下来|下一步)(?:我)?(?:会|将|要|准备)?[^。\n]{0,40}[。.\s]*$`),
	regexp.MustCompile(`我(?:会|将|准备|打算|马上|这就|去|要)[^。\n]{0,40}[。.\s]*$`),
}

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
			for _, pat := range planningEndingPatterns {
				if pat.MatchString(trimmed) {
					return false, "response reads like a plan (\"我会... / next steps: ... / I will...\") " +
						"instead of delivering a completed result. Either complete the work and call task_complete " +
						"with the verified result, or call task_blocked with the concrete blocker."
				}
			}
			return true, ""
		},
	}
}

// RegisterDefaultOutputLints wires the three built-in lints into the given
// service. Callers can pick and choose by registering individually if they
// only want a subset.
func RegisterDefaultOutputLints(svc *Service) {
	if svc == nil {
		return
	}
	svc.RegisterOutputLint(NoPlanningOnlyFinish())
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
// Custom services built directly via agent.New(...).Build() are NOT
// touched here; they keep the previous behavior unless the caller invokes
// RegisterDefaultOutputLints or RegisterOutputLint themselves.
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
