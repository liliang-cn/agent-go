package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type builtInRuntimeDispatchFunc func(context.Context, string, string, []RunOption) (*ExecutionResult, error)
type builtInRuntimeStreamDispatchFunc func(context.Context, string, string, []RunOption) (<-chan *Event, error)

type builtInAgentRuntime struct {
	agentName string
	inbox     chan *builtInRuntimeRequest
	cancel    context.CancelFunc
	done      chan struct{}
	workerCnt int
	statsMu   sync.RWMutex
	active    int
	processed int
	lastMsg   *AgentMessage
	lastError string
	lastSeen  time.Time
}

type builtInRuntimeRequest struct {
	ctx         context.Context
	message     *AgentMessage
	instruction string
	runOptions  []RunOption
	stream      bool
	resultCh    chan builtInRuntimeResult
	eventCh     chan *Event
}

type builtInRuntimeResult struct {
	message *AgentMessage
	result  *ExecutionResult
	err     error
}

type builtInRuntimeSnapshot struct {
	AgentName         string
	WorkerCount       int
	ActiveWorkers     int
	QueueDepth        int
	Active            bool
	ProcessedCount    int
	LastMessageType   AgentMessageType
	LastCorrelationID string
	LastError         string
	LastSeenAt        *time.Time
}

func defaultBuiltInRuntimeAgentNames() []string {
	return []string{
		defaultConciergeAgentName,
		defaultCaptainAgentName,
		defaultIntentRouterAgentName,
		defaultPromptOptimizerAgentName,
		defaultAssistantAgentName,
		defaultOperatorAgentName,
		defaultStakeholderAgentName,
		defaultArchivistAgentName,
		defaultVerifierAgentName,
	}
}

func builtInRuntimeWorkerCount(agentName string) int {
	switch strings.ToLower(strings.TrimSpace(agentName)) {
	case strings.ToLower(defaultConciergeAgentName):
		return 8
	case strings.ToLower(defaultIntentRouterAgentName):
		return 4
	case strings.ToLower(defaultPromptOptimizerAgentName):
		return 4
	default:
		return 1
	}
}

func builtInRuntimeQueueSize(agentName string) int {
	switch strings.ToLower(strings.TrimSpace(agentName)) {
	case strings.ToLower(defaultConciergeAgentName):
		return 256
	case strings.ToLower(defaultIntentRouterAgentName), strings.ToLower(defaultPromptOptimizerAgentName):
		return 128
	default:
		return 32
	}
}

func isBuiltInRuntimeAgentName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, candidate := range defaultBuiltInRuntimeAgentNames() {
		if name == strings.ToLower(candidate) {
			return true
		}
	}
	return false
}

