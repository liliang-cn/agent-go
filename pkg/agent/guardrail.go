package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// GuardrailKind defines when a guardrail is applied
type GuardrailKind string

const (
	GuardrailKindInput  GuardrailKind = "input"  // Applied before tool execution
	GuardrailKindOutput GuardrailKind = "output" // Applied after tool execution
	GuardrailKindBoth   GuardrailKind = "both"   // Applied to both input and output
)

// GuardrailResult represents the result of a guardrail check
type GuardrailResult struct {
	Passed    bool                   `json:"passed"`
	Reason    string                 `json:"reason,omitempty"`
	Modified  bool                   `json:"modified"`
	Content   string                 `json:"content,omitempty"` // Modified content if any
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Duration  time.Duration          `json:"duration"`
	CheckTime time.Time              `json:"check_time"`
}

// GuardrailFunc is a function that performs a guardrail check
type GuardrailFunc func(ctx context.Context, content string, kind GuardrailKind) (*GuardrailResult, error)

// Guardrail represents a safety/validation check
// Inspired by OpenAI Agents SDK guardrail pattern
type Guardrail struct {
	id          string
	name        string
	kind        GuardrailKind
	fn          GuardrailFunc
	enabled     bool
	priority    int // Higher priority runs first
	metadata    map[string]interface{}
	description string
}

// GuardrailOption configures a Guardrail
type GuardrailOption func(*Guardrail)

// WithGuardrailEnabled sets whether the guardrail is enabled
func WithGuardrailEnabled(enabled bool) GuardrailOption {
	return func(g *Guardrail) {
		g.enabled = enabled
	}
}

// WithGuardrailPriority sets the guardrail priority
func WithGuardrailPriority(priority int) GuardrailOption {
	return func(g *Guardrail) {
		g.priority = priority
	}
}

// WithGuardrailMetadata adds metadata to the guardrail
func WithGuardrailMetadata(key string, value interface{}) GuardrailOption {
	return func(g *Guardrail) {
		if g.metadata == nil {
			g.metadata = make(map[string]interface{})
		}
		g.metadata[key] = value
	}
}

// WithGuardrailDescription sets the guardrail description
func WithGuardrailDescription(desc string) GuardrailOption {
	return func(g *Guardrail) {
		g.description = desc
	}
}

