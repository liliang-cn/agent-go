package graphrag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// EntityExtractor extracts entities and relations from text using LLM
type EntityExtractor struct {
	generator   domain.Generator
	entityTypes []string
}

// ExtractedEntity represents an entity extracted from text
type ExtractedEntity struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	Type           string                 `json:"type"`
	Description    string                 `json:"description"`
	Properties     map[string]interface{} `json:"properties,omitempty"`
	Vector         []float64              `json:"vector,omitempty"`
	Sources        []string               `json:"sources"`
	SourceChunkIDs []string               `json:"source_chunk_ids"`
	SourceChunkID  string                 `json:"-"`
	DocumentID     string                 `json:"-"`
	Confidence     float64                `json:"confidence"`
}

// ExtractedRelation represents a relation between entities
type ExtractedRelation struct {
	ID            string                 `json:"id"`
	Source        string                 `json:"source"`
	Target        string                 `json:"target"`
	Type          string                 `json:"type"`
	Weight        float64                `json:"weight"`
	Properties    map[string]interface{} `json:"properties,omitempty"`
	Sources       []string               `json:"sources"`
	SourceChunkID string                 `json:"-"`
}

// entityExtractionResult is the structured output from LLM
type entityExtractionResult struct {
	Entities  []entityJSON   `json:"entities"`
	Relations []relationJSON `json:"relations"`
}

type entityJSON struct {
	Name        string                 `json:"name"`
	Type        string                 `json:"type"`
	Description string                 `json:"description"`
	Properties  map[string]interface{} `json:"properties,omitempty"`
	Confidence  float64                `json:"confidence,omitempty"`
}

type relationJSON struct {
	Source     string                 `json:"source"`
	Target     string                 `json:"target"`
	Type       string                 `json:"type"`
	Weight     float64                `json:"weight,omitempty"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// NewEntityExtractor creates a new entity extractor
func NewEntityExtractor(generator domain.Generator, entityTypes []string) *EntityExtractor {
	if len(entityTypes) == 0 {
		entityTypes = []string{"person", "organization", "location", "concept", "event", "product"}
	}

	return &EntityExtractor{
		generator:   generator,
		entityTypes: entityTypes,
	}
}

// Extract extracts entities and relations from text
func (e *EntityExtractor) Extract(ctx context.Context, text string) ([]ExtractedEntity, []ExtractedRelation, error) {
	if e.generator == nil {
		return nil, nil, fmt.Errorf("generator not available")
	}

	// Build extraction prompt
	prompt := e.buildExtractionPrompt(text)

	// Generate with adequate tokens for JSON output
	opts := &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   2000,
	}

	// Use regular Generate - content will have JSON if model outputs it there
	result, err := e.generator.Generate(ctx, prompt, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("generation failed: %w", err)
	}

	// Clean thinking tags from result (in case it contains reasoning content)
	result = cleanThinkingTags(result)

	// If result is still empty after cleaning, try to get reasoning content
	if result == "" {
		log.Printf("[GraphRAG] Warning: LLM returned empty content after cleaning")
	}

	// Try to parse JSON from the response
	entities, relations, err := e.parseJSONResponse(result)
	if err != nil {
		log.Printf("[GraphRAG] Failed to parse JSON from LLM response: %v", err)
		// Return empty results instead of failing completely
		return []ExtractedEntity{}, []ExtractedRelation{}, nil
	}

	// Convert to domain entities
	domainEntities := make([]ExtractedEntity, 0, len(entities))
	for _, ent := range entities {
		if len(ent.Name) < 2 {
			continue
		}

		entity := ExtractedEntity{
			ID:          GenerateEntityID(ent.Name, ent.Type),
			Name:        ent.Name,
			Type:        ent.Type,
			Description: ent.Description,
			Properties:  ent.Properties,
			Confidence:  ent.Confidence,
			Sources:     []string{text[:min(100, len(text))]},
		}

		// Validate entity type
		if !e.isValidEntityType(entity.Type) {
			entity.Type = "concept" // Default to concept
		}

		domainEntities = append(domainEntities, entity)
	}

	// Convert to domain relations
	domainRelations := make([]ExtractedRelation, 0, len(relations))
	for _, rel := range relations {
		if len(rel.Source) < 2 || len(rel.Target) < 2 {
			continue
		}

		weight := rel.Weight
		if weight <= 0 {
			weight = 1.0
		}

		relation := ExtractedRelation{
			ID:         fmt.Sprintf("%s->%s->%s", rel.Source, rel.Type, rel.Target),
			Source:     rel.Source,
			Target:     rel.Target,
			Type:       rel.Type,
			Weight:     weight,
			Properties: rel.Properties,
			Sources:    []string{text[:min(100, len(text))]},
		}

		domainRelations = append(domainRelations, relation)
	}

	return domainEntities, domainRelations, nil
}

// parseJSONResponse tries to extract JSON from the LLM response
func (e *EntityExtractor) parseJSONResponse(response string) ([]entityJSON, []relationJSON, error) {
	// For Qwen/DeepSeek models, the thinking/reasoning might contain the JSON at the end
	// We need to look for JSON patterns in the response

	// Try multiple extraction strategies
	jsonStr := ""

	// Strategy 1: Direct JSON extraction
	jsonStr = extractJSON(response)

	// Strategy 2: Look for JSON after ``` markers
	if jsonStr == "" {
		if idx := strings.Index(response, "```json"); idx != -1 {
			jsonStr = response[idx+7:]
			if endIdx := strings.Index(jsonStr, "```"); endIdx != -1 {
				jsonStr = jsonStr[:endIdx]
			}
		}
	}

	// Strategy 3: Look for array or object patterns at the end
	if jsonStr == "" {
		lines := strings.Split(response, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "{") {
				jsonStr = strings.Join(lines[i:], "\n")
				break
			}
		}
	}

	if jsonStr == "" {
		return nil, nil, fmt.Errorf("no JSON found in response")
	}

	var result entityExtractionResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		// Try to fix common JSON issues
		fixed := fixJSON(jsonStr)
		if err2 := json.Unmarshal([]byte(fixed), &result); err2 != nil {
			return nil, nil, fmt.Errorf("failed to parse extraction result: %w (original: %v, fixed: %v)", err, jsonStr, fixed)
		}
	}

	return result.Entities, result.Relations, nil
}

