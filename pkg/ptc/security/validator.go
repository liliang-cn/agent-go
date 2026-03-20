package security

import (
	"regexp"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

// Validator validates code for security issues
type Validator struct {
	config *ptc.SecurityConfig

	mu       sync.RWMutex
	patterns []*regexp.Regexp
}

// NewValidator creates a new code validator
func NewValidator(config *ptc.SecurityConfig) (*Validator, error) {
	v := &Validator{
		config:   config,
		patterns: make([]*regexp.Regexp, 0),
	}

	// Compile forbidden patterns
	for _, pattern := range config.ForbiddenPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue // Skip invalid patterns
		}
		v.patterns = append(v.patterns, re)
	}

	return v, nil
}

// Validate validates code for security issues
func (v *Validator) Validate(code string) error {
	if !v.config.ValidateCode {
		return nil
	}

	// Check for forbidden patterns
	for _, pattern := range v.patterns {
		if pattern.MatchString(code) {
			return ptc.NewExecutionError(ptc.ErrForbiddenPattern, "validate").
				WithSource(pattern.String())
		}
	}

	// Check for common dangerous patterns
	if err := v.checkDangerousPatterns(code); err != nil {
		return err
	}

	return nil
}

// ValidateToolAccess validates if a tool is accessible
func (v *Validator) ValidateToolAccess(toolName string) error {
	// Check blocked list
	for _, blocked := range v.config.BlockedTools {
		if blocked == toolName || blocked == "*" {
			return ptc.ErrToolNotAllowed
		}
	}

	// If allowed list is empty, all non-blocked tools are allowed
	if len(v.config.AllowedTools) == 0 {
		return nil
	}

	// Check allowed list
	for _, allowed := range v.config.AllowedTools {
		if allowed == toolName || allowed == "*" {
			return nil
		}
	}

	return ptc.ErrToolNotAllowed
}

// checkDangerousPatterns checks for common dangerous code patterns
func (v *Validator) checkDangerousPatterns(code string) error {
	// These are additional checks beyond the configurable patterns
	dangerousPatterns := []struct {
		pattern *regexp.Regexp
		reason  string
	}{
		{regexp.MustCompile(`(?i)\beval\s*\(`), "eval() is not allowed"},
		{regexp.MustCompile(`(?i)\bFunction\s*\(`), "Function constructor is not allowed"},
		{regexp.MustCompile(`(?i)\bimport\s*\(`), "dynamic import is not allowed"},
		{regexp.MustCompile(`(?i)\brequire\s*\(\s*['\"]`), "require is not allowed"},
		{regexp.MustCompile(`(?i)\bprocess\s*\.`), "process object is not accessible"},
		{regexp.MustCompile(`(?i)\bglobal\s*\.`), "global object is not accessible"},
		{regexp.MustCompile(`(?i)__proto__`), "__proto__ manipulation is not allowed"},
		{regexp.MustCompile(`(?i)\bconstructor\s*\[`), "constructor access is not allowed"},
		{regexp.MustCompile(`(?i)\bprototype\s*\[`), "prototype manipulation is not allowed"},
	}

	for _, dp := range dangerousPatterns {
		if dp.pattern.MatchString(code) {
			return ptc.NewExecutionError(ptc.ErrForbiddenPattern, "validate").
				WithSource(dp.reason)
		}
	}

	return nil
}

// AddPattern adds a forbidden pattern
func (v *Validator) AddPattern(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.patterns = append(v.patterns, re)
	return nil
}

// RemovePattern removes a forbidden pattern
func (v *Validator) RemovePattern(pattern string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	for i, p := range v.patterns {
		if p.String() == pattern {
			v.patterns = append(v.patterns[:i], v.patterns[i+1:]...)
			break
		}
	}
}

// ListPatterns lists all forbidden patterns
func (v *Validator) ListPatterns() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()

	patterns := make([]string, len(v.patterns))
	for i, p := range v.patterns {
		patterns[i] = p.String()
	}
	return patterns
}