func (m *TeamManager) startBuiltInRuntimes(ctx context.Context) error {
	for _, name := range defaultBuiltInRuntimeAgentNames() {
		if err := m.ensureBuiltInAgentRuntime(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (m *TeamManager) ensureBuiltInAgentRuntime(_ context.Context, agentName string) error {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return fmt.Errorf("agent name is required")
	}

	model, err := m.store.GetAgentModelByName(agentName)
	if err != nil {
		return err
	}
	if !isBuiltInAgentModel(model) {
		return nil
	}

	m.runtimeMu.Lock()
	if m.builtInRuntimes == nil {
		m.builtInRuntimes = make(map[string]*builtInAgentRuntime)
	}
	if m.builtInRuntimes[model.Name] != nil {
		m.runtimeMu.Unlock()
		return nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	runtime := &builtInAgentRuntime{
		agentName: model.Name,
		inbox:     make(chan *builtInRuntimeRequest, builtInRuntimeQueueSize(model.Name)),
		cancel:    cancel,
		done:      make(chan struct{}),
		workerCnt: builtInRuntimeWorkerCount(model.Name),
	}
	m.builtInRuntimes[model.Name] = runtime
	m.runtimeMu.Unlock()

	m.mu.Lock()
	if m.runningAgents == nil {
		m.runningAgents = make(map[string]context.CancelFunc)
	}
	m.runningAgents[model.Name] = cancel
	m.mu.Unlock()

	for i := 0; i < runtime.workerCnt; i++ {
		go m.runBuiltInAgentRuntime(runCtx, runtime)
	}
	return nil
}

func (m *TeamManager) stopBuiltInAgentRuntime(agentName string) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return
	}

	m.runtimeMu.Lock()
	runtime := m.builtInRuntimes[agentName]
	delete(m.builtInRuntimes, agentName)
	m.runtimeMu.Unlock()

	if runtime != nil && runtime.cancel != nil {
		runtime.cancel()
	}
}

func (m *TeamManager) builtInAgentRuntime(agentName string) *builtInAgentRuntime {
	agentName = strings.TrimSpace(agentName)
	m.runtimeMu.RLock()
	defer m.runtimeMu.RUnlock()
	return m.builtInRuntimes[agentName]
}

func (m *TeamManager) builtInAgentRuntimeSnapshot(agentName string) *builtInRuntimeSnapshot {
	runtime := m.builtInAgentRuntime(agentName)
	if runtime == nil {
		return nil
	}
	return runtime.snapshot()
}

func (m *TeamManager) runBuiltInAgentRuntime(ctx context.Context, runtime *builtInAgentRuntime) {
	if runtime == nil {
		return
	}
	defer close(runtime.done)

	for {
		select {
		case <-ctx.Done():
			return
		case req := <-runtime.inbox:
			if req == nil {
				continue
			}
			m.handleBuiltInRuntimeRequest(runtime.agentName, req)
		}
	}
}

func (m *TeamManager) handleBuiltInRuntimeRequest(agentName string, req *builtInRuntimeRequest) {
	if req == nil {
		return
	}
	var runErr error
	if runtime := m.builtInAgentRuntime(agentName); runtime != nil {
		runtime.markStart(req.message)
		defer func() {
			runtime.markDone(runErr)
		}()
	}

	if req.stream {
		events, err := m.executeDispatchStream(req.ctx, agentName, req.instruction, req.runOptions)
		if err != nil {
			runErr = err
			m.finishBuiltInRuntimeRequest(req, builtInRuntimeResult{
				message: newAgentProtocolReply(req.message, agentName, AgentMessageTypeError, err.Error(), map[string]interface{}{
					"error": err.Error(),
				}, nil),
				err: err,
			})
			if req.eventCh != nil {
				close(req.eventCh)
			}
			return
		}

		m.finishBuiltInRuntimeRequest(req, builtInRuntimeResult{
			message: newAgentProtocolReply(req.message, agentName, AgentMessageTypeProgress, "stream_started", map[string]interface{}{
				"status": "stream_started",
			}, nil),
		})

		if req.eventCh == nil {
			return
		}
		defer close(req.eventCh)
		for {
			select {
			case <-req.ctx.Done():
				return
			case evt, ok := <-events:
				if !ok {
					return
				}
				select {
				case req.eventCh <- evt:
				case <-req.ctx.Done():
					return
				}
			}
		}
	}

	res, err := m.executeDispatchSync(req.ctx, agentName, req.instruction, req.runOptions)
	if err != nil {
		runErr = err
		m.finishBuiltInRuntimeRequest(req, builtInRuntimeResult{
			message: newAgentProtocolReply(req.message, agentName, AgentMessageTypeError, err.Error(), map[string]interface{}{
				"error": err.Error(),
			}, nil),
			err: err,
		})
		return
	}

	text := extractDispatchText(res)
	payload := map[string]interface{}{
		"text":    text,
		"success": res.Success,
	}
	if res.FinalResult != nil {
		payload["final_result"] = res.FinalResult
	}

	m.finishBuiltInRuntimeRequest(req, builtInRuntimeResult{
		message: newAgentProtocolReply(req.message, agentName, AgentMessageTypeResponse, text, payload, nil),
		result:  res,
	})
}

func (m *TeamManager) finishBuiltInRuntimeRequest(req *builtInRuntimeRequest, result builtInRuntimeResult) {
	if req == nil || req.resultCh == nil {
		return
	}
	select {
	case req.resultCh <- result:
	default:
	}
}

func (m *TeamManager) dispatchViaBuiltInRuntime(ctx context.Context, agentName, instruction string, runOptions []RunOption) (*ExecutionResult, error) {
	if current := getCurrentAgent(ctx); current != nil && strings.EqualFold(strings.TrimSpace(current.Name()), strings.TrimSpace(agentName)) {
		return m.executeDispatchSync(ctx, agentName, instruction, runOptions)
	}

	runtime := m.builtInAgentRuntime(agentName)
	if runtime == nil {
		return nil, fmt.Errorf("built-in runtime unavailable for %s", agentName)
	}

	sourceAgent := "System"
	if current := getCurrentAgent(ctx); current != nil && strings.TrimSpace(current.Name()) != "" {
		sourceAgent = strings.TrimSpace(current.Name())
	}

	req := &builtInRuntimeRequest{
		ctx:         ctx,
		instruction: instruction,
		runOptions:  append([]RunOption(nil), runOptions...),
		message: newAgentProtocolMessage(sourceAgent, agentName, AgentMessageTypeRequest, instruction, map[string]interface{}{
			"instruction": instruction,
			"mode":        "sync",
		}, nil),
		resultCh: make(chan builtInRuntimeResult, 1),
	}

	select {
	case runtime.inbox <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case result := <-req.resultCh:
		return result.result, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *TeamManager) dispatchStreamViaBuiltInRuntime(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
	if current := getCurrentAgent(ctx); current != nil && strings.EqualFold(strings.TrimSpace(current.Name()), strings.TrimSpace(agentName)) {
		return m.executeDispatchStream(ctx, agentName, instruction, runOptions)
	}

	runtime := m.builtInAgentRuntime(agentName)
	if runtime == nil {
		return nil, fmt.Errorf("built-in runtime unavailable for %s", agentName)
	}

	sourceAgent := "System"
	if current := getCurrentAgent(ctx); current != nil && strings.TrimSpace(current.Name()) != "" {
		sourceAgent = strings.TrimSpace(current.Name())
	}

	req := &builtInRuntimeRequest{
		ctx:         ctx,
		instruction: instruction,
		runOptions:  append([]RunOption(nil), runOptions...),
		stream:      true,
		eventCh:     make(chan *Event, 32),
		resultCh:    make(chan builtInRuntimeResult, 1),
		message: newAgentProtocolMessage(sourceAgent, agentName, AgentMessageTypeRequest, instruction, map[string]interface{}{
			"instruction": instruction,
			"mode":        "stream",
		}, nil),
	}

	select {
	case runtime.inbox <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case result := <-req.resultCh:
		if result.err != nil {
			return nil, result.err
		}
		return req.eventCh, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *TeamManager) executeDispatchSync(ctx context.Context, agentName, instruction string, runOptions []RunOption) (*ExecutionResult, error) {
	if m.builtInDispatchOverride != nil && isBuiltInRuntimeAgentName(agentName) {
		return m.builtInDispatchOverride(ctx, agentName, instruction, runOptions)
	}

	var (
		svc *Service
		err error
	)
	if isBuiltInDispatchOnlyAgentName(agentName) {
		svc, err = m.buildEphemeralService(agentName)
	} else {
		svc, err = m.getOrBuildService(agentName)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot dispatch to agent %s: %w", agentName, err)
	}
	return svc.Run(ctx, instruction, runOptions...)
}

func (m *TeamManager) executeDispatchStream(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
	if m.builtInStreamDispatchOverride != nil && isBuiltInRuntimeAgentName(agentName) {
		return m.builtInStreamDispatchOverride(ctx, agentName, instruction, runOptions)
	}

	var (
		svc *Service
		err error
	)
	if isBuiltInDispatchOnlyAgentName(agentName) {
		svc, err = m.buildEphemeralService(agentName)
	} else {
		svc, err = m.getOrBuildService(agentName)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot dispatch to agent %s: %w", agentName, err)
	}
	return svc.RunStreamWithOptions(ctx, instruction, runOptions...)
}

func (m *TeamManager) prepareDispatchRequest(agentName, instruction string, sessionID string, taskID string, extraOpts []RunOption) (string, []RunOption) {
	wrappedInstruction := instruction
	if cfg := m.configuredAgentGoConfig(); cfg != nil {
		wrappedInstruction = buildTeamTaskEnvelope(cfg, agentName, instruction)
	}

	runOptions := dispatchRunOptions(agentName)
	if strings.TrimSpace(sessionID) != "" {
		runOptions = append(runOptions, WithSessionID(sessionID))
	}
	if strings.TrimSpace(taskID) != "" {
		runOptions = append(runOptions, WithTaskID(taskID))
	}
	runOptions = append(runOptions, extraOpts...)
	return wrappedInstruction, runOptions
}

func (r *builtInAgentRuntime) markStart(message *AgentMessage) {
	if r == nil {
		return
	}
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	r.active++
	r.lastMsg = cloneAgentMessage(message)
	r.lastSeen = time.Now()
	r.lastError = ""
}

func (r *builtInAgentRuntime) markDone(err error) {
	if r == nil {
		return
	}
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	if r.active > 0 {
		r.active--
	}
	r.processed++
	r.lastSeen = time.Now()
	if err != nil {
		r.lastError = err.Error()
	}
}

func (r *builtInAgentRuntime) snapshot() *builtInRuntimeSnapshot {
	if r == nil {
		return nil
	}
	r.statsMu.RLock()
	defer r.statsMu.RUnlock()

	snapshot := &builtInRuntimeSnapshot{
		AgentName:      r.agentName,
		WorkerCount:    r.workerCnt,
		ActiveWorkers:  r.active,
		QueueDepth:     len(r.inbox),
		Active:         r.active > 0,
		ProcessedCount: r.processed,
		LastError:      r.lastError,
	}
	if r.lastMsg != nil {
		snapshot.LastMessageType = r.lastMsg.MessageType
		snapshot.LastCorrelationID = r.lastMsg.CorrelationID
	}
	if !r.lastSeen.IsZero() {
		seen := r.lastSeen
		snapshot.LastSeenAt = &seen
	}
	return snapshot
}
