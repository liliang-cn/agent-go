package agent

import (
	"encoding/json"
	"errors"
	"strings"
)

// errNoJSONObject is returned when a reply contains nothing object-shaped.
var errNoJSONObject = errors.New("no JSON object found in response")

// extractJSONObject pulls the first complete JSON object out of a model reply.
//
// Providers that advertise structured output do not reliably deliver it. The
// same gateway returns bare JSON on one call and, on the next, a code fence, a
// "Based on the user request, ..." preamble, or prose wrapped around the object.
// A strict json.Unmarshal on the raw reply fails on all three, and a failed
// parse means a constraint the user actually stated is silently not enforced —
// which is worse than the extra work of parsing leniently.
//
// Scanning is brace-balanced and string-aware, so an object containing braces
// inside a string value (a description, a path) does not truncate the match the
// way LastIndex("}") would.
func extractJSONObject(raw string) ([]byte, error) {
	s := stripJSONCodeFences(raw)

	start := strings.Index(s, "{")
	if start < 0 {
		return nil, errNoJSONObject
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		switch {
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// Braces inside a string are data, not structure.
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				candidate := s[start : i+1]
				if json.Valid([]byte(candidate)) {
					return []byte(candidate), nil
				}
				return nil, errNoJSONObject
			}
		}
	}
	return nil, errNoJSONObject
}

// stripJSONCodeFences removes a leading ```json / ``` fence and its closer.
func stripJSONCodeFences(s string) string {
	s = strings.TrimSpace(s)
	idx := strings.Index(s, "```")
	if idx < 0 {
		return s
	}
	s = s[idx+3:]
	// The opening fence may carry a language tag on the same line.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		if tag := strings.TrimSpace(s[:nl]); tag == "" || !strings.ContainsAny(tag, "{[\"") {
			s = s[nl+1:]
		}
	}
	if end := strings.Index(s, "```"); end >= 0 {
		s = s[:end]
	}
	return strings.TrimSpace(s)
}
