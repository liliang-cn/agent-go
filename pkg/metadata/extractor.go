package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// MetadataExtractor provides enhanced metadata extraction using LLMs
type MetadataExtractor struct {
	llmProvider domain.LLMProvider
	cache       map[string]*domain.ExtractedMetadata
	cacheExpiry time.Duration
}

// NewMetadataExtractor creates a new metadata extractor
func NewMetadataExtractor(llmProvider domain.LLMProvider) *MetadataExtractor {
	return &MetadataExtractor{
		llmProvider: llmProvider,
		cache:       make(map[string]*domain.ExtractedMetadata),
		cacheExpiry: 1 * time.Hour,
	}
}

// ExtractEnhancedMetadata extracts comprehensive metadata from content
func (me *MetadataExtractor) ExtractEnhancedMetadata(ctx context.Context, content string, docType string) (*domain.ExtractedMetadata, error) {
	// Check cache first
	cacheKey := fmt.Sprintf("%s:%s", docType, hashContent(content))
	if cached, ok := me.cache[cacheKey]; ok {
		return cached, nil
	}

	// Build extraction prompt based on document type
	prompt := me.buildExtractionPrompt(content, docType)

	// Use structured generation for reliable output
	schema := MetadataSchema{
		Type: "object",
		Properties: map[string]SchemaProperty{
			"title":   {Type: "string", Description: "Document title"},
			"summary": {Type: "string", Description: "Brief summary (max 200 words)"},
			"keywords": {
				Type:        "array",
				Items:       &SchemaProperty{Type: "string"},
				Description: "Key terms and concepts",
			},
			"entities": {
				Type: "object",
				Properties: map[string]SchemaProperty{
					"people":        {Type: "array", Items: &SchemaProperty{Type: "string"}},
					"organizations": {Type: "array", Items: &SchemaProperty{Type: "string"}},
					"locations":     {Type: "array", Items: &SchemaProperty{Type: "string"}},
					"dates":         {Type: "array", Items: &SchemaProperty{Type: "string"}},
					"products":      {Type: "array", Items: &SchemaProperty{Type: "string"}},
				},
			},
			"topics": {
				Type:        "array",
				Items:       &SchemaProperty{Type: "string"},
				Description: "Main topics discussed",
			},
			"sentiment": {
				Type: "string",
				Enum: []string{"positive", "negative", "neutral", "mixed"},
			},
			"category": {Type: "string", Description: "Document category"},
			"language": {Type: "string", Description: "Primary language"},
			"technical_level": {
				Type: "string",
				Enum: []string{"beginner", "intermediate", "advanced", "expert"},
			},
		},
		Required: []string{"title", "summary", "keywords", "topics"},
	}

	// Generate structured metadata
	result, err := me.llmProvider.GenerateStructured(ctx, prompt, schema, &domain.GenerationOptions{
		MaxTokens:   1000,
		Temperature: 0.3, // Lower temperature for more consistent extraction
	})
	if err != nil {
		return nil, fmt.Errorf("failed to extract metadata: %w", err)
	}

	// Parse the structured result
	metadata, err := me.parseStructuredResult(result)
	if err != nil {
		// Fall back to regex-based extraction
		metadata = me.extractWithRegex(content)
	}

	// Enhance with document-type-specific extraction
	me.enhanceByDocumentType(metadata, content, docType)

	// Cache the result
	me.cache[cacheKey] = metadata

	return metadata, nil
}

// buildExtractionPrompt creates a prompt for metadata extraction
func (me *MetadataExtractor) buildExtractionPrompt(content string, docType string) string {
	// Truncate content if too long
	maxLength := 3000
	if len(content) > maxLength {
		content = content[:maxLength] + "..."
	}

	prompt := fmt.Sprintf(`Extract comprehensive metadata from the following %s document.

Focus on:
1. Main title and summary
2. Key concepts and keywords
3. Named entities (people, organizations, locations, dates)
4. Main topics and themes
5. Overall sentiment and tone
6. Technical level and target audience
7. Document category and classification

Document Content:
%s

Provide structured metadata in JSON format.`, docType, content)

	return prompt
}

