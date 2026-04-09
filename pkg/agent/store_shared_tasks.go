package agent

import (
	"encoding/json"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func (s *Store) SaveSharedTask(task *SharedTask) error {
	if task == nil {
		return fmt.Errorf("shared task is required")
	}

	resultsJSON, _ := json.Marshal(task.Results)

	return s.agentGoDB.SaveSharedTask(&store.SharedTask{
		ID:          task.ID,
		SessionID:   task.SessionID,
		TeamID:      task.TeamID,
		TeamName:    task.TeamName,
		CaptainName: task.CaptainName,
		AgentNames:  task.AgentNames,
		Prompt:      task.Prompt,
		AckMessage:  task.AckMessage,
		Status:      string(task.Status),
		QueuedAhead: task.QueuedAhead,
		ResultText:  task.ResultText,
		Results:     resultsJSON,
		CreatedAt:   task.CreatedAt,
		StartedAt:   task.StartedAt,
		FinishedAt:  task.FinishedAt,
	})
}

func (s *Store) ListSharedTasksPersisted() ([]*SharedTask, error) {
	tasks, err := s.agentGoDB.ListSharedTasks()
	if err != nil {
		return nil, err
	}

	result := make([]*SharedTask, len(tasks))
	for i, t := range tasks {
		task := &SharedTask{
			ID:          t.ID,
			SessionID:   t.SessionID,
			TeamID:      t.TeamID,
			TeamName:    t.TeamName,
			CaptainName: t.CaptainName,
			AgentNames:  t.AgentNames,
			Prompt:      t.Prompt,
			AckMessage:  t.AckMessage,
			Status:      SharedTaskStatus(t.Status),
			QueuedAhead: t.QueuedAhead,
			ResultText:  t.ResultText,
			CreatedAt:   t.CreatedAt,
			StartedAt:   t.StartedAt,
			FinishedAt:  t.FinishedAt,
		}
		_ = json.Unmarshal(t.Results, &task.Results)
		result[i] = task
	}
	return result, nil
}
