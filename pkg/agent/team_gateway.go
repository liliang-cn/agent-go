package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const TeamGatewayProtocolVersion = "v1"

type TeamResponseStatus string

const (
	TeamResponseStatusQueued    TeamResponseStatus = "queued"
	TeamResponseStatusRunning   TeamResponseStatus = "running"
	TeamResponseStatusCompleted TeamResponseStatus = "completed"
	TeamResponseStatusFailed    TeamResponseStatus = "failed"
	TeamResponseStatusCancelled TeamResponseStatus = "cancelled"
)

type TeamResponseEventType string

const (
	TeamResponseEventTypeCreated   TeamResponseEventType = "created"
	TeamResponseEventTypeQueued    TeamResponseEventType = "queued"
	TeamResponseEventTypeStarted   TeamResponseEventType = "started"
	TeamResponseEventTypeRuntime   TeamResponseEventType = "runtime"
	TeamResponseEventTypeProgress  TeamResponseEventType = "progress"
	TeamResponseEventTypeCompleted TeamResponseEventType = "completed"
	TeamResponseEventTypeFailed    TeamResponseEventType = "failed"
	TeamResponseEventTypeCancelled TeamResponseEventType = "cancelled"
)

type TeamRequest struct {
	ProtocolVersion string         `json:"protocol_version,omitempty"`
	ID              string         `json:"id"`
	SessionID       string         `json:"session_id,omitempty"`
	TeamID          string         `json:"team_id,omitempty"`
	TeamName        string         `json:"team_name,omitempty"`
	Prompt          string         `json:"prompt"`
	AgentNames      []string       `json:"agent_names,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	RequestedAt     time.Time      `json:"requested_at"`
}

type TeamResponse struct {
	ProtocolVersion string             `json:"protocol_version,omitempty"`
	ID              string             `json:"id"`
	RequestID       string             `json:"request_id,omitempty"`
	SessionID       string             `json:"session_id,omitempty"`
	TeamID          string             `json:"team_id,omitempty"`
	TeamName        string             `json:"team_name,omitempty"`
	CaptainName     string             `json:"captain_name,omitempty"`
	AgentNames      []string           `json:"agent_names,omitempty"`
	Prompt          string             `json:"prompt,omitempty"`
	Status          TeamResponseStatus `json:"status"`
	AckMessage      string             `json:"ack_message,omitempty"`
	ResultText      string             `json:"result_text,omitempty"`
	Error           string             `json:"error,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	StartedAt       *time.Time         `json:"started_at,omitempty"`
	FinishedAt      *time.Time         `json:"finished_at,omitempty"`
	Metadata        map[string]any     `json:"metadata,omitempty"`
}

