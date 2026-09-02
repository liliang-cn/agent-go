// Package pii keeps personal data and secrets out of the model's context and
// out of the final answer.
//
// It works at two seams. After a tool runs, the result is walked and every
// match is masked before the model sees it — the model cannot leak what it
// never received. When the model produces a final answer, a lint rejects one
// that still carries a match and re-prompts with the reason, so the answer
// the caller gets is clean by construction rather than by cleanup.
//
// The patterns read tool output and model output, never the user's request:
// that is the output side, where deterministic checks belong.
package pii

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// Kind is a category of sensitive data.
type Kind string

const (
	Email      Kind = "email"
	Phone      Kind = "phone"
	CreditCard Kind = "credit_card"
	Secret     Kind = "secret"
)

// AllKinds is every category the extension knows.
var AllKinds = []Kind{Email, Phone, CreditCard, Secret}

// Option configures the extension.
type Option func(*Extension)

// WithKinds restricts detection to these categories. Default: all.
func WithKinds(kinds ...Kind) Option {
	return func(e *Extension) { e.kinds = append([]Kind(nil), kinds...) }
}

// WithToolResultRedaction turns masking of tool results on or off. Default on.
func WithToolResultRedaction(on bool) Option {
	return func(e *Extension) { e.redactResults = on }
}

// WithFinalAnswerLint turns the final-answer check on or off. Default on.
func WithFinalAnswerLint(on bool) Option {
	return func(e *Extension) { e.lintAnswer = on }
}

// WithBlockedTools refuses calls to these tools when their arguments carry a
// match — for tools that send data somewhere (web search, email, webhooks).
// Default: none.
func WithBlockedTools(names ...string) Option {
	return func(e *Extension) {
		for _, n := range names {
			e.blocked[n] = true
		}
	}
}

// Extension implements agent.Extension, agent.ToolResultFilter,
// agent.ToolCallFilter and agent.OutputLint.
type Extension struct {
	kinds         []Kind
	redactResults bool
	lintAnswer    bool
	blocked       map[string]bool

	mu    sync.Mutex
	stats map[Kind]int
}