// parseStructuredResult parses the LLM's structured response
func (me *MetadataExtractor) parseStructuredResult(result *domain.StructuredResult) (*domain.ExtractedMetadata, error) {
	// Convert structured result to metadata
	jsonData, err := json.Marshal(result.Data)
	if err != nil {
		return nil, err
	}

	var rawMetadata map[string]interface{}
	if err := json.Unmarshal(jsonData, &rawMetadata); err != nil {
		return nil, err
	}

	metadata := &domain.ExtractedMetadata{
		CustomMeta: make(map[string]interface{}),
	}
	metadata.CustomMeta["extracted_at"] = time.Now()

	// Extract fields from raw metadata
	if title, ok := rawMetadata["title"].(string); ok {
		metadata.CustomMeta["title"] = title
	}
	if summary, ok := rawMetadata["summary"].(string); ok {
		metadata.Summary = summary
	}
	if keywords, ok := rawMetadata["keywords"].([]interface{}); ok {
		for _, kw := range keywords {
			if kwStr, ok := kw.(string); ok {
				metadata.Keywords = append(metadata.Keywords, kwStr)
			}
		}
	}
	if topics, ok := rawMetadata["topics"].([]interface{}); ok {
		topicList := []string{}
		for _, topic := range topics {
			if topicStr, ok := topic.(string); ok {
				topicList = append(topicList, topicStr)
			}
		}
		metadata.CustomMeta["topics"] = topicList
	}

	// Extract entities
	if entities, ok := rawMetadata["entities"].(map[string]interface{}); ok {
		metadata.Entities = me.parseEntities(entities)
	}

	// Extract additional fields
	if sentiment, ok := rawMetadata["sentiment"].(string); ok {
		metadata.CustomMeta["sentiment"] = sentiment
	}
	if category, ok := rawMetadata["category"].(string); ok {
		metadata.CustomMeta["category"] = category
	}
	if lang, ok := rawMetadata["language"].(string); ok {
		metadata.CustomMeta["language"] = lang
	}

	return metadata, nil
}

// parseEntities extracts named entities from the entities map
func (me *MetadataExtractor) parseEntities(entities map[string]interface{}) map[string][]string {
	result := make(map[string][]string)

	entityTypes := map[string]string{
		"people":        "person",
		"organizations": "organization",
		"locations":     "location",
		"dates":         "date",
		"products":      "product",
	}

	for key, entityType := range entityTypes {
		if values, ok := entities[key].([]interface{}); ok {
			for _, value := range values {
				if valueStr, ok := value.(string); ok {
					result[entityType] = append(result[entityType], valueStr)
				}
			}
		}
	}

	return result
}

// extractWithRegex performs basic regex-based extraction as fallback
func (me *MetadataExtractor) extractWithRegex(content string) *domain.ExtractedMetadata {
	metadata := &domain.ExtractedMetadata{
		CustomMeta: make(map[string]interface{}),
	}
	metadata.CustomMeta["extracted_at"] = time.Now()

	// Extract potential title (first non-empty line or heading)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && len(line) < 200 {
			metadata.CustomMeta["title"] = line
			break
		}
	}

	// Initialize entities map if nil
	if metadata.Entities == nil {
		metadata.Entities = make(map[string][]string)
	}

	// Extract emails
	emailRegex := regexp.MustCompile(`[\w._%+-]+@[\w.-]+\.[A-Za-z]{2,}`)
	emails := emailRegex.FindAllString(content, -1)
	if len(emails) > 0 {
		metadata.Entities["email"] = emails
	}

	// Extract URLs
	urlRegex := regexp.MustCompile(`https?://[^\s]+`)
	urls := urlRegex.FindAllString(content, -1)
	if len(urls) > 0 {
		metadata.Entities["url"] = urls
	}

	// Extract dates
	dateRegex := regexp.MustCompile(`\b\d{1,2}[/-]\d{1,2}[/-]\d{2,4}\b|\b\d{4}[/-]\d{1,2}[/-]\d{1,2}\b`)
	dates := dateRegex.FindAllString(content, -1)
	if len(dates) > 0 {
		metadata.Entities["date"] = dates
	}

	// Simple keyword extraction (most frequent words)
	metadata.Keywords = me.extractKeywords(content, 10)

	// Generate basic summary (first few sentences)
	sentences := strings.Split(content, ".")
	summary := ""
	for i, sentence := range sentences {
		if i >= 3 {
			break
		}
		summary += strings.TrimSpace(sentence) + ". "
	}
	metadata.Summary = strings.TrimSpace(summary)

	return metadata
}