type TeamResponseEvent struct {
	ProtocolVersion string                `json:"protocol_version,omitempty"`
	ID              string                `json:"id"`
	ResponseID      string                `json:"response_id"`
	RequestID       string                `json:"request_id,omitempty"`
	SessionID       string                `json:"session_id,omitempty"`
	TeamID          string                `json:"team_id,omitempty"`
	TeamName        string                `json:"team_name,omitempty"`
	CaptainName     string                `json:"captain_name,omitempty"`
	Type            TeamResponseEventType `json:"type"`
	Status          TeamResponseStatus    `json:"status"`
	Message         string                `json:"message,omitempty"`
	Runtime         *Event                `json:"runtime,omitempty"`
	Timestamp       time.Time             `json:"timestamp"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
}

type TeamGateway interface {
	SubmitTeamRequest(ctx context.Context, req *TeamRequest) (*TeamResponse, error)
	GetTeamResponse(responseID string) (*TeamResponse, error)
	SubscribeTeamResponse(responseID string) (<-chan *TeamResponseEvent, func(), error)
	CancelTeamResponse(ctx context.Context, responseID string) (*TeamResponse, error)
}

func (m *TeamManager) SubmitTeamRequest(ctx context.Context, req *TeamRequest) (*TeamResponse, error) {
	normalized, team, err := m.normalizeTeamRequest(req)
	if err != nil {
		return nil, err
	}

	task, err := m.SubmitTeamTask(ctx, normalized.SessionID, team.ID, normalized.Prompt, normalized.AgentNames)
	if err != nil {
		return nil, err
	}

	normalized.TeamID = team.ID
	normalized.TeamName = team.Name
	m.recordTeamRequest(task.ID, normalized)
	return m.teamResponseFromAsyncTask(task), nil
}

func (m *TeamManager) GetTeamResponse(responseID string) (*TeamResponse, error) {
	task, err := m.GetTask(strings.TrimSpace(responseID))
	if err != nil {
		return nil, err
	}
	return m.teamResponseFromAsyncTask(task), nil
}

func (m *TeamManager) SubscribeTeamResponse(responseID string) (<-chan *TeamResponseEvent, func(), error) {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return nil, nil, fmt.Errorf("team response id is required")
	}
	if _, err := m.GetTask(responseID); err != nil {
		return nil, nil, err
	}

	taskEvents, unsubscribeTask, err := m.SubscribeTask(responseID)
	if err != nil {
		return nil, nil, err
	}

	out := make(chan *TeamResponseEvent, 16)
	var once sync.Once
	unsubscribe := func() {
		once.Do(unsubscribeTask)
	}

	go func() {
		defer close(out)
		defer unsubscribe()

		for evt := range taskEvents {
			converted := m.teamResponseEventFromTaskEvent(responseID, evt)
			if converted == nil {
				continue
			}
			out <- converted
		}
	}()

	return out, unsubscribe, nil
}

func (m *TeamManager) CancelTeamResponse(ctx context.Context, responseID string) (*TeamResponse, error) {
	task, err := m.CancelTask(ctx, strings.TrimSpace(responseID))
	if err != nil {
		return nil, err
	}
	return m.teamResponseFromAsyncTask(task), nil
}

func (m *TeamManager) normalizeTeamRequest(req *TeamRequest) (*TeamRequest, *Team, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("team request is required")
	}

	normalized := cloneTeamRequest(req)
	normalized.ProtocolVersion = TeamGatewayProtocolVersion
	normalized.ID = strings.TrimSpace(normalized.ID)
	normalized.SessionID = strings.TrimSpace(normalized.SessionID)
	normalized.TeamID = strings.TrimSpace(normalized.TeamID)
	normalized.TeamName = strings.TrimSpace(normalized.TeamName)
	normalized.Prompt = strings.TrimSpace(normalized.Prompt)
	if normalized.ID == "" {
		normalized.ID = uuid.NewString()
	}
	if normalized.Prompt == "" {
		return nil, nil, fmt.Errorf("team request prompt is required")
	}
	if normalized.RequestedAt.IsZero() {
		normalized.RequestedAt = time.Now()
	}
	normalized.AgentNames = normalizeStringSlice(normalized.AgentNames)

	team, err := m.resolveTeamRef(normalized.TeamID, normalized.TeamName)
	if err != nil {
		return nil, nil, err
	}
	return normalized, team, nil
}

func (m *TeamManager) recordTeamRequest(responseID string, req *TeamRequest) {
	if strings.TrimSpace(responseID) == "" || req == nil {
		return
	}
	m.teamGatewayMu.Lock()
	if m.teamRequests == nil {
		m.teamRequests = make(map[string]*TeamRequest)
	}
	m.teamRequests[strings.TrimSpace(responseID)] = cloneTeamRequest(req)
	m.teamGatewayMu.Unlock()
}

func (m *TeamManager) teamRequestForResponse(responseID string) *TeamRequest {
	m.teamGatewayMu.RLock()
	req := m.teamRequests[strings.TrimSpace(responseID)]
	m.teamGatewayMu.RUnlock()
	return cloneTeamRequest(req)
}

func (m *TeamManager) teamResponseFromAsyncTask(task *AsyncTask) *TeamResponse {
	if task == nil {
		return nil
	}
	req := m.teamRequestForResponse(task.ID)
	resp := &TeamResponse{
		ProtocolVersion: TeamGatewayProtocolVersion,
		ID:              task.ID,
		SessionID:       strings.TrimSpace(task.SessionID),
		TeamID:          strings.TrimSpace(task.TeamID),
		TeamName:        strings.TrimSpace(task.TeamName),
		CaptainName:     strings.TrimSpace(task.CaptainName),
		AgentNames:      append([]string(nil), task.AgentNames...),
		Prompt:          strings.TrimSpace(task.Prompt),
		Status:          teamResponseStatusFromAsyncTask(task.Status),
		AckMessage:      strings.TrimSpace(task.AckMessage),
		ResultText:      strings.TrimSpace(task.ResultText),
		Error:           strings.TrimSpace(task.Error),
		CreatedAt:       task.CreatedAt,
		StartedAt:       cloneTimePtr(task.StartedAt),
		FinishedAt:      cloneTimePtr(task.FinishedAt),
		Metadata:        cloneStructuredMap(nil),
	}
	if req != nil {
		resp.RequestID = req.ID
		if resp.SessionID == "" {
			resp.SessionID = req.SessionID
		}
		if resp.TeamID == "" {
			resp.TeamID = req.TeamID
		}
		if resp.TeamName == "" {
			resp.TeamName = req.TeamName
		}
		resp.Metadata = cloneStructuredMap(req.Metadata)
	}
	if resp.Metadata == nil {
		resp.Metadata = make(map[string]any)
	}
	resp.Metadata["team_response_id"] = resp.ID
	if resp.RequestID != "" {
		resp.Metadata["team_request_id"] = resp.RequestID
	}
	resp.Metadata["protocol_version"] = TeamGatewayProtocolVersion
	return resp
}

func (m *TeamManager) teamResponseEventFromTaskEvent(responseID string, evt *TaskEvent) *TeamResponseEvent {
	if evt == nil {
		return nil
	}
	req := m.teamRequestForResponse(responseID)
	out := &TeamResponseEvent{
		ProtocolVersion: TeamGatewayProtocolVersion,
		ID:              strings.TrimSpace(evt.ID),
		ResponseID:      strings.TrimSpace(responseID),
		SessionID:       strings.TrimSpace(evt.SessionID),
		TeamID:          strings.TrimSpace(evt.TeamID),
		TeamName:        strings.TrimSpace(evt.TeamName),
		CaptainName:     strings.TrimSpace(evt.CaptainName),
		Type:            teamResponseEventTypeFromTaskEvent(evt),
		Status:          teamResponseStatusFromTaskEvent(evt),
		Message:         strings.TrimSpace(evt.Message),
		Runtime:         cloneAgentEvent(evt.Runtime),
		Timestamp:       evt.Timestamp,
		Metadata:        cloneStructuredMap(nil),
	}
	if out.ID == "" {
		out.ID = uuid.NewString()
	}
	if req != nil {
		out.RequestID = req.ID
		if out.SessionID == "" {
			out.SessionID = req.SessionID
		}
		if out.TeamID == "" {
			out.TeamID = req.TeamID
		}
		if out.TeamName == "" {
			out.TeamName = req.TeamName
		}
		out.Metadata = cloneStructuredMap(req.Metadata)
	}
	if out.Metadata == nil {
		out.Metadata = make(map[string]any)
	}
	out.Metadata["team_response_id"] = out.ResponseID
	if out.RequestID != "" {
		out.Metadata["team_request_id"] = out.RequestID
	}
	out.Metadata["protocol_version"] = TeamGatewayProtocolVersion
	return out
}

func teamResponseStatusFromAsyncTask(status AsyncTaskStatus) TeamResponseStatus {
	switch status {
	case AsyncTaskStatusRunning:
		return TeamResponseStatusRunning
	case AsyncTaskStatusCompleted:
		return TeamResponseStatusCompleted
	case AsyncTaskStatusFailed:
		return TeamResponseStatusFailed
	case AsyncTaskStatusCancelled:
		return TeamResponseStatusCancelled
	default:
		return TeamResponseStatusQueued
	}
}

func teamResponseStatusFromTaskEvent(evt *TaskEvent) TeamResponseStatus {
	if evt == nil {
		return TeamResponseStatusQueued
	}
	if evt.Status != "" {
		return teamResponseStatusFromAsyncTask(evt.Status)
	}
	switch evt.Type {
	case TaskEventTypeCompleted:
		return TeamResponseStatusCompleted
	case TaskEventTypeFailed:
		return TeamResponseStatusFailed
	case TaskEventTypeCancelled:
		return TeamResponseStatusCancelled
	case TaskEventTypeStarted, TaskEventTypeRuntime:
		return TeamResponseStatusRunning
	default:
		return TeamResponseStatusQueued
	}
}

func teamResponseEventTypeFromTaskEvent(evt *TaskEvent) TeamResponseEventType {
	if evt == nil {
		return TeamResponseEventTypeRuntime
	}
	switch evt.Type {
	case TaskEventTypeCreated:
		return TeamResponseEventTypeCreated
	case TaskEventTypeQueued:
		return TeamResponseEventTypeQueued
	case TaskEventTypeStarted:
		return TeamResponseEventTypeStarted
	case TaskEventTypeCompleted:
		return TeamResponseEventTypeCompleted
	case TaskEventTypeFailed:
		return TeamResponseEventTypeFailed
	case TaskEventTypeCancelled:
		return TeamResponseEventTypeCancelled
	case TaskEventTypeRuntime:
		if evt.Runtime != nil && (evt.Runtime.Type == EventTypePartial || evt.Runtime.Type == EventTypeComplete) {
			return TeamResponseEventTypeProgress
		}
		return TeamResponseEventTypeRuntime
	default:
		return TeamResponseEventTypeRuntime
	}
}

func cloneTeamRequest(req *TeamRequest) *TeamRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.AgentNames = append([]string(nil), req.AgentNames...)
	cloned.Metadata = cloneStructuredMap(req.Metadata)
	return &cloned
}

func cloneStructuredMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
