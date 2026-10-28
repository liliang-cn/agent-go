package log

import (
	"regexp"
	"strings"
)

// Secrets never reach a log line or a trace line, at any level — payload
// included. A payload-level trace is the one an operator turns on to read a
// failing request back, and it is exactly the one that would carry an
// Authorization header, an API key in a tool's arguments, or a key pasted
// into a prompt. Redaction runs on the output side: it reads what is about to
// be written, never what the user asked.

// Redacted is what a secret is replaced with.
const Redacted = "[REDACTED]"

var secretPatterns = []*regexp.Regexp{
	// "Authorization: Bearer xxx", "bearer xxx"
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`),
	// key=value / "key": "value" for names that only ever hold secrets.
	regexp.MustCompile(`(?i)("?(?:x-api-key|api[_-]?key|apikey|authorization|access[_-]?token|refresh[_-]?token|client[_-]?secret|secret[_-]?key|password|passwd)"?\s*[:=]\s*"?)[^"\s,;&}]{4,}`),
	// Provider key shapes, wherever they appear.
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
}

// Redact returns s with anything that looks like a credential replaced.
func Redact(s string) string {
	if s == "" {
		return s
	}
	for i, re := range secretPatterns {
		switch i {
		case 0:
			s = re.ReplaceAllString(s, "$1 "+Redacted)
		case 1:
			s = re.ReplaceAllString(s, "${1}"+Redacted)
		default:
			s = re.ReplaceAllString(s, Redacted)
		}
	}
	return s
}

// IsSecretKey reports whether a field or argument named k holds a secret by
// its name alone.
func IsSecretKey(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	k = strings.NewReplacer("-", "", "_", "").Replace(k)
	switch k {
	case "authorization", "xapikey", "apikey", "accesstoken", "refreshtoken", "clientsecret",
		"secret", "secretkey", "password", "passwd", "token", "bearer", "cookie", "setcookie":
		return true
	}
	return strings.HasSuffix(k, "apikey") || strings.HasSuffix(k, "secret") || strings.HasSuffix(k, "password")
}

// RedactValue returns v with secrets removed: a map's secret-named keys are
// replaced whole, strings are Redact-ed, and maps and slices are walked. The
// input is never modified.
func RedactValue(v any) any {
	switch t := v.(type) {
	case string:
		return Redact(t)
	case map[string]any:
		return RedactMap(t)
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if IsSecretKey(k) {
				out[k] = Redacted
			} else {
				out[k] = Redact(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = RedactValue(e)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = Redact(e)
		}
		return out
	}
	return v
}

// RedactMap is RedactValue for a map; nil stays nil.
func RedactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if IsSecretKey(k) {
			out[k] = Redacted
			continue
		}
		out[k] = RedactValue(v)
	}
	return out
}
