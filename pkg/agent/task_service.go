package agent

import (
	"context"

	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

type TaskListOptions struct {
	Limit int
}

type TaskSubmitOptions struct {
	SessionID  string
	AgentName  string
	TeamID     string
	AgentNames []string
	Input      string
}

type TaskYieldOptions struct {
	Reason string
}

type TaskResumeOptions struct {
	Input any
}

// TaskService is the library-facing facade for first-class AgentGo tasks.
type TaskService struct {
	manager *TeamManager
}

func (m *TeamManager) Tasks() *TaskService {
	return &TaskService{manager: m}
}

func (s *TaskService) Submit(ctx context.Context, opts TaskSubmitOptions) (*taskpkg.Task, error) {
	if opts.TeamID != "" || len(opts.AgentNames) > 0 {
		task, err := s.manager.SubmitTeamTask(ctx, opts.SessionID, opts.TeamID, opts.Input, opts.AgentNames)
		if err != nil {
			return nil, err
		}
		return s.manager.GetUnifiedTask(task.ID)
	}
	task, err := s.manager.SubmitAgentTask(ctx, opts.SessionID, opts.AgentName, opts.Input)
	if err != nil {
		return nil, err
	}
	return s.manager.GetUnifiedTask(task.ID)
}

func (s *TaskService) Get(ctx context.Context, taskID string) (*taskpkg.Task, error) {
	_ = ctx
	return s.manager.GetUnifiedTask(taskID)
}

func (s *TaskService) List(ctx context.Context, opts TaskListOptions) ([]*taskpkg.Task, error) {
	_ = ctx
	return s.manager.ListUnifiedTasks(opts.Limit), nil
}

func (s *TaskService) Await(ctx context.Context, taskID string) (*taskpkg.Task, error) {
	return s.manager.AwaitTask(ctx, taskID)
}

func (s *TaskService) Yield(ctx context.Context, taskID string, opts TaskYieldOptions) (*taskpkg.Task, error) {
	return s.manager.YieldTask(ctx, taskID, opts.Reason)
}

func (s *TaskService) Resume(ctx context.Context, taskID string, opts TaskResumeOptions) (*taskpkg.Task, error) {
	return s.manager.ResumeTask(ctx, taskID, opts.Input)
}

func (s *TaskService) Cancel(ctx context.Context, taskID string) (*taskpkg.Task, error) {
	if _, err := s.manager.CancelTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.manager.GetUnifiedTask(taskID)
}

// ListCheckpoints returns up to `limit` checkpoints for a task in
// descending Seq order. Library-facing wrapper around the underlying
// store call so external consumers (UI / API server) don't need a
// reference to the package-private store.
func (s *TaskService) ListCheckpoints(ctx context.Context, taskID string, limit int) ([]*TaskCheckpoint, error) {
	_ = ctx
	if s == nil || s.manager == nil || s.manager.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = MaxCheckpointsPerTask
	}
	return s.manager.store.ListTaskCheckpoints(taskID, limit)
}

// GetCheckpoint loads a single checkpoint by its ID.
func (s *TaskService) GetCheckpoint(ctx context.Context, checkpointID string) (*TaskCheckpoint, error) {
	_ = ctx
	if s == nil || s.manager == nil || s.manager.store == nil {
		return nil, nil
	}
	return s.manager.store.GetTaskCheckpoint(checkpointID)
}