// New returns the extension with the given options applied.
func New(opts ...Option) *Extension {
	e := &Extension{
		kinds:         append([]Kind(nil), AllKinds...),
		redactResults: true,
		lintAnswer:    true,
		blocked:       map[string]bool{},
		stats:         map[Kind]int{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Name implements agent.Extension.
func (e *Extension) Name() string { return "pii" }

// Stats reports how many matches were masked, by kind, since construction.
func (e *Extension) Stats() map[Kind]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[Kind]int, len(e.stats))
	for k, v := range e.stats {
		out[k] = v
	}
	return out
}

// AfterTool implements agent.ToolResultFilter.
func (e *Extension) AfterTool(_ context.Context, res agent.ToolResultInfo) (interface{}, bool, error) {
	if !e.redactResults || res.Result == nil {
		return nil, false, nil
	}
	out, changed, err := e.redactValue(res.Result)
	if err != nil {
		// Fail closed: a result we could not inspect is not one the model
		// may see.
		return nil, false, fmt.Errorf("pii: could not inspect result of %s: %w", res.Name, err)
	}
	return out, changed, nil
}

// BeforeTool implements agent.ToolCallFilter.
func (e *Extension) BeforeTool(_ context.Context, call agent.ToolCallInfo) (agent.ToolVerdict, error) {
	if !e.blocked[call.Name] {
		return agent.ToolVerdict{}, nil
	}
	raw, err := json.Marshal(call.Args)
	if err != nil {
		return agent.ToolVerdict{}, fmt.Errorf("pii: could not inspect arguments of %s: %w", call.Name, err)
	}
	if kinds := e.find(string(raw)); len(kinds) > 0 {
		return agent.ToolVerdict{Block: fmt.Sprintf(
			"its arguments contain %s; this tool sends data out, so mask or drop them first", describe(kinds))}, nil
	}
	return agent.ToolVerdict{}, nil
}

// Check implements agent.OutputLint.
func (e *Extension) Check(text string, _ agent.LintContext) (bool, string) {
	if !e.lintAnswer {
		return true, ""
	}
	kinds := e.find(text)
	if len(kinds) == 0 {
		return true, ""
	}
	return false, fmt.Sprintf(
		"The answer contains %s. Mask it (keep only what the user needs, for example j***@example.com or ***1234) and answer again.",
		describe(kinds))
}

// Redact masks every match in s and reports which kinds were found.
func (e *Extension) Redact(s string) (string, []Kind) {
	var found []Kind
	for _, k := range e.kinds {
		d := detectors[k]
		if d == nil {
			continue
		}
		hit := false
		s = d.re.ReplaceAllStringFunc(s, func(m string) string {
			if d.verify != nil && !d.verify(m) {
				return m
			}
			hit = true
			e.count(k)
			return d.mask(m)
		})
		if hit {
			found = append(found, k)
		}
	}
	return s, found
}

func (e *Extension) find(s string) []Kind {
	var found []Kind
	for _, k := range e.kinds {
		d := detectors[k]
		if d == nil {
			continue
		}
		for _, m := range d.re.FindAllString(s, -1) {
			if d.verify == nil || d.verify(m) {
				found = append(found, k)
				break
			}
		}
	}
	return found
}

func (e *Extension) count(k Kind) {
	e.mu.Lock()
	e.stats[k]++
	e.mu.Unlock()
}

// redactValue walks strings, maps and slices; anything else goes through a
// JSON round trip so its structure survives.
func (e *Extension) redactValue(v interface{}) (interface{}, bool, error) {
	switch x := v.(type) {
	case string:
		out, kinds := e.Redact(x)
		return out, len(kinds) > 0, nil
	case []string:
		changed := false
		out := make([]string, len(x))
		for i, s := range x {
			r, kinds := e.Redact(s)
			out[i] = r
			changed = changed || len(kinds) > 0
		}
		return out, changed, nil
	case []interface{}:
		changed := false
		out := make([]interface{}, len(x))
		for i, item := range x {
			r, c, err := e.redactValue(item)
			if err != nil {
				return nil, false, err
			}
			out[i] = r
			changed = changed || c
		}
		return out, changed, nil
	case map[string]interface{}:
		changed := false
		out := make(map[string]interface{}, len(x))
		for k, item := range x {
			r, c, err := e.redactValue(item)
			if err != nil {
				return nil, false, err
			}
			out[k] = r
			changed = changed || c
		}
		return out, changed, nil
	case map[string]string:
		changed := false
		out := make(map[string]string, len(x))
		for k, s := range x {
			r, kinds := e.Redact(s)
			out[k] = r
			changed = changed || len(kinds) > 0
		}
		return out, changed, nil
	case nil, bool, int, int64, float64:
		return v, false, nil
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, false, err
		}
		red, kinds := e.Redact(string(raw))
		if len(kinds) == 0 {
			return v, false, nil
		}
		var back interface{}
		if err := json.Unmarshal([]byte(red), &back); err != nil {
			// Masking broke the JSON (a match inside a key, say); hand the
			// model the masked text rather than the original structure.
			return red, true, nil
		}
		return back, true, nil
	}
}

func describe(kinds []Kind) string {
	names := map[Kind]string{
		Email: "an email address", Phone: "a phone number",
		CreditCard: "a card number", Secret: "a credential",
	}
	seen := map[Kind]bool{}
	var parts []string
	for _, k := range kinds {
		if seen[k] {
			continue
		}
		seen[k] = true
		parts = append(parts, names[k])
	}
	sort.Strings(parts)
	switch len(parts) {
	case 0:
		return "sensitive data"
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

type detector struct {
	re     *regexp.Regexp
	verify func(string) bool
	mask   func(string) string
}

var detectors = map[Kind]*detector{
	Email: {
		re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
		mask: func(m string) string {
			at := strings.Index(m, "@")
			return m[:1] + "***" + m[at:]
		},
	},
	// Three shapes with low false-positive rates: E.164 with a leading +,
	// North American 3-3-4 with separators, and Chinese mobile numbers.
	Phone: {
		re:   regexp.MustCompile(`\+\d{1,3}[\s-]?\d{2,4}[\s-]?\d{3,4}[\s-]?\d{3,4}\b|\(?\b\d{3}\)?[\s.-]\d{3}[\s.-]\d{4}\b|\b1[3-9]\d{9}\b`),
		mask: func(m string) string { return "***" + tail(m, 4) },
	},
	CreditCard: {
		re:     regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`),
		verify: luhn,
		mask:   func(m string) string { return "**** **** **** " + tail(m, 4) },
	},
	Secret: {
		re: regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{36}|xox[baprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35}|t2m-[0-9a-f]{40})\b|Bearer\s+[A-Za-z0-9._-]{20,}`),
		mask: func(m string) string {
			if len(m) <= 4 {
				return "[redacted]"
			}
			return m[:4] + "…[redacted]"
		},
	},
}

func tail(s string, n int) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(digits) <= n {
		return digits
	}
	return digits[len(digits)-n:]
}

func luhn(s string) bool {
	sum, alt, n := 0, false, 0
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
		n++
	}
	return n >= 13 && sum%10 == 0
}
