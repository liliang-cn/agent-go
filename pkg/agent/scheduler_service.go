package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/scheduler"
)

// The convenience layer over PromptExecutor.
//
// Wiring a scheduler by hand means building it, registering the executor,
// starting it, and translating scheduler.Task into whatever the application
// shows the user. Every host would write the same twenty lines, so it is here
// once. NewPromptExecutor remains available for anyone who wants to drive
// pkg/scheduler directly.

// ScheduledPrompt is one scheduled agent run, in the shape a host would show.
type ScheduledPrompt struct {
	ID       string     `json:"id"`
	Prompt   string     `json:"prompt"`
	Schedule string     `json:"schedule"`
	Note     string     `json:"note,omitempty"`
	Session  string     `json:"session,omitempty"`
	Enabled  bool       `json:"enabled"`
	NextRun  *time.Time `json:"next_run,omitempty"`
	LastRun  *time.Time `json:"last_run,omitempty"`
}

// PromptScheduler runs prompts on a cron schedule.
//
// The schedule lives in the same database as the sessions, so it survives a
// restart and is visible to any process pointed at that home directory — which
// is what lets a background daemon keep the timers running while the UI is
// closed.
type PromptScheduler struct {
	inner *scheduler.TaskScheduler
	exec  *PromptExecutor
}

// NewPromptScheduler builds a scheduler for this service. It is not started;
// call Start.
func (s *Service) NewPromptScheduler(opts ...PromptExecutorOption) (*PromptScheduler, error) {
	if s == nil {
		return nil, fmt.Errorf("nil service")
	}
	inner := scheduler.NewScheduler(s.cfg)
	exec := NewPromptExecutor(s, opts...)
	inner.RegisterExecutor(exec)
	return &PromptScheduler{inner: inner, exec: exec}, nil
}

// Start begins the cron loop: this process will fire due schedules.
func (p *PromptScheduler) Start() error {
	if p == nil || p.inner == nil {
		return fmt.Errorf("scheduler not built")
	}
	return p.inner.Start()
}

// StartManageOnly opens the store for reading and editing schedules without
// firing any.
//
// Use it in a process that shares a home directory with another that owns
// execution — otherwise every schedule runs once per process. Which process owns
// execution is the host's decision (an advisory lock is the usual way); this only
// provides the half that does not.
func (p *PromptScheduler) StartManageOnly() error {
	if p == nil || p.inner == nil {
		return fmt.Errorf("scheduler not built")
	}
	return p.inner.StartManageOnly()
}

// IsExecuting reports whether this process fires schedules or merely manages
// them, which a UI needs in order to say so.
func (p *PromptScheduler) IsExecuting() bool {
	if p == nil || p.inner == nil {
		return false
	}
	return p.inner.IsExecuting()
}

// Stop halts the cron loop. In-flight runs finish.
func (p *PromptScheduler) Stop() error {
	if p == nil || p.inner == nil {
		return nil
	}
	return p.inner.Stop()
}

// Schedule registers a prompt to run on a cron expression ("0 8 * * *") or a
// shorthand ("@daily"). session names the conversation runs append to, so the
// history ends up somewhere a user can read it; empty uses the executor default.
func (p *PromptScheduler) Schedule(prompt, cronExpr, note, session string) (ScheduledPrompt, error) {
	prompt = strings.TrimSpace(prompt)
	cronExpr = strings.TrimSpace(cronExpr)
	if prompt == "" || cronExpr == "" {
		return ScheduledPrompt{}, fmt.Errorf("prompt and schedule are both required")
	}
	if note == "" {
		note = prompt
	}
	id, err := p.inner.CreateTask(&scheduler.Task{
		Type:        string(TaskTypeAgentPrompt),
		Schedule:    cronExpr,
		Description: note,
		Enabled:     true,
		Parameters: map[string]string{
			ParamPrompt:    prompt,
			ParamSessionID: strings.TrimSpace(session),
		},
	})
	if err != nil {
		return ScheduledPrompt{}, err
	}
	task, err := p.inner.GetTask(id)
	if err != nil {
		return ScheduledPrompt{}, err
	}
	return toScheduledPrompt(task), nil
}

// List returns the scheduled prompts, including disabled ones. Tasks of other
// types (script, mcp, …) sharing the same store are left out.
func (p *PromptScheduler) List() ([]ScheduledPrompt, error) {
	tasks, err := p.inner.ListTasks(true)
	if err != nil {
		return nil, err
	}
	out := make([]ScheduledPrompt, 0, len(tasks))
	for _, t := range tasks {
		if t == nil || t.Type != string(TaskTypeAgentPrompt) {
			continue
		}
		out = append(out, toScheduledPrompt(t))
	}
	return out, nil
}

// SetEnabled pauses or resumes a schedule without discarding it.
func (p *PromptScheduler) SetEnabled(id string, enabled bool) error {
	return p.inner.EnableTask(id, enabled)
}

// Delete removes a schedule.
func (p *PromptScheduler) Delete(id string) error { return p.inner.DeleteTask(id) }

// RunNow executes a schedule immediately. Without this, finding out whether
// "0 8 * * *" does what was meant takes until tomorrow morning.
func (p *PromptScheduler) RunNow(id string) (*scheduler.TaskResult, error) {
	return p.inner.RunTask(id)
}

// History returns recent executions of a schedule, newest first.
func (p *PromptScheduler) History(id string, limit int) ([]*scheduler.TaskExecution, error) {
	if limit <= 0 {
		limit = 20
	}
	return p.inner.GetTaskExecutions(id, limit)
}

func toScheduledPrompt(t *scheduler.Task) ScheduledPrompt {
	return ScheduledPrompt{
		ID:       t.ID,
		Prompt:   t.Parameters[ParamPrompt],
		Schedule: t.Schedule,
		Note:     t.Description,
		Session:  t.Parameters[ParamSessionID],
		Enabled:  t.Enabled,
		NextRun:  t.NextRun,
		LastRun:  t.LastRun,
	}
}