// extractJSON extracts JSON from a response that may contain extra text
func extractJSON(text string) string {
	// Strategy 1: Find the first { and last } - take everything between them
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")

	if start != -1 && end != -1 && end > start {
		return text[start : end+1]
	}

	// Strategy 2: Try array format
	start = strings.Index(text, "[")
	end = strings.LastIndex(text, "]")
	if start != -1 && end != -1 && end > start {
		return text[start : end+1]
	}

	// Strategy 3: Try to find lines that start with { and end with }
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		// Skip lines that are clearly not JSON (like "Let me create...")
		if len(line) > 10 && !strings.HasPrefix(line, "Let") && !strings.HasPrefix(line, "Here") && !strings.HasPrefix(line, "The") {
			if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
				return line
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				return line
			}
		}
	}

	// Strategy 4: Find JSON in markdown code blocks
	if idx := strings.Index(text, "```json"); idx != -1 {
		rest := text[idx+7:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			return strings.TrimSpace(rest[:endIdx])
		}
	}

	return ""
}

// buildExtractionPrompt creates the prompt for entity extraction
func (e *EntityExtractor) buildExtractionPrompt(text string) string {
	// Use a template approach - ask model to fill in the JSON template
	// This works better with Qwen/Ollama models that use thinking
	return fmt.Sprintf(`Extract entities and relations from the text below. Output ONLY valid JSON.

Text: %s

Output JSON:
{"entities": [{"name":"Apple Inc.","type":"organization","description":"A company"}, {"name":"Steve Jobs","type":"person","description":"A founder"}], "relations": [{"source":"Apple Inc.","target":"Steve Jobs","type":"founded_by"}]}`, text)
}

// isValidEntityType checks if the entity type is valid
func (e *EntityExtractor) isValidEntityType(entityType string) bool {
	lowerType := strings.ToLower(entityType)
	for _, t := range e.entityTypes {
		if strings.ToLower(t) == lowerType {
			return true
		}
	}
	return false
}

// fixJSON attempts to fix common JSON parsing issues
func fixJSON(jsonStr string) string {
	// Remove common issues
	result := strings.TrimSpace(jsonStr)

	// If the result is wrapped in markdown code blocks, remove them
	if strings.HasPrefix(result, "```") {
		lines := strings.Split(result, "\n")
		if len(lines) >= 2 {
			// Remove first and last line (code block markers)
			result = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	// Try to find the first { and last }
	start := strings.Index(result, "{")
	end := strings.LastIndex(result, "}")

	if start != -1 && end != -1 && end > start {
		result = result[start : end+1]
	}

	return result
}

// cleanThinkingTags removes thinking tags from LLM response
func cleanThinkingTags(text string) string {
	// Remove <thinking>...</thinking> blocks
	for {
		start := strings.Index(text, "<thinking>")
		if start == -1 {
			break
		}
		end := strings.Index(text, "</thinking>")
		if end == -1 {
			break
		}
		text = text[:start] + text[end+len("</thinking>"):]
	}

	// Remove 【...】 analysis blocks
	for {
		start := strings.Index(text, "【")
		if start == -1 {
			break
		}
		end := strings.Index(text, "】")
		if end == -1 {
			break
		}
		text = text[:start] + text[end+len("】"):]
	}

	return strings.TrimSpace(text)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
