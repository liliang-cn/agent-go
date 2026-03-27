package agent

import "strings"

// normalizeTaskPrompt strips common routing and squad-envelope wrappers so
// downstream intent recognition sees the actual user task.
func normalizeTaskPrompt(prompt string) string {
	current := strings.TrimSpace(prompt)
	if current == "" {
		return ""
	}

	for i := 0; i < 3; i++ {
		next := unwrapTaskPromptOnce(current)
		next = strings.TrimSpace(next)
		if next == "" || next == current {
			break
		}
		current = next
	}

	return strings.TrimSpace(current)
}

func unwrapTaskPromptOnce(prompt string) string {
	if task := extractTaskSection(prompt); task != "" && task != strings.TrimSpace(prompt) {
		return task
	}
	if request := extractQuotedUserRequest(prompt); request != "" && request != strings.TrimSpace(prompt) {
		return request
	}
	return strings.TrimSpace(prompt)
}

func extractTaskSection(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}

	markers := []string{
		"\nTask:\n",
		"\nTask:\r\n",
		"Task:\n",
		"Task:\r\n",
	}
	for _, marker := range markers {
		if idx := strings.LastIndex(prompt, marker); idx >= 0 {
			return strings.TrimSpace(prompt[idx+len(marker):])
		}
	}
	return ""
}

func extractQuotedUserRequest(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return ""
	}

	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "user request") && !strings.HasPrefix(trimmed, "用户请求") {
		return ""
	}

	quotePairs := [][2]string{
		{`“`, `”`},
		{`"`, `"`},
		{`'`, `'`},
	}
	for _, pair := range quotePairs {
		start := strings.Index(trimmed, pair[0])
		if start < 0 {
			continue
		}
		rest := trimmed[start+len(pair[0]):]
		end := strings.Index(rest, pair[1])
		if end < 0 {
			continue
		}
		content := strings.TrimSpace(rest[:end])
		if content != "" {
			return content
		}
	}

	colonIdx := strings.IndexAny(trimmed, ":：")
	if colonIdx < 0 || colonIdx+1 >= len(trimmed) {
		return ""
	}
	content := strings.TrimSpace(trimmed[colonIdx+1:])
	if content == "" {
		return ""
	}
	if idx := strings.IndexAny(content, ".。!?！？\n"); idx > 0 {
		content = strings.TrimSpace(content[:idx])
	}
	return content
}
