package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// PIIKind identifies a category of Personally Identifiable Information the
// PII guardrail can detect. Empty PIIConfig.Kinds means "all kinds".
type PIIKind string

const (
	PIIEmail      PIIKind = "email"       // e-mail addresses
	PIIPhoneUS    PIIKind = "phone_us"    // US-style 10-digit phone numbers
	PIICreditCard PIIKind = "credit_card" // 16-digit grouped card numbers (Luhn-checked)
	PIISSN        PIIKind = "ssn"         // US Social Security numbers
	PIICNID       PIIKind = "cn_id"       // 中国居民身份证 (18 digits, checksum-validated)
	PIICNMobile   PIIKind = "cn_mobile"   // 中国大陆手机号 (1[3-9] + 9 digits)
	PIIBankCard   PIIKind = "bank_card"   // 13-19 contiguous digits (Luhn-checked)
	PIIPassport   PIIKind = "passport"    // letter-prefixed passport-style ids
	PIIIPv4       PIIKind = "ipv4"        // IPv4 addresses
)

// AllPIIKinds is the canonical ordering used when PIIConfig.Kinds is empty.
var AllPIIKinds = []PIIKind{
	PIIEmail,
	PIISSN,
	PIICNID,
	PIICreditCard,
	PIIBankCard,
	PIICNMobile,
	PIIPhoneUS,
	PIIIPv4,
	PIIPassport,
}

// RedactMode controls how a detected PII span is rewritten.
type RedactMode string

const (
	// RedactMask replaces the whole match with [REDACTED_<KIND>].
	RedactMask RedactMode = "mask"
	// RedactPartial keeps a head/tail hint (e.g. 138****1234, a***@b.com).
	RedactPartial RedactMode = "partial"
	// RedactHash replaces the match with [<KIND>:<short-sha256>] — stable and
	// pseudonymous, so the same value always maps to the same token.
	RedactHash RedactMode = "hash"
	// RedactBlock does not rewrite; it marks the whole content blocked so the
	// run can refuse instead of forwarding the value.
	RedactBlock RedactMode = "block"
)

// PIIConfig configures a PII guardrail.
type PIIConfig struct {
	// Kinds restricts detection to the listed categories. Empty = all kinds.
	Kinds []PIIKind
	// Mode selects the redaction strategy. Zero value defaults to RedactPartial.
	Mode RedactMode
	// Kind selects when the guardrail applies (input, before the LLM, or
	// output, on the final answer). Zero value defaults to GuardrailKindInput.
	Kind GuardrailKind
}

// piiOptions is the accumulator behind Builder.WithPIIRedaction.
type piiOptions struct {
	kinds      []PIIKind
	mode       RedactMode
	inputOnly  bool
	outputOnly bool
}

// PIIOption tunes Builder.WithPIIRedaction.
type PIIOption func(*piiOptions)

// WithPIIKinds restricts redaction to the listed PII categories (default: all).
func WithPIIKinds(kinds ...PIIKind) PIIOption {
	return func(o *piiOptions) { o.kinds = kinds }
}

// WithPIIMode selects the redaction strategy (default: RedactPartial).
func WithPIIMode(mode RedactMode) PIIOption {
	return func(o *piiOptions) { o.mode = mode }
}

// WithPIIInputOnly redacts only before the LLM (skip the output guardrail).
func WithPIIInputOnly() PIIOption {
	return func(o *piiOptions) { o.inputOnly = true; o.outputOnly = false }
}

// WithPIIOutputOnly redacts only the final answer / memory (skip the input guardrail).
func WithPIIOutputOnly() PIIOption {
	return func(o *piiOptions) { o.outputOnly = true; o.inputOnly = false }
}

// piiDetector pairs a compiled regex with an optional validator (checksum /
// Luhn) and an overlap-resolution priority (higher wins when spans collide).
type piiDetector struct {
	kind     PIIKind
	re       *regexp.Regexp
	validate func(string) bool
	priority int
}

