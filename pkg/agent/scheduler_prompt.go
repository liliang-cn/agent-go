package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/scheduler"
)

// Running an agent on a schedule.
//
// pkg/scheduler has the cron loop, the store and the execution history, but its
// executors run a script, an MCP tool, a query or an ingest — none of them runs
// the agent. So the thing people actually want on a timer ("every morning at
// eight, work out my stock returns and message me") had no way to be expressed:
// each application had to write this adapter itself.
//
// A scheduled run is an ordinary turn. It gets the same tools, skills and MCP
// servers as a chat turn, which is what lets "and message me" work without the
// scheduler knowing anything about WeChat or SMTP — the agent sends it with a
// tool, exactly as it would if asked in a chat window.
//
// scheduler cannot import this package (its executors are registered from
// outside precisely to avoid that cycle), so the adapter lives here.

// TaskTypeAgentPrompt is the scheduler task type a PromptExecutor handles.
const TaskTypeAgentPrompt scheduler.TaskType = "agent_prompt"

// Task parameter keys a PromptExecutor understands.
const (
	// ParamPrompt is the goal to run. Required.
	ParamPrompt = "prompt"
	// ParamSessionID names the conversation a run appends to, so its history is
	// somewhere a user can find it. Optional.
	ParamSessionID = "session_id"
)

// PromptRun describes one finished scheduled run. Hosts use it to notify, log,
// or surface the result in a UI.
type PromptRun struct {
	Prompt    string
	SessionID string
	Answer    string
	Err       error
	// Cancelled reports that the run was stopped before it finished. Err is
	// nil in that case: a host notifying on Err would otherwise pop up a
	// failure alert for the user's own cancel button.
	Cancelled bool
	StartedAt time.Time
	Duration  time.Duration
}

// PromptExecutorOption configures a PromptExecutor.
type PromptExecutorOption func(*PromptExecutor)

// WithPromptTimeout caps a single run. Default is 15 minutes: a scheduled run
// has nobody waiting on it, so it should be allowed to take longer than a chat
// turn, but not to hang for ever and block the next occurrence.
func WithPromptTimeout(d time.Duration) PromptExecutorOption {
	return func(e *PromptExecutor) {
		if d > 0 {
			e.timeout = d
		}
	}
}

// WithPromptSessionID sets the conversation runs append to when a task does not
// name one.
func WithPromptSessionID(id string) PromptExecutorOption {
	return func(e *PromptExecutor) {
		if strings.TrimSpace(id) != "" {
			e.defaultSession = strings.TrimSpace(id)
		}
	}
}

// WithPromptObserver registers a callback invoked after every run, successful or
// not. It runs on the scheduler's goroutine, so it should not block.
func WithPromptObserver(fn func(PromptRun)) PromptExecutorOption {
	return func(e *PromptExecutor) { e.observer = fn }
}

// WithPromptRunOptions passes RunOptions to every scheduled turn (a max-turn
// budget, a specific agent, and so on).
func WithPromptRunOptions(opts ...RunOption) PromptExecutorOption {
	return func(e *PromptExecutor) { e.runOpts = append(e.runOpts, opts...) }
}

// PromptExecutor runs a scheduled prompt as an agent turn.
type PromptExecutor struct {
	svc            *Service
	timeout        time.Duration
	defaultSession string
	observer       func(PromptRun)
	runOpts        []RunOption
}

// NewPromptExecutor adapts a Service into a scheduler executor.
//
//	sch := scheduler.NewScheduler(cfg)
//	sch.RegisterExecutor(agent.NewPromptExecutor(svc))
//	sch.Start()
//	sch.CreateTask(&scheduler.Task{
//	    Type:     string(agent.TaskTypeAgentPrompt),
//	    Schedule: "0 8 * * *",
//	    Enabled:  true,
//	    Parameters: map[string]string{agent.ParamPrompt: "统计我的股票收益并发消息给我"},
//	})
func NewPromptExecutor(svc *Service, opts ...PromptExecutorOption) *PromptExecutor {
	e := &PromptExecutor{
		svc:            svc,
		timeout:        15 * time.Minute,
		defaultSession: "scheduled",
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Type implements scheduler.Executor.
func (e *PromptExecutor) Type() scheduler.TaskType { return TaskTypeAgentPrompt }

// Validate implements scheduler.Executor. It rejects a task with no prompt at
// creation time rather than letting it fail every morning at eight.
func (e *PromptExecutor) Validate(parameters map[string]string) error {
	if strings.TrimSpace(parameters[ParamPrompt]) == "" {
		return fmt.Errorf("%s is required", ParamPrompt)
	}
	return nil
}

// Execute implements scheduler.Executor.
func (e *PromptExecutor) Execute(ctx context.Context, parameters map[string]string) (*scheduler.TaskResult, error) {
	prompt := strings.TrimSpace(parameters[ParamPrompt])
	if prompt == "" {
		return nil, fmt.Errorf("%s is required", ParamPrompt)
	}
	if e == nil || e.svc == nil {
		return nil, fmt.Errorf("scheduled prompt has no agent service")
	}

	sessionID := strings.TrimSpace(parameters[ParamSessionID])
	if sessionID == "" {
		sessionID = e.defaultSession
	}

	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// Host-supplied options win, so WithPromptRunOptions can override the
	// session binding if a caller needs to.
	opts := append([]RunOption{WithSessionID(sessionID)}, e.runOpts...)
	result, err := e.svc.Run(runCtx, prompt, opts...)

	run := PromptRun{
		Prompt:    prompt,
		SessionID: sessionID,
		Err:       err,
		StartedAt: started,
		Duration:  time.Since(started),
	}
	blocked := false
	cancelled := false
	if result != nil {
		run.Answer = strings.TrimSpace(result.Text())
		blocked = result.Blocked
		cancelled = result.Cancelled
		// A run that failed without an error still failed; surface it rather
		// than recording a success with an empty answer. A *blocked* run is not
		// that: the agent answered, and its answer is why it stopped — that
		// text is exactly what the host needs to notify with, so it must not be
		// demoted to an error and dropped.
		if err == nil && !blocked && !cancelled && !result.Success && result.Error != "" {
			err = fmt.Errorf("%s", result.Error)
			run.Err = err
		}
	}
	// A cancelled run reaches here two ways: the loop reported it (result
	// .Cancelled), or the context died before the loop could say anything and
	// Run returned context.Canceled. Both are the same outcome.
	if !cancelled && errors.Is(err, context.Canceled) {
		cancelled = true
	}
	if cancelled {
		err = nil
		run.Err = nil
		run.Cancelled = true
	}
	if e.observer != nil {
		e.observer(run)
	}

	if cancelled {
		return &scheduler.TaskResult{
			Success:   false,
			Cancelled: true,
			Output:    run.Answer,
			Duration:  run.Duration,
		}, nil
	}
	if err != nil {
		return &scheduler.TaskResult{Success: false, Error: err.Error(), Duration: run.Duration}, err
	}
	if blocked {
		return &scheduler.TaskResult{
			Success:  false,
			Output:   run.Answer,
			Error:    run.Answer,
			Duration: run.Duration,
		}, nil
	}
	return &scheduler.TaskResult{Success: true, Output: run.Answer, Duration: run.Duration}, nil
}
