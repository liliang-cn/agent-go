package agent

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// maxToolDiscoveryCallsPerRun bounds how many distinct tool searches a single
// run may execute. A model that cannot find a tool for the request otherwise
// rewords the query indefinitely — every reword is a new signature, so plain
// duplicate detection never fires and the run burns its whole turn budget on
// discovery without ever executing anything.
//
// One, because that is what the measurement supports. On agentbench (30 tasks,
// paired, identical tooling and judge) a budget of 3 never binds: 13 tasks up,
// 13 down, sign test p=1.00 — indistinguishable from noise. A budget of 1 cuts
// total tool calls 28% (17 down, 5 up, p=0.017) with task completion unchanged.
//
// The catalog does not change mid-run, so a second search over the same catalog
// is a reworded version of the first. If one search finds nothing usable, the
// answer is to answer directly or block — not to search again.
const maxToolDiscoveryCallsPerRun = 1

// discoverySignaturePrefix marks dedup keys that belong to tool-discovery
// calls so they can be counted against the budget. Regular keys are
// "<name>:map[...]", which cannot collide with this form.
const discoverySignaturePrefix = "tool-discovery:"

// toolDiscoveryBudgetGuidance replaces the refused search. Refusing silently
// is not enough — the model needs an explicit way out, or it retries.
var toolDiscoveryBudgetGuidance = fmt.Sprintf(
	"Tool discovery budget exhausted: %d different tool searches in this task returned nothing usable. "+
		"Do not search for tools again. Either answer the user directly from your own knowledge, "+
		"or call task_blocked naming the specific capability that is missing.",
	maxToolDiscoveryCallsPerRun,
)

// toolDiscoveryRepeatGuidance replaces a search whose query was already run in
// this task. Deliberately does not mention task_blocked: the model still has
// budget left and may have a better query to try.
const toolDiscoveryRepeatGuidance = "This tool search was already executed in this task and cannot return anything new. " +
	"Use the tools it already returned, or try a genuinely different capability."

// toolCallSignature is the per-run dedup key for a tool call.
//
// For tool searches the query is normalized first, so casing, spacing, and
// word order do not create bogus distinct searches. Everything else keeps the
// verbatim name+arguments form: a re-read after a write must stay distinct.
func toolCallSignature(tc domain.ToolCall) string {
	if isSearchToolName(tc.Function.Name) {
		return discoverySignaturePrefix + normalizeSearchQuery(tc.Function.Arguments)
	}
	return fmt.Sprintf("%s:%v", tc.Function.Name, tc.Function.Arguments)
}

// normalizeSearchQuery reduces search arguments to a canonical token set.
//
// This only collapses formatting churn ("Send  EMAIL" == "email send"). It
// deliberately does not attempt stemming or synonyms — "email" vs "mail
// sender" stays distinct, which is exactly why the budget above exists.
func normalizeSearchQuery(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}

	argKeys := make([]string, 0, len(args))
	for k := range args {
		argKeys = append(argKeys, k)
	}
	slices.Sort(argKeys)

	var raw strings.Builder
	for _, k := range argKeys {
		if text, ok := args[k].(string); ok {
			raw.WriteString(text)
			raw.WriteByte(' ')
		}
	}
	return normalizeQueryText(raw.String())
}

// normalizeQueryText canonicalizes a search query to a sorted, deduplicated
// token set so that casing, spacing, punctuation, and word order do not make
// the same search look new.
func normalizeQueryText(text string) string {
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	slices.Sort(tokens)
	return strings.Join(slices.Compact(tokens), " ")
}
