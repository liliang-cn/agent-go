// Prompt preview — what the first turn of a run would actually be.
//
// A run's first request is assembled from a dozen places: the system prompt
// and its sections, recalled memory, RAG, a plan and notes an earlier segment
// left behind, extension-contributed context, a skill reminder, the filtered
// history, and the tool catalogue the policies survived. Every one of them is
// a place a run can go wrong before the model has seen a single token, and
// until now the only way to look at the result was to run the thing — which
// costs a model call, writes a session, and may set a reminder or send a mail
// on the way.
//
// Preview is that same assembly with the model call and the persistence taken
// out. It deliberately calls the loop's own helpers rather than describing
// them again: a preview that drifts from what the loop sends is worse than no
// preview at all.
package agent

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// Preview is what the first model turn of a run would receive.
type Preview struct {
	// SessionID and TaskID are the conversation and task the previewed run
	// would belong to. When no session was named, SessionID is a throwaway
	// id: nothing was created.
	SessionID string `json:"session_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`

	// Model is the model the turn would be sent to.
	Model string `json:"model,omitempty"`

	// Messages is the exact message list the provider would be handed,
	// system message first.
	Messages []domain.Message `json:"messages"`

	// SystemPrompt is Messages[0].Content, lifted out because it is the part
	// people actually read.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Tools is the tool catalogue the turn would offer, in the order the
	// request would carry it (sorted by name, skill-first promotion applied).
	Tools []domain.ToolDefinition `json:"tools"`

	// EstimatedTokens is the runtime's own estimate of Messages, from the
	// same pool token counter the loop uses for its budget decisions. It is
	// an estimate and does not count the tool schemas.
	EstimatedTokens int `json:"estimated_tokens"`

	// Constraints are the constraints the run would enforce.
	Constraints RunConstraints `json:"constraints"`

	// ConstraintExtractionSkipped is true when the run would have resolved
	// its constraints by asking the model, and the preview did not — the one
	// thing a preview cannot show without doing what it promised not to do.
	// Constraints declared outright (WithToolsDisabled,
	// WithRequiredDeliverables, WithRequiredActions) are always reflected,
	// and skip the model call in a real run too, so this stays false for
	// them.
	ConstraintExtractionSkipped bool `json:"constraint_extraction_skipped,omitempty"`
}

// Preview assembles the first model turn of a run and returns it without
// sending it.
//
// Nothing is persisted and nothing is started: no session or history write, no
// checkpoint, no memory auto-store, no entry in the cancel registry, and no
// call to the model — not even the constraint-extraction call, whose absence
// is reported on the result rather than hidden.
//
//	p, err := svc.Preview(ctx, "summarise the changelog", agent.WithToolAllowlist("fs_read"))
//	fmt.Println(p.SystemPrompt, len(p.Tools), p.EstimatedTokens)
//
// It accepts the same RunOptions as Run, so a preview of a configured run is
// the same call with the same options.
func (s *Service) Preview(ctx context.Context, goal string, opts ...RunOption) (*Preview, error) {
	if s == nil {
		return nil, ErrServiceClosed
	}
	// A closed service has released its store and its memory, so what it
	// would assemble now is not what it would have assembled. Refuse for the
	// same reason startRun does.
	if s.Closed() {
		return nil, ErrServiceClosed
	}

	cfg := DefaultRunConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	session := s.previewSession(cfg)
	taskID := ensureTaskID(session, cfg)
	ctx = withCurrentSession(ctx, session)

	// The run-scoped system-prompt sections, resolved exactly where startRun
	// resolves them. All four are reads.
	if cfg.recalledContext == "" {
		cfg.recalledContext = s.recallRunMemory(ctx, goal)
	}
	if cfg.resumedPlan == "" {
		cfg.resumedPlan = s.planSummaryForRun(cfg.PlanKey, taskID)
	}
	if cfg.resumedTask == "" {
		cfg.resumedTask = s.taskResumeForRun(ctx, taskID)
	}
	if cfg.resumedWorkspace == "" {
		cfg.resumedWorkspace = s.workspaceSummaryForRun(ctx)
	}
	if cfg.resumedNotes == "" {
		cfg.resumedNotes = s.notesForRun(ctx)
	}

	constraints, skipped := s.previewConstraints(goal, cfg)
	cfg.resolvedConstraints = &constraints

	resuming := len(cfg.ResumeMessages) > 0

	// The loop emits UserPromptSubmit before it builds any context, because
	// a handler may rewrite the goal — and because that is the seam an
	// extension's ContributeContext is wired to. A preview that skipped it
	// would show neither.
	var injected []domain.Message
	if !resuming && s.hooks != nil {
		out, err := s.hooks.EmitWithResult(ctx, HookEventUserPromptSubmit, HookData{
			SessionID:  session.GetID(),
			AgentID:    currentAgentID(s.resolveCurrentAgent(session), s.agent),
			TaskID:     taskID,
			Goal:       goal,
			UserPrompt: goal,
		})
		if err != nil {
			return nil, err
		}
		if rewritten := strings.TrimSpace(out.UserPrompt); rewritten != "" {
			goal = rewritten
		}
		injected = out.AdditionalSystemMessages
	}
	if cfg.StructuredOutput != nil {
		if hint := buildStructuredOutputSystemHint(cfg.StructuredOutput); hint != "" {
			injected = append(injected, domain.Message{Role: "system", Content: hint})
		}
	}

	prepared := s.prepareConversationContext(ctx, goal, session, prepareConversationOptions{
		includeIntent: true,
		dryRun:        true,
	})

	messages := prepared.messages
	if resuming {
		messages = append([]domain.Message(nil), cfg.ResumeMessages...)
	}
	if len(injected) > 0 {
		messages = append(append([]domain.Message(nil), injected...), messages...)
	}
	if !resuming && len(cfg.InputParts) > 0 {
		attachInputParts(messages, cfg.InputParts)
	}

	// The policy the loop would build, with the skill relevance the dry-run
	// context assembly just computed rather than the copy it would normally
	// have written to the service on its way past.
	policy := s.buildToolPreparationPolicy(ctx)
	if prepared.skillReminder != nil && len(prepared.skillReminder.All) > 0 {
		policy.RelevantSkillNames = prepared.skillReminder.All
		if !s.isRelevantSkillSatisfied(policy.SessionID, policy.TaskID) {
			policy.ForceSkillFirst = true
		}
	}

	currentAgent := s.resolveCurrentAgent(session)
	tools, genMessages := s.prepareTurnInputsWithPolicy(ctx, currentAgent, messages, goal, cfg, policy)

	model := s.modelName
	if model == "" {
		model = "default"
	}
	systemPrompt := ""
	if len(genMessages) > 0 && genMessages[0].Role == "system" {
		systemPrompt = genMessages[0].Content
	}

	return &Preview{
		SessionID:                   session.GetID(),
		TaskID:                      taskID,
		Model:                       s.modelName,
		Messages:                    genMessages,
		SystemPrompt:                systemPrompt,
		Tools:                       tools,
		EstimatedTokens:             pool.NewTokenCounter().EstimateConversationTokens(genMessages, model),
		Constraints:                 constraints,
		ConstraintExtractionSkipped: skipped,
	}, nil
}