// NewGuardrail creates a new guardrail
func NewGuardrail(name string, kind GuardrailKind, fn GuardrailFunc, opts ...GuardrailOption) *Guardrail {
	g := &Guardrail{
		id:       name, // Use name as ID for simplicity
		name:     name,
		kind:     kind,
		fn:       fn,
		enabled:  true,
		priority: 0,
		metadata: make(map[string]interface{}),
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// ID returns the guardrail ID
func (g *Guardrail) ID() string {
	return g.id
}

// Name returns the guardrail name
func (g *Guardrail) Name() string {
	return g.name
}

// Kind returns the guardrail kind
func (g *Guardrail) Kind() GuardrailKind {
	return g.kind
}

// IsEnabled returns whether the guardrail is enabled
func (g *Guardrail) IsEnabled() bool {
	return g.enabled
}

// Priority returns the guardrail priority
func (g *Guardrail) Priority() int {
	return g.priority
}

// Check runs the guardrail check on the given content
func (g *Guardrail) Check(ctx context.Context, content string, kind GuardrailKind) (*GuardrailResult, error) {
	if !g.enabled {
		return &GuardrailResult{Passed: true}, nil
	}

	// If guardrail kind doesn't match, skip
	if g.kind != GuardrailKindBoth && g.kind != kind {
		return &GuardrailResult{Passed: true}, nil
	}

	start := time.Now()
	result, err := g.fn(ctx, content, kind)
	if err != nil {
		return nil, fmt.Errorf("guardrail %s check failed: %w", g.name, err)
	}
	result.Duration = time.Since(start)
	result.CheckTime = start

	return result, nil
}

// GuardrailChain runs multiple guardrails in sequence
type GuardrailChain struct {
	guardrails      []*Guardrail
	failFast        bool
	stopOnFirstFail bool
}

// NewGuardrailChain creates a new guardrail chain
func NewGuardrailChain(guardrails ...*Guardrail) *GuardrailChain {
	return &GuardrailChain{
		guardrails:      guardrails,
		failFast:        true, // Stop on first failure by default
		stopOnFirstFail: false,
	}
}

// WithFailFast sets whether to stop on first failure
func (gc *GuardrailChain) WithFailFast(failFast bool) *GuardrailChain {
	gc.failFast = failFast
	return gc
}

// WithStopOnFirstFail sets whether to stop after first failed check
func (gc *GuardrailChain) WithStopOnFirstFail(stop bool) *GuardrailChain {
	gc.stopOnFirstFail = stop
	return gc
}

// Add adds a guardrail to the chain
func (gc *GuardrailChain) Add(guardrail *Guardrail) *GuardrailChain {
	gc.guardrails = append(gc.guardrails, guardrail)
	return gc
}

// CheckAll runs all applicable guardrails in the chain
func (gc *GuardrailChain) CheckAll(ctx context.Context, content string, kind GuardrailKind) (*GuardrailChainResult, error) {
	result := &GuardrailChainResult{
		Results: make([]*GuardrailResult, 0, len(gc.guardrails)),
		Passed:  true,
	}

	// Sort by priority (highest first)
	sorted := make([]*Guardrail, len(gc.guardrails))
	copy(sorted, gc.guardrails)
	// Simple bubble sort by priority (descending)
	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			if sorted[j].priority < sorted[j+1].priority {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}

	currentContent := content

	for _, guardrail := range sorted {
		checkResult, err := guardrail.Check(ctx, currentContent, kind)
		if err != nil {
			return nil, err
		}

		result.Results = append(result.Results, checkResult)

		if !checkResult.Passed {
			result.Passed = false
			result.FailedGuardrails = append(result.FailedGuardrails, guardrail.name)

			if gc.failFast {
				result.FailedAt = guardrail.name
				result.Reason = checkResult.Reason
				return result, nil
			}
		}

		// Update content if modified
		if checkResult.Modified && checkResult.Content != "" {
			currentContent = checkResult.Content
			result.Modified = true
			result.FinalContent = currentContent
		}
	}

	return result, nil
}

// GuardrailChainResult represents the result of running a guardrail chain
type GuardrailChainResult struct {
	Results          []*GuardrailResult `json:"results"`
	Passed           bool               `json:"passed"`
	Modified         bool               `json:"modified"`
	FinalContent     string             `json:"final_content,omitempty"`
	FailedGuardrails []string           `json:"failed_guardrails,omitempty"`
	FailedAt         string             `json:"failed_at,omitempty"`
	Reason           string             `json:"reason,omitempty"`
}

// Guardrails returns the service's guardrail chain, or nil when none are
// registered. The runtime checks for nil to skip the guardrail path entirely
// (zero overhead when unused).
func (s *Service) Guardrails() *GuardrailChain {
	if s == nil {
		return nil
	}
	s.guardrailsMu.RLock()
	defer s.guardrailsMu.RUnlock()
	return s.guardrails
}

// RegisterGuardrail adds one or more guardrails to the service, creating the
// chain lazily on first use. The chain threads redacted content through each
// applicable guardrail (matched by kind) at the runtime seams. No-op for nil
// guardrails. Safe to call after Build().
func (s *Service) RegisterGuardrail(gs ...*Guardrail) {
	if s == nil {
		return
	}
	s.guardrailsMu.Lock()
	defer s.guardrailsMu.Unlock()
	for _, g := range gs {
		if g == nil {
			continue
		}
		if s.guardrails == nil {
			s.guardrails = NewGuardrailChain()
			// Redaction chains must run every applicable guardrail so each
			// pass can thread its modified content into the next; don't bail
			// on the first non-pass (that's for hard-block chains only).
			s.guardrails.WithFailFast(false)
		}
		s.guardrails.Add(g)
	}
}

// InputGuardrail creates a guardrail for input validation
func InputGuardrail(name string, fn func(ctx context.Context, content string) (*GuardrailResult, error), opts ...GuardrailOption) *Guardrail {
	return NewGuardrail(name, GuardrailKindInput, func(ctx context.Context, content string, kind GuardrailKind) (*GuardrailResult, error) {
		return fn(ctx, content)
	}, opts...)
}

// OutputGuardrail creates a guardrail for output validation
func OutputGuardrail(name string, fn func(ctx context.Context, content string) (*GuardrailResult, error), opts ...GuardrailOption) *Guardrail {
	return NewGuardrail(name, GuardrailKindOutput, func(ctx context.Context, content string, kind GuardrailKind) (*GuardrailResult, error) {
		return fn(ctx, content)
	}, opts...)
}

// Common guardrail builders

// MaxLengthGuardrail creates a guardrail that checks content length
func MaxLengthGuardrail(maxLength int) *Guardrail {
	return InputGuardrail(
		fmt.Sprintf("max_length_%d", maxLength),
		func(ctx context.Context, content string) (*GuardrailResult, error) {
			if len(content) > maxLength {
				return &GuardrailResult{
					Passed: false,
					Reason: fmt.Sprintf("content exceeds maximum length of %d characters (got %d)", maxLength, len(content)),
				}, nil
			}
			return &GuardrailResult{Passed: true}, nil
		},
		WithGuardrailDescription("Ensures content does not exceed maximum length"),
	)
}

// MinLengthGuardrail creates a guardrail that checks minimum content length
func MinLengthGuardrail(minLength int) *Guardrail {
	return InputGuardrail(
		fmt.Sprintf("min_length_%d", minLength),
		func(ctx context.Context, content string) (*GuardrailResult, error) {
			if len(content) < minLength {
				return &GuardrailResult{
					Passed: false,
					Reason: fmt.Sprintf("content is too short (minimum %d characters, got %d)", minLength, len(content)),
				}, nil
			}
			return &GuardrailResult{Passed: true}, nil
		},
		WithGuardrailDescription("Ensures content meets minimum length"),
	)
}

// ForbiddenContentGuardrail creates a guardrail that checks for forbidden content
func ForbiddenContentGuardrail(name string, forbiddenWords []string, caseSensitive bool) *Guardrail {
	return NewGuardrail(
		fmt.Sprintf("forbidden_%s", name),
		GuardrailKindBoth,
		func(ctx context.Context, content string, kind GuardrailKind) (*GuardrailResult, error) {
			checkContent := content
			words := forbiddenWords
			if !caseSensitive {
				checkContent = strings.ToLower(content)
				words = make([]string, len(forbiddenWords))
				for i, w := range forbiddenWords {
					words[i] = strings.ToLower(w)
				}
			}

			for _, word := range words {
				if strings.Contains(checkContent, word) {
					return &GuardrailResult{
						Passed: false,
						Reason: fmt.Sprintf("contains forbidden content: %s", word),
					}, nil
				}
			}
			return &GuardrailResult{Passed: true}, nil
		},
		WithGuardrailDescription("Checks for forbidden content"),
	)
}

// RequiredKeywordsGuardrail creates a guardrail that requires certain keywords
func RequiredKeywordsGuardrail(name string, keywords []string, caseSensitive bool) *Guardrail {
	return InputGuardrail(
		fmt.Sprintf("required_%s", name),
		func(ctx context.Context, content string) (*GuardrailResult, error) {
			checkContent := content
			words := keywords
			if !caseSensitive {
				checkContent = strings.ToLower(content)
				words = make([]string, len(keywords))
				for i, w := range keywords {
					words[i] = strings.ToLower(w)
				}
			}

			var found []string
			for _, word := range words {
				if strings.Contains(checkContent, word) {
					found = append(found, word)
				}
			}

			if len(found) == 0 {
				return &GuardrailResult{
					Passed: false,
					Reason: fmt.Sprintf("missing required keywords (need one of: %s)", strings.Join(keywords, ", ")),
				}, nil
			}
			return &GuardrailResult{Passed: true}, nil
		},
		WithGuardrailDescription("Ensures required keywords are present"),
	)
}

// RegexGuardrail creates a guardrail that validates content against a regex pattern
func RegexGuardrail(name string, pattern string, mustMatch bool) *Guardrail {
	return NewGuardrail(
		fmt.Sprintf("regex_%s", name),
		GuardrailKindBoth,
		func(ctx context.Context, content string, kind GuardrailKind) (*GuardrailResult, error) {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid regex pattern: %w", err)
			}

			matches := re.MatchString(content)
			if mustMatch && !matches {
				return &GuardrailResult{
					Passed: false,
					Reason: fmt.Sprintf("content does not match required pattern: %s", pattern),
				}, nil
			}
			if !mustMatch && matches {
				return &GuardrailResult{
					Passed: false,
					Reason: fmt.Sprintf("content matches forbidden pattern: %s", pattern),
				}, nil
			}
			return &GuardrailResult{Passed: true}, nil
		},
		WithGuardrailDescription("Validates content against regex pattern"),
	)
}

// SanitizeGuardrail creates a guardrail that modifies content to remove unwanted content
func SanitizeGuardrail(name string, replacements map[string]string) *Guardrail {
	return NewGuardrail(
		fmt.Sprintf("sanitize_%s", name),
		GuardrailKindBoth,
		func(ctx context.Context, content string, kind GuardrailKind) (*GuardrailResult, error) {
			result := content
			modified := false

			for old, new := range replacements {
				if strings.Contains(result, old) {
					result = strings.ReplaceAll(result, old, new)
					modified = true
				}
			}

			return &GuardrailResult{
				Passed:   true,
				Modified: modified,
				Content:  result,
			}, nil
		},
		WithGuardrailDescription("Sanitizes content by replacing unwanted content"),
	)
}