// extractKeywords extracts top N keywords from content
func (me *MetadataExtractor) extractKeywords(content string, n int) []string {
	// Simple word frequency analysis
	words := strings.Fields(strings.ToLower(content))
	wordCount := make(map[string]int)

	// Common stop words to exclude
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "with": true, "by": true, "from": true,
		"as": true, "is": true, "was": true, "are": true, "were": true,
		"been": true, "be": true, "have": true, "has": true, "had": true,
		"do": true, "does": true, "did": true, "will": true, "would": true,
		"could": true, "should": true, "may": true, "might": true,
	}

	for _, word := range words {
		// Clean word
		word = strings.Trim(word, ".,!?;:\"'()[]{}")
		if len(word) < 3 || stopWords[word] {
			continue
		}
		wordCount[word]++
	}

	// Sort by frequency
	type wordFreq struct {
		word  string
		count int
	}
	var freqs []wordFreq
	for word, count := range wordCount {
		freqs = append(freqs, wordFreq{word, count})
	}

	// Simple bubble sort
	for i := 0; i < len(freqs); i++ {
		for j := i + 1; j < len(freqs); j++ {
			if freqs[i].count < freqs[j].count {
				freqs[i], freqs[j] = freqs[j], freqs[i]
			}
		}
	}

	// Extract top N
	var keywords []string
	for i, wf := range freqs {
		if i >= n {
			break
		}
		keywords = append(keywords, wf.word)
	}

	return keywords
}

// enhanceByDocumentType adds document-type-specific metadata
func (me *MetadataExtractor) enhanceByDocumentType(metadata *domain.ExtractedMetadata, content string, docType string) {
	switch docType {
	case "code":
		me.enhanceCodeMetadata(metadata, content)
	case "legal":
		me.enhanceLegalMetadata(metadata, content)
	case "medical":
		me.enhanceMedicalMetadata(metadata, content)
	case "academic":
		me.enhanceAcademicMetadata(metadata, content)
	case "financial":
		me.enhanceFinancialMetadata(metadata, content)
	}
}

// enhanceCodeMetadata adds code-specific metadata
func (me *MetadataExtractor) enhanceCodeMetadata(metadata *domain.ExtractedMetadata, content string) {
	// Extract programming languages
	langPatterns := map[string]string{
		"python":     `\bimport\s+\w+|def\s+\w+|class\s+\w+`,
		"javascript": `\bfunction\s+\w+|const\s+\w+|var\s+\w+|=>\s*{`,
		"go":         `\bpackage\s+\w+|func\s+\w+|type\s+\w+`,
		"java":       `\bpublic\s+class|private\s+\w+|import\s+java`,
		"rust":       `\bfn\s+\w+|impl\s+\w+|use\s+\w+`,
	}

	for lang, pattern := range langPatterns {
		if matched, _ := regexp.MatchString(pattern, content); matched {
			metadata.Keywords = append(metadata.Keywords, lang)
			if metadata.CustomMeta["language"] == nil || metadata.CustomMeta["language"] == "" {
				metadata.CustomMeta["language"] = lang
			}
		}
	}

	// Initialize entities map if nil
	if metadata.Entities == nil {
		metadata.Entities = make(map[string][]string)
	}

	// Extract function/class names
	funcRegex := regexp.MustCompile(`(?:func|def|function|class)\s+(\w+)`)
	matches := funcRegex.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) > 1 {
			metadata.Entities["code_symbol"] = append(metadata.Entities["code_symbol"], match[1])
		}
	}
}

// enhanceLegalMetadata adds legal document-specific metadata
func (me *MetadataExtractor) enhanceLegalMetadata(metadata *domain.ExtractedMetadata, content string) {
	// Initialize entities map if nil
	if metadata.Entities == nil {
		metadata.Entities = make(map[string][]string)
	}

	// Extract case numbers
	caseRegex := regexp.MustCompile(`\b\d{2,4}-[A-Z]{2}-\d{3,6}\b`)
	cases := caseRegex.FindAllString(content, -1)
	if len(cases) > 0 {
		metadata.Entities["case_number"] = cases
	}

	// Extract legal terms
	legalTerms := []string{"plaintiff", "defendant", "court", "judgment", "statute", "regulation"}
	contentLower := strings.ToLower(content)
	for _, term := range legalTerms {
		if strings.Contains(contentLower, term) {
			metadata.Keywords = append(metadata.Keywords, term)
		}
	}
}