// previewSession resolves the conversation a preview should be assembled
// against, always as a detached copy. Store.GetSession already hands back a
// fresh Session for every call, so the history and context a preview reads are
// real while everything it writes onto them dies with the call.
//
// It never calls SetSessionID or ResetSession: looking at what a run would say
// must not move the service's own conversation pointer.
func (s *Service) previewSession(cfg *RunConfig) *Session {
	sessionID := strings.TrimSpace(cfg.SessionID)
	if sessionID == "" {
		sessionID = s.CurrentSessionID()
	}
	if sessionID == "" {
		// Nothing to preview against. A throwaway id keeps the assembly
		// honest — a run started now would get a new session too — without
		// creating one.
		return NewSessionWithID("preview-"+uuid.NewString(), s.agentID())
	}
	if s.store != nil {
		if session, err := s.store.GetSession(sessionID); err == nil && session != nil {
			return session
		}
	}
	return NewSessionWithID(sessionID, s.agentID())
}

// agentID is the service's own agent id, or "" when it has none.
func (s *Service) agentID() string {
	if s == nil || s.agent == nil {
		return ""
	}
	return s.agent.ID()
}

// previewConstraints mirrors resolveRunConstraints's decision tree, stopping
// where it would reach for the model. The second return says whether that
// happened, so a caller reading Constraints knows whether they are the run's
// constraints or merely the declared ones.
func (s *Service) previewConstraints(goal string, cfg *RunConfig) (RunConstraints, bool) {
	declared := RunConstraints{
		ForbidTools:      cfg.ToolsDisabled,
		Deliverables:     cfg.RequiredDeliverables,
		RequestedActions: cfg.RequiredActions,
	}
	if !declared.Empty() {
		return declared, false
	}
	if cfg.DisableConstraintExtraction {
		return declared, false
	}
	if strings.TrimSpace(goal) == "" || s.llmService == nil {
		return declared, false
	}
	return RunConstraints{}, true
}

// attachInputParts fronts the last user message's text as a text part and
// appends the caller's parts after it, the way the loop does for a multimodal
// run.
func attachInputParts(messages []domain.Message, inputParts []domain.MessagePart) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		parts := make([]domain.MessagePart, 0, len(inputParts)+1)
		if c := messages[i].Content; c != "" {
			parts = append(parts, domain.TextPart(c))
		}
		parts = append(parts, inputParts...)
		messages[i].Parts = parts
		return
	}
}
