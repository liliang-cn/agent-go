package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

type EntityExtractor struct {
	generator     domain.Generator
	promptManager *prompt.Manager
}

func NewEntityExtractor(generator domain.Generator) *EntityExtractor {
	return &EntityExtractor{
		generator:     generator,
		promptManager: prompt.NewManager(),
	}
}

func (e *EntityExtractor) SetPromptManager(m *prompt.Manager) {
	e.promptManager = m
}

type ExtractedGraphData struct {
	Entities []struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"entities"`
	Relationships []struct {
		Source      string `json:"source"`
		Target      string `json:"target"`
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"relationships"`
}

func (e *EntityExtractor) Extract(ctx context.Context, text string) (*ExtractedGraphData, error) {
	promptData := map[string]interface{}{
		"Text": text,
	}

	rendered, err := e.promptManager.Render(prompt.RAGGraphExtraction, promptData)
	if err != nil {
		rendered = fmt.Sprintf("Extract entities and relationships from: %s", text)
	}

	// Use low temperature for deterministic output
	opts := &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   4000,
	}

	response, err := e.generator.Generate(ctx, rendered, opts)
	if err != nil {
		return nil, fmt.Errorf("extraction generation failed: %w", err)
	}

	// Clean up response (remove markdown code blocks if any)
	cleaned := cleanJSON(response)

	var data ExtractedGraphData
	if err := json.Unmarshal([]byte(cleaned), &data); err != nil {
		return nil, fmt.Errorf("failed to parse extraction JSON: %w", err)
	}

	return &data, nil
}

func cleanJSON(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	return strings.TrimSpace(text)
}
