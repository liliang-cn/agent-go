package memory

import (
	"regexp"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// NoiseFilterConfig holds configuration for noise filtering
type NoiseFilterConfig struct {
	Enabled          bool // Enable noise filtering
	MinContentLength int  // Minimum content length to keep

	// Filter categories
	FilterRefusals   bool // Filter refusal responses
	FilterMeta       bool // Filter meta questions
	FilterDuplicates bool // Filter near-duplicates
}

// DefaultNoiseFilterConfig returns default noise filter configuration
func DefaultNoiseFilterConfig() *NoiseFilterConfig {
	return &NoiseFilterConfig{
		Enabled:          true,
		MinContentLength: 5,
		FilterRefusals:   true,
		FilterMeta:       true,
		FilterDuplicates: true,
	}
}

// NoiseFilter filters out low-quality memories
type NoiseFilter struct {
	config          *NoiseFilterConfig
	refusalPatterns []*regexp.Regexp
	metaPatterns    []*regexp.Regexp
	genericPatterns []*regexp.Regexp
}

// NewNoiseFilter creates a new noise filter
func NewNoiseFilter(config *NoiseFilterConfig) *NoiseFilter {
	if config == nil {
		config = DefaultNoiseFilterConfig()
	}

	f := &NoiseFilter{
		config: config,
	}

	// Initialize refusal patterns
	f.refusalPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)i\s+(can'?t|cannot|am\s+unable\s+to|won'?t)\s+(help|assist|do|provide)`),
		regexp.MustCompile(`(?i)i'?m\s+(sorry|afraid)\s+(but\s+)?i\s+(can'?t|cannot)`),
		regexp.MustCompile(`(?i)i\s+don'?t\s+(have|know)\s+(access|information|knowledge)`),
		regexp.MustCompile(`(?i)not\s+(able|allowed|permitted)\s+to`),
		regexp.MustCompile(`(?i)unable\s+to\s+(complete|fulfill|process)`),
		regexp.MustCompile(`(?i)against\s+my\s+(policy|guidelines|rules)`),
		regexp.MustCompile(`(?i)i\s+must\s+(decline|refuse)`),
		regexp.MustCompile(`(?i)我不能|无法|不允许`),
	}

	// Initialize meta patterns
	f.metaPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)who\s+(are|r)\s+you`),
		regexp.MustCompile(`(?i)what\s+(are|r)\s+you`),
		regexp.MustCompile(`(?i)what\s+is\s+your\s+(name|purpose|role)`),
		regexp.MustCompile(`(?i)tell\s+me\s+about\s+yourself`),
		regexp.MustCompile(`(?i)are\s+you\s+(a\s+)?(human|ai|robot|bot|assistant)`),
		regexp.MustCompile(`(?i)你是谁|你是什么`),
		regexp.MustCompile(`(?i)how\s+do\s+you\s+work`),
		regexp.MustCompile(`(?i)what\s+can\s+you\s+do`),
	}

	// Initialize generic/low-value patterns
	f.genericPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(ok|okay|sure|yes|no|maybe|alright|fine)\s*[!.]?`),
		regexp.MustCompile(`(?i)^(i\s+see|i\s+understand|got\s+it)\s*[!.]?`),
		regexp.MustCompile(`(?i)^(thanks|thank\s+you|thx)\s*[!.]?`),
		regexp.MustCompile(`(?i)^.{0,4}$`), // Very short content
	}

	return f
}

// Filter filters out low-quality memories
func (f *NoiseFilter) Filter(memories []*domain.MemoryWithScore) []*domain.MemoryWithScore {
	if !f.config.Enabled || len(memories) == 0 {
		return memories
	}

	filtered := make([]*domain.MemoryWithScore, 0, len(memories))
	seen := make(map[string]bool)

	for _, m := range memories {
		if f.shouldKeep(m, seen) {
			filtered = append(filtered, m)
		}
	}

	return filtered
}

// shouldKeep determines if a memory should be kept
func (f *NoiseFilter) shouldKeep(memory *domain.MemoryWithScore, seen map[string]bool) bool {
	if memory == nil || memory.Memory == nil {
		return false
	}

	content := strings.TrimSpace(memory.Content)

	// Check minimum length
	if len(content) < f.config.MinContentLength {
		return false
	}

	// Check refusal patterns
	if f.config.FilterRefusals && f.isRefusal(content) {
		return false
	}

	// Check meta patterns
	if f.config.FilterMeta && f.isMeta(content) {
		return false
	}

	// Check generic patterns
	if f.isGeneric(content) {
		return false
	}

	// Check duplicates (case-insensitive)
	if f.config.FilterDuplicates {
		contentLower := strings.ToLower(content)
		// Normalize whitespace for duplicate detection
		normalized := strings.Join(strings.Fields(contentLower), " ")
		if seen[normalized] {
			return false
		}
		seen[normalized] = true
	}

	return true
}

// isRefusal checks if content is a refusal response
func (f *NoiseFilter) isRefusal(content string) bool {
	for _, p := range f.refusalPatterns {
		if p.MatchString(content) {
			return true
		}
	}
	return false
}

// isMeta checks if content is a meta question/response
func (f *NoiseFilter) isMeta(content string) bool {
	for _, p := range f.metaPatterns {
		if p.MatchString(content) {
			return true
		}
	}
	return false
}

// isGeneric checks if content is generic/low-value
func (f *NoiseFilter) isGeneric(content string) bool {
	for _, p := range f.genericPatterns {
		if p.MatchString(content) {
			return true
		}
	}
	return false
}

// IsNoisy checks if a single content string is noisy
func (f *NoiseFilter) IsNoisy(content string) bool {
	if !f.config.Enabled {
		return false
	}

	content = strings.TrimSpace(content)

	if len(content) < f.config.MinContentLength {
		return true
	}

	if f.config.FilterRefusals && f.isRefusal(content) {
		return true
	}

	if f.config.FilterMeta && f.isMeta(content) {
		return true
	}

	if f.isGeneric(content) {
		return true
	}

	return false
}

// FilterContent filters a single content string
// Returns true if the content should be kept
func (f *NoiseFilter) FilterContent(content string) bool {
	return !f.IsNoisy(content)
}