// enhanceMedicalMetadata adds medical document-specific metadata
func (me *MetadataExtractor) enhanceMedicalMetadata(metadata *domain.ExtractedMetadata, content string) {
	// Extract medical terms (simplified)
	medicalTerms := []string{"diagnosis", "treatment", "symptom", "medication", "patient", "clinical"}
	contentLower := strings.ToLower(content)
	for _, term := range medicalTerms {
		if strings.Contains(contentLower, term) {
			metadata.Keywords = append(metadata.Keywords, term)
		}
	}

	// Mark as containing sensitive information
	if metadata.CustomMeta["tags"] == nil {
		metadata.CustomMeta["tags"] = []string{}
	}
	if tags, ok := metadata.CustomMeta["tags"].([]string); ok {
		metadata.CustomMeta["tags"] = append(tags, "sensitive", "medical")
	}
}

// enhanceAcademicMetadata adds academic document-specific metadata
func (me *MetadataExtractor) enhanceAcademicMetadata(metadata *domain.ExtractedMetadata, content string) {
	// Initialize entities map if nil
	if metadata.Entities == nil {
		metadata.Entities = make(map[string][]string)
	}

	// Extract DOI
	doiRegex := regexp.MustCompile(`10\.\d{4,}\/[-._;()\/:A-Za-z0-9]+`)
	dois := doiRegex.FindAllString(content, -1)
	if len(dois) > 0 {
		metadata.Entities["doi"] = dois
	}

	// Extract citations (simplified)
	citationRegex := regexp.MustCompile(`\[\d+\]|\(\w+,\s*\d{4}\)`)
	citations := citationRegex.FindAllString(content, -1)
	if metadata.CustomMeta["tags"] == nil {
		metadata.CustomMeta["tags"] = []string{}
	}
	if tags, ok := metadata.CustomMeta["tags"].([]string); ok {
		metadata.CustomMeta["tags"] = append(tags, fmt.Sprintf("%d_citations", len(citations)))
	}

	// Academic keywords
	academicTerms := []string{"abstract", "methodology", "results", "conclusion", "hypothesis", "research"}
	contentLower := strings.ToLower(content)
	for _, term := range academicTerms {
		if strings.Contains(contentLower, term) {
			metadata.Keywords = append(metadata.Keywords, term)
		}
	}
}

// enhanceFinancialMetadata adds financial document-specific metadata
func (me *MetadataExtractor) enhanceFinancialMetadata(metadata *domain.ExtractedMetadata, content string) {
	// Initialize entities map if nil
	if metadata.Entities == nil {
		metadata.Entities = make(map[string][]string)
	}

	// Extract currency amounts
	currencyRegex := regexp.MustCompile(`\$[\d,]+\.?\d*|\d+\s*(?:USD|EUR|GBP|JPY)`)
	amounts := currencyRegex.FindAllString(content, -1)
	if len(amounts) > 0 {
		metadata.Entities["monetary"] = amounts
	}

	// Extract percentages
	percentRegex := regexp.MustCompile(`\d+\.?\d*%`)
	percents := percentRegex.FindAllString(content, -1)
	if len(percents) > 0 {
		metadata.Entities["percentage"] = percents
	}

	// Financial keywords
	finTerms := []string{"revenue", "profit", "loss", "investment", "asset", "liability", "equity"}
	contentLower := strings.ToLower(content)
	for _, term := range finTerms {
		if strings.Contains(contentLower, term) {
			metadata.Keywords = append(metadata.Keywords, term)
		}
	}
}

// hashContent creates a simple hash for cache keys
func hashContent(content string) string {
	// Simple hash for demo (in production, use crypto/sha256)
	hash := 0
	for _, r := range content {
		hash = hash*31 + int(r)
	}
	return fmt.Sprintf("%x", hash)
}

// MetadataSchema defines the JSON schema for structured metadata extraction
type MetadataSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]SchemaProperty `json:"properties"`
	Required   []string                  `json:"required"`
}

// SchemaProperty defines a property in the schema
type SchemaProperty struct {
	Type        string                    `json:"type"`
	Description string                    `json:"description,omitempty"`
	Items       *SchemaProperty           `json:"items,omitempty"`
	Properties  map[string]SchemaProperty `json:"properties,omitempty"`
	Enum        []string                  `json:"enum,omitempty"`
}
