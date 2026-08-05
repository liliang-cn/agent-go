package agent

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Turning a PTC run into an answer for the user.
//
// PTCResult.Output comes from FormatForLLM — the name says who it is for. It is
// an execution report ("Code execution completed / Status: Success ✅" plus the
// script's return value) meant to go back to the model as a tool result. Using
// it as the final answer is what made a PTC turn reply with raw JSON, or with
// the half sentence the model managed before it called execute_javascript, and
// in both cases drop whatever the system prompt asks every reply to end with.
//
// So: if the model already said something usable, that is the answer. If it did
// not — it called the tool and stopped — the report goes back to the model for
// one summarising round, which is also what makes the system prompt's rules
// (tone, a required trailing tag, a language) apply to the result.

// ptcAnswerPrompt asks for the user-facing sentence. Deliberately terse: the
// system prompt already governs how to answer, and repeating those rules here
// would let the two drift.
const ptcAnswerPrompt = "以上是刚刚执行的结果。请直接面向用户给出最终回复：" +
	"说明做了什么、结果是什么。不要粘贴原始 JSON 或执行报告，不要提到脚本或代码。" +
	"遵循你在系统提示里被要求的回复格式。"

// ptcCodeBlockRe matches one whole <code>…</code> block, PTC's call protocol.
var ptcCodeBlockRe = regexp.MustCompile(`(?s)<code>.*?</code>`)

// stripPTCCodeBlocks drops PTC call blocks and keeps the surrounding prose.
//
// The summarising round below is given the agent's own system prompt — which is
// what makes the answer obey the agent's tone, language and required trailing tag
// — and that prompt also teaches PTC. So the model tends to reply in PTC form
// even when asked for a sentence: a <code>return "…"</code> wrapping the answer,
// then the answer. Keeping the prose and dropping the block gets the sentence
// without having to give this round a different personality.
func stripPTCCodeBlocks(text string) string {
	return strings.TrimSpace(ptcCodeBlockRe.ReplaceAllString(text, "\n"))
}

// looksLikeExecutionReport reports whether text is machine-facing rather than an
// answer: the report FormatForLLM produces, or a bare JSON blob.
func looksLikeExecutionReport(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	for _, marker := range []string{
		"Code execution completed",
		"**Status:** Success",
		"**Status:** Failed",
		"execute_javascript failed",
		"Direct tool-call fallback executed",
	} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	// A payload, not a sentence.
	if (strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}")) ||
		(strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")) {
		return true
	}
	// A <code>…</code> block is PTC's call protocol (see service_ptc.go), so text
	// carrying one is the model asking to run something, not telling the user
	// anything. On the streaming path those blocks arrive as separate step events
	// and the UI renders them as progress, so nobody notices; off it, the whole
	// transcript — script plus the tool's JSON — was being handed over as the
	// answer, which is what a scheduled run delivered.
	if strings.Contains(t, "<code>") && strings.Contains(t, "</code>") {
		return true
	}
	return false
}

// ptcFinalAnswer decides what the user sees after a PTC turn.
//
// modelSaid is whatever text the model produced alongside its tool call;
// report is PTCResult.Output. Returns the answer to use.
func (s *Service) ptcFinalAnswer(ctx context.Context, goal string, session *Session, modelSaid, report string) string {
	// The model already answered: keep it. This is the common case, and it is
	// the one the old code broke by overwriting it with the report.
	if !looksLikeExecutionReport(modelSaid) {
		return modelSaid
	}
	if strings.TrimSpace(report) == "" {
		return modelSaid
	}
	if s.llmService == nil {
		return report
	}

	messages := []domain.Message{
		{Role: "system", Content: s.buildSystemPrompt(ctx, s.resolveCurrentAgent(session))},
		{Role: "user", Content: goal},
		{Role: "assistant", Content: report},
		{Role: "user", Content: ptcAnswerPrompt},
	}

	// No tools on purpose: this round is for wording, and offering tools invites
	// another round of work.
	res, err := s.llmService.GenerateWithTools(ctx, messages, nil, &domain.GenerationOptions{})
	if err != nil || res == nil || strings.TrimSpace(res.Content) == "" {
		if err != nil {
			s.logger.Warn("PTC answer summarisation failed; falling back to the execution report",
				slog.Any("error", err))
		}
		// Falling back to the report is ugly but truthful — better than an empty
		// reply that hides a completed job.
		return report
	}
	if answer := stripPTCCodeBlocks(res.Content); answer != "" {
		return answer
	}
	// The round replied with nothing but a code block. Its return value is
	// usually the answer, so un-tag and flatten rather than surrender to the
	// report.
	if inline := extractInlineTaskCompleteResult(res.Content); inline != "" {
		return inline
	}
	return report
}
