package agent

import (
	"regexp"
	"strings"
)

// extractBetweenTags extracts content between the first occurrence of startTag and
// the matching endTag. Returns empty string if tags are not found or malformed.
func extractBetweenTags(content, startTag, endTag string) string {
	startIdx := strings.Index(content, startTag)
	if startIdx == -1 {
		return ""
	}
	codeStart := startIdx + len(startTag)
	endIdx := strings.Index(content[codeStart:], endTag)
	if endIdx == -1 {
		return ""
	}
	return strings.TrimSpace(content[codeStart : codeStart+endIdx])
}

// sanitiseJSCode strips non-JavaScript content that models sometimes append to the
// code argument. It handles three common failure modes:
//  1. The model wraps code in markdown fences (```javascript ... ```)
//  2. The model appends free-form prose or JSON after valid JS
//  3. The model uses async/await which is not supported by the synchronous sandbox
//
// The function is conservative — if no obvious contamination is detected, the input
// is returned unchanged so we don't accidentally break valid code.
func sanitiseJSCode(code string) string {
	// Normalise line endings — models sometimes emit \r\n
	code = strings.ReplaceAll(code, "\r\n", "\n")
	code = strings.ReplaceAll(code, "\r", "\n")
	trimmed := strings.TrimSpace(code)

	// Pattern 1: code is wrapped in markdown fences
	for _, prefix := range []string{"```javascript\n", "```js\n", "```\n"} {
		if strings.HasPrefix(trimmed, prefix) {
			rest := trimmed[len(prefix):]
			if endIdx := strings.Index(rest, "```"); endIdx != -1 {
				trimmed = strings.TrimSpace(rest[:endIdx])
				break
			}
		}
	}

	// Pattern 2: valid JS followed by a bare JSON block (e.g. {"queries": [...]})
	// or natural language. Detect by finding the last 'return' statement,
	// then trim everything after the statement's expression ends.
	if idx := findTrailingNonJS(trimmed); idx > 0 && idx <= len(trimmed) {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}

	// Pattern 3: strip async/await for the synchronous Goja sandbox
	trimmed = strings.ReplaceAll(trimmed, "async ", "")
	trimmed = strings.ReplaceAll(trimmed, "await ", "")

	return trimmed
}

// findTrailingNonJS looks for the boundary where valid JS ends and garbage begins.
// Returns the byte offset of the boundary, or -1 if the entire string looks clean.
func findTrailingNonJS(code string) int {
	lines := strings.Split(code, "\n")

	// Heuristic 1: find the last top-level return statement and check for
	// garbage after its semicolon — even on the same line.
	braceDepth := 0
	for i, line := range lines {
		trimLine := strings.TrimSpace(line)

		// For return lines, only count braces in the JS portion (before ';')
		// to avoid garbage like `return result;{...}` polluting the depth.
		braceCountLine := trimLine
		if strings.HasPrefix(trimLine, "return ") {
			if si := strings.Index(trimLine, ";"); si >= 0 {
				braceCountLine = trimLine[:si+1]
			}
		}
		braceDepth += strings.Count(braceCountLine, "{") - strings.Count(braceCountLine, "}")

		if strings.HasPrefix(trimLine, "return ") && braceDepth <= 0 {
			// Walk forward to find the ';' that terminates this return
			for j := i; j < len(lines); j++ {
				tl := strings.TrimSpace(lines[j])
				semiIdx := strings.Index(tl, ";")
				if semiIdx >= 0 {
					// Check for garbage AFTER the ';' on the same line
					afterSemi := strings.TrimSpace(tl[semiIdx+1:])
					if afterSemi != "" && !looksLikeJS(afterSemi) {
						// Compute byte offset up to this line + the ';' position
						offset := 0
						for k := 0; k < j; k++ {
							offset += len(lines[k]) + 1
						}
						// Find the actual ';' in the raw (non-trimmed) line
						rawSemiIdx := strings.Index(lines[j], ";")
						return offset + rawSemiIdx + 1
					}
					// ';' is at end of line — check lines AFTER
					lineEnd := 0
					for k := 0; k <= j; k++ {
						lineEnd += len(lines[k]) + 1
					}
					if lineEnd > len(code) {
						lineEnd = len(code)
					}
					if lineEnd < len(code) {
						trailing := strings.TrimSpace(code[lineEnd:])
						if trailing != "" && !looksLikeJS(trailing) {
							return lineEnd
						}
					}
					break
				}
				// If last line of the return has no ';', treat line end as boundary
				if j == len(lines)-1 {
					lineEnd := 0
					for k := 0; k <= j; k++ {
						lineEnd += len(lines[k]) + 1
					}
					if lineEnd > len(code) {
						lineEnd = len(code)
					}
					return lineEnd
				}
			}
		}
	}

	// Heuristic 2: scan for lines that start with common non-JS patterns
	offset := 0
	for _, line := range lines {
		trimLine := strings.TrimSpace(line)
		if isNonJSBoundary(trimLine) && offset > 0 {
			preceding := strings.TrimSpace(code[:offset])
			if preceding != "" && looksLikeCompleteJS(preceding) {
				return offset
			}
		}
		offset += len(line) + 1
	}

	return -1
}

// isNonJSBoundary checks if a line signals the start of non-JS content.
func isNonJSBoundary(line string) bool {
	if line == "" {
		return false
	}
	// Bare JSON object on its own line (not an assignment/block)
	if strings.HasPrefix(line, "{") && !isJSContextLine(line) {
		return true
	}
	return isNaturalLanguageLine(line)
}

// looksLikeJS checks if a string fragment looks like JavaScript.
func looksLikeJS(s string) bool {
	jsStarts := []string{"//", "/*", "const ", "let ", "var ", "function ", "if ", "for ",
		"while ", "return ", "try ", "catch ", "class ", "import ", "export "}
	first := strings.TrimSpace(s)
	for _, prefix := range jsStarts {
		if strings.HasPrefix(first, prefix) {
			return true
		}
	}
	return false
}

// isJSContextLine checks if a line starting with '{' is plausibly JS (e.g. block, object literal in context).
func isJSContextLine(line string) bool {
	if line == "{" {
		return true
	}
	// Object literal patterns: { key: value or { "key": value
	jsObjPattern := regexp.MustCompile(`^\{\s*(?:\w+|"[^"]+"|'[^']+')\s*:`)
	return !jsObjPattern.MatchString(line)
}

// looksLikeCompleteJS checks if a JS snippet ends in a way that suggests it's complete.
func looksLikeCompleteJS(code string) bool {
	trimmed := strings.TrimRight(code, " \t\n\r")
	if trimmed == "" {
		return false
	}
	lastChar := trimmed[len(trimmed)-1]
	return lastChar == ';' || lastChar == '}' || lastChar == ')' || lastChar == ']'
}

// isNaturalLanguageLine checks if a line looks like natural language or non-JS content.
func isNaturalLanguageLine(line string) bool {
	if line == "" {
		return false
	}
	// Markdown code fence for another language (e.g. ```python)
	if strings.HasPrefix(line, "```") {
		return true
	}
	nlPrefixes := []string{
		"It seems", "I ", "Unfortunately", "Here", "The ", "This ", "Please ",
		"Would ", "Could ", "Should ", "If you", "Let me", "Based on",
	}
	for _, prefix := range nlPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
