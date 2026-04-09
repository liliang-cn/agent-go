package domain

import "strings"

// IsFastModelName heuristically classifies latency-optimized model names.
// This is intentionally conservative and is used for runtime hints/UI metadata,
// not correctness-critical routing.
func IsFastModelName(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}

	fastHints := []string{
		"mini",
		"nano",
		"haiku",
		"flash",
		"instant",
		"turbo",
		"fast",
	}

	for _, hint := range fastHints {
		if strings.Contains(model, hint) {
			return true
		}
	}
	return false
}