// Compiled once. Word boundaries keep the numeric patterns from matching
// digits embedded in a longer run (e.g. a 10-digit phone inside an 11-digit
// mobile number), and overlaps that survive are resolved by priority below.
var piiDetectors = map[PIIKind]piiDetector{
	PIIEmail: {
		kind:     PIIEmail,
		re:       regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
		priority: 90,
	},
	PIISSN: {
		kind:     PIISSN,
		re:       regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
		priority: 85,
	},
	PIICNID: {
		kind:     PIICNID,
		re:       regexp.MustCompile(`\b\d{17}[\dXx]\b`),
		validate: validCNID,
		priority: 80,
	},
	PIICreditCard: {
		kind:     PIICreditCard,
		re:       regexp.MustCompile(`\b\d{4}[-\s]\d{4}[-\s]\d{4}[-\s]\d{4}\b`),
		validate: validLuhnLoose,
		priority: 75,
	},
	PIIBankCard: {
		kind:     PIIBankCard,
		re:       regexp.MustCompile(`\b\d{13,19}\b`),
		validate: validLuhn,
		priority: 70,
	},
	PIICNMobile: {
		kind:     PIICNMobile,
		re:       regexp.MustCompile(`\b1[3-9]\d{9}\b`),
		priority: 65,
	},
	PIIPhoneUS: {
		kind:     PIIPhoneUS,
		re:       regexp.MustCompile(`\b\d{3}[-.\s]\d{3}[-.\s]\d{4}\b`),
		priority: 60,
	},
	PIIIPv4: {
		kind:     PIIIPv4,
		re:       regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\b`),
		priority: 55,
	},
	PIIPassport: {
		kind:     PIIPassport,
		re:       regexp.MustCompile(`\b[A-Za-z]{1,2}\d{7,8}\b`),
		priority: 30,
	},
}

// NewPIIGuardrail builds a configurable PII guardrail. It supersedes the basic
// PIIDetectionGuardrail (kept for back-compat). Detection is well-tested per
// kind; RedactBlock marks the content blocked instead of rewriting it.
func NewPIIGuardrail(cfg PIIConfig) *Guardrail {
	mode := cfg.Mode
	if mode == "" {
		mode = RedactPartial
	}
	kind := cfg.Kind
	if kind == "" {
		kind = GuardrailKindInput
	}
	kinds := cfg.Kinds
	if len(kinds) == 0 {
		kinds = AllPIIKinds
	}
	dets := detectorsFor(kinds)

	name := "pii_" + string(mode)
	return NewGuardrail(
		name,
		kind,
		func(ctx context.Context, content string, _ GuardrailKind) (*GuardrailResult, error) {
			redacted, detected, blocked := redactPII(content, dets, mode)
			if len(detected) == 0 {
				return &GuardrailResult{Passed: true}, nil
			}
			kindStrs := make([]string, len(detected))
			for i, k := range detected {
				kindStrs[i] = string(k)
			}
			meta := map[string]interface{}{"pii_kinds": kindStrs}
			if blocked {
				return &GuardrailResult{
					Passed:   false,
					Reason:   "content contains PII: " + strings.Join(kindStrs, ", "),
					Metadata: meta,
				}, nil
			}
			return &GuardrailResult{
				Passed:   true,
				Modified: true,
				Content:  redacted,
				Reason:   "redacted PII: " + strings.Join(kindStrs, ", "),
				Metadata: meta,
			}, nil
		},
		WithGuardrailDescription("Detects and redacts configurable PII (email/phone/card/id/ip)"),
	)
}

// detectorsFor returns the detectors for the requested kinds, skipping unknowns.
func detectorsFor(kinds []PIIKind) []piiDetector {
	out := make([]piiDetector, 0, len(kinds))
	for _, k := range kinds {
		if d, ok := piiDetectors[k]; ok {
			out = append(out, d)
		}
	}
	return out
}

// piiMatch is a single detected span.
type piiMatch struct {
	start, end int
	kind       PIIKind
	priority   int
	text       string
}

// redactPII finds every configured PII span, resolves overlaps by priority,
// and rewrites the surviving spans according to mode. Returns the (possibly
// unchanged) content, the ordered-unique kinds detected, and whether the
// content should be blocked (RedactBlock with at least one hit).
func redactPII(content string, dets []piiDetector, mode RedactMode) (string, []PIIKind, bool) {
	var matches []piiMatch
	for _, d := range dets {
		for _, loc := range d.re.FindAllStringIndex(content, -1) {
			s := content[loc[0]:loc[1]]
			if d.validate != nil && !d.validate(s) {
				continue
			}
			matches = append(matches, piiMatch{start: loc[0], end: loc[1], kind: d.kind, priority: d.priority, text: s})
		}
	}
	if len(matches) == 0 {
		return content, nil, false
	}

	// Resolve overlaps: prefer higher priority, then longer span, then earlier
	// start. A candidate is accepted only if it doesn't overlap an accepted one.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].priority != matches[j].priority {
			return matches[i].priority > matches[j].priority
		}
		li, lj := matches[i].end-matches[i].start, matches[j].end-matches[j].start
		if li != lj {
			return li > lj
		}
		return matches[i].start < matches[j].start
	})
	var accepted []piiMatch
	overlaps := func(m piiMatch) bool {
		for _, a := range accepted {
			if m.start < a.end && a.start < m.end {
				return true
			}
		}
		return false
	}
	for _, m := range matches {
		if !overlaps(m) {
			accepted = append(accepted, m)
		}
	}

	// Order accepted spans by position for deterministic detected-kind ordering
	// and single-pass rebuild.
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].start < accepted[j].start })

	detected := make([]PIIKind, 0, len(accepted))
	seen := make(map[PIIKind]bool)
	for _, a := range accepted {
		if !seen[a.kind] {
			seen[a.kind] = true
			detected = append(detected, a.kind)
		}
	}

	if mode == RedactBlock {
		return content, detected, true
	}

	var b strings.Builder
	last := 0
	for _, a := range accepted {
		b.WriteString(content[last:a.start])
		b.WriteString(redactSpan(a.kind, a.text, mode))
		last = a.end
	}
	b.WriteString(content[last:])
	return b.String(), detected, false
}

// redactSpan rewrites a single matched span per mode.
func redactSpan(kind PIIKind, s string, mode RedactMode) string {
	switch mode {
	case RedactMask:
		return "[REDACTED_" + strings.ToUpper(string(kind)) + "]"
	case RedactHash:
		sum := sha256.Sum256([]byte(s))
		return "[" + strings.ToUpper(string(kind)) + ":" + hex.EncodeToString(sum[:])[:8] + "]"
	case RedactPartial:
		return partialRedact(kind, s)
	default:
		return "[REDACTED_" + strings.ToUpper(string(kind)) + "]"
	}
}

// partialRedact keeps a per-kind head/tail hint while masking the middle.
func partialRedact(kind PIIKind, s string) string {
	switch kind {
	case PIIEmail:
		at := strings.Index(s, "@")
		if at <= 0 {
			return maskAll(s)
		}
		local, domain := s[:at], s[at:]
		if len(local) <= 1 {
			return "*" + "***" + domain
		}
		return local[:1] + "***" + domain
	case PIIPhoneUS, PIICNMobile:
		digits := onlyDigits(s)
		if len(digits) < 7 {
			return maskAll(s)
		}
		return digits[:3] + "****" + digits[len(digits)-4:]
	case PIICNID:
		if len(s) < 7 {
			return maskAll(s)
		}
		return s[:3] + strings.Repeat("*", len(s)-7) + s[len(s)-4:]
	case PIICreditCard, PIIBankCard, PIISSN:
		return maskAllButLastDigits(s, 4)
	case PIIIPv4:
		if i := strings.Index(s, "."); i > 0 {
			return s[:i] + ".*.*.*"
		}
		return maskAll(s)
	case PIIPassport:
		if len(s) <= 2 {
			return maskAll(s)
		}
		return s[:2] + strings.Repeat("*", len(s)-2)
	default:
		return maskAll(s)
	}
}

// maskAllButLastDigits masks every digit except the last keep digits,
// preserving non-digit separators (dashes, spaces).
func maskAllButLastDigits(s string, keep int) string {
	total := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			total++
		}
	}
	cut := total - keep
	out := []rune(s)
	seen := 0
	for i, r := range out {
		if r >= '0' && r <= '9' {
			if seen < cut {
				out[i] = '*'
			}
			seen++
		}
	}
	return string(out)
}

// maskAll replaces every alphanumeric rune with '*', preserving separators.
func maskAll(s string) string {
	out := []rune(s)
	for i, r := range out {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			out[i] = '*'
		}
	}
	return string(out)
}

// onlyDigits strips everything but 0-9.
func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validCNID validates a 中国居民身份证 (18 digits) via the GB 11643-1999
// ISO 7064 MOD 11-2 checksum, cutting false positives on arbitrary 18-digit runs.
func validCNID(s string) bool {
	if len(s) != 18 {
		return false
	}
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	const check = "10X98765432"
	sum := 0
	for i := 0; i < 17; i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
		sum += int(c-'0') * weights[i]
	}
	last := s[17]
	if last == 'x' {
		last = 'X'
	}
	return check[sum%11] == last
}

// validLuhn runs the Luhn checksum over a string of contiguous digits.
func validLuhn(s string) bool {
	if s == "" {
		return false
	}
	sum := 0
	alt := false
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			return false
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
	}
	return sum%10 == 0
}

// validLuhnLoose strips separators before running Luhn, for grouped card numbers.
func validLuhnLoose(s string) bool {
	return validLuhn(onlyDigits(s))
}
