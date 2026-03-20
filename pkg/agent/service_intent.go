package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

// recognizeIntent performs intent recognition using planner
func (s *Service) recognizeIntent(ctx context.Context, goal string, session *Session) (*IntentRecognitionResult, error) {
	return s.planner.RecognizeIntent(ctx, goal, session)
}

// shouldUseRAG determines if RAG should be used based on intent
func (s *Service) shouldUseRAG(intent *IntentRecognitionResult) bool {
	// Use RAG for query, analysis, and general_qa intents
	return intent.IntentType == "rag_query" ||
		intent.IntentType == "analysis" ||
		intent.IntentType == "general_qa" ||
		intent.IntentType == "question"
}

// shouldUseSkills determines if skills should be used based on intent
func (s *Service) shouldUseSkills(intent *IntentRecognitionResult) bool {
	// Use skills for web_search, file operations, etc.
	return intent.IntentType == "web_search" ||
		intent.IntentType == "file_create" ||
		intent.IntentType == "file_read"
}

// executeSkills executes skills based on intent
func (s *Service) executeSkills(ctx context.Context, intent *IntentRecognitionResult, prompt string) (interface{}, error) {
	// Find relevant skill based on intent
	if s.skillsService == nil {
		return nil, fmt.Errorf("skills service not available")
	}

	// List available skills
	skillList, err := s.skillsService.ListSkills(ctx, skills.SkillFilter{})
	if err != nil {
		return nil, err
	}

	// Map intents to skill keyword patterns
	intentSkillPatterns := map[string][]string{
		"web_search":  {"search", "web", "query", "rag"},
		"rag_query":   {"query", "rag", "search"},
		"file_create": {"create", "write", "file"},
		"file_read":   {"read", "file", "open"},
	}

	// Find patterns for this intent
	patterns, hasPatterns := intentSkillPatterns[intent.IntentType]
	if !hasPatterns {
		// No specific patterns, try any skill
		for _, sk := range skillList {
			req := &skills.ExecutionRequest{
				SkillID:     sk.ID,
				Variables:   map[string]interface{}{"query": intent.Topic},
				Interactive: false,
			}
			result, err := s.skillsService.Execute(ctx, req)
			if err == nil && result.Success {
				return result.Output, nil
			}
		}
	}

	// Try to find a matching skill
	for _, sk := range skillList {
		skillIDLower := strings.ToLower(sk.ID)
		for _, pattern := range patterns {
			if strings.Contains(skillIDLower, pattern) {
				req := &skills.ExecutionRequest{
					SkillID:     sk.ID,
					Variables:   map[string]interface{}{"query": intent.Topic},
					Interactive: false,
				}
				result, err := s.skillsService.Execute(ctx, req)
				if err == nil && result.Success {
					return result.Output, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no suitable skill found for intent: %s", intent.IntentType)
}
