package agent

import "strings"

// ToolStatus represents the state of a tool in the execution pipeline
type ToolStatus string

const (
	ToolStatusQueued    ToolStatus = "queued"
	ToolStatusExecuting ToolStatus = "executing"
	ToolStatusCompleted ToolStatus = "completed"
	ToolStatusYielded   ToolStatus = "yielded"
)

const (
	InterruptBehaviorCancel = "cancel"
	InterruptBehaviorBlock  = "block"
)

func normalizeInterruptBehavior(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InterruptBehaviorCancel:
		return InterruptBehaviorCancel
	case InterruptBehaviorBlock:
		return InterruptBehaviorBlock
	default:
		return ""
	}
}

func (s *Service) toolInterruptBehavior(toolName string, currentAgent *Agent) string {
	behavior := normalizeInterruptBehavior(s.lookupToolMetadataForAgent(toolName, currentAgent).InterruptBehavior)
	if behavior == "" {
		if s.lookupToolMetadataForAgent(toolName, currentAgent).ReadOnly {
			behavior = InterruptBehaviorCancel
		} else {
			behavior = InterruptBehaviorBlock
		}
	}
	return behavior
}

func (s *Service) beginToolExecution(toolName string, currentAgent *Agent) (string, func()) {
	behavior := s.toolInterruptBehavior(toolName, currentAgent)

	s.inProgressToolsMu.Lock()
	s.inProgressTools[behavior]++
	s.inProgressToolsMu.Unlock()

	return behavior, func() {
		s.inProgressToolsMu.Lock()
		defer s.inProgressToolsMu.Unlock()
		if s.inProgressTools[behavior] > 1 {
			s.inProgressTools[behavior]--
			return
		}
		delete(s.inProgressTools, behavior)
	}
}

func (s *Service) hasBlockingToolInProgress() bool {
	return s.blockingToolCount() > 0
}

func (s *Service) blockingToolCount() int {
	s.inProgressToolsMu.RLock()
	defer s.inProgressToolsMu.RUnlock()
	return s.inProgressTools[InterruptBehaviorBlock]
}
