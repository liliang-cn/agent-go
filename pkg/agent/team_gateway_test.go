package agent

import (
	"testing"
	"time"
)

func TestGetTeamResponseIncludesRecordedRequestMetadata(t *testing.T) {
	manager := newTaskTestManager()
	now := time.Now()

	manager.upsertAsyncTask(&AsyncTask{
		ID:          "team-response-1",
		SessionID:   "session-1",
		Kind:        AsyncTaskKindTeam,
		Status:      AsyncTaskStatusQueued,
		TeamID:      "team-1",
		TeamName:    "Docs Team",
		CaptainName: "Docs Team Captain",
		Prompt:      "summarize docs",
		AckMessage:  "accepted",
		CreatedAt:   now,
	})
	manager.recordTeamRequest("team-response-1", &TeamRequest{
		ID:              "team-request-1",
		SessionID:       "session-1",
		TeamID:          "team-1",
		TeamName:        "Docs Team",
		Prompt:          "summarize docs",
		ProtocolVersion: TeamGatewayProtocolVersion,
		RequestedAt:     now,
		Metadata: map[string]any{
			"source": "test",
		},
	})

	resp, err := manager.GetTeamResponse("team-response-1")
	if err != nil {
		t.Fatalf("GetTeamResponse() error = %v", err)
	}
	if resp.RequestID != "team-request-1" {
		t.Fatalf("RequestID = %q, want %q", resp.RequestID, "team-request-1")
	}
	if resp.TeamName != "Docs Team" {
		t.Fatalf("TeamName = %q, want Docs Team", resp.TeamName)
	}
	if resp.Metadata["source"] != "test" {
		t.Fatalf("expected request metadata to be preserved, got %#v", resp.Metadata)
	}
	if resp.Metadata["team_response_id"] != "team-response-1" {
		t.Fatalf("expected response metadata to include response id, got %#v", resp.Metadata)
	}
	if resp.Metadata["team_request_id"] != "team-request-1" {
		t.Fatalf("expected response metadata to include request id, got %#v", resp.Metadata)
	}
}

func TestSubscribeTeamResponseConvertsRuntimeEvent(t *testing.T) {
	manager := newTaskTestManager()
	now := time.Now()

	manager.upsertAsyncTask(&AsyncTask{
		ID:          "team-response-live",
		SessionID:   "session-live",
		Kind:        AsyncTaskKindTeam,
		Status:      AsyncTaskStatusRunning,
		TeamID:      "team-1",
		TeamName:    "Docs Team",
		CaptainName: "Docs Team Captain",
		Prompt:      "summarize docs",
		CreatedAt:   now,
	})
	manager.recordTeamRequest("team-response-live", &TeamRequest{
		ID:          "team-request-live",
		SessionID:   "session-live",
		TeamID:      "team-1",
		TeamName:    "Docs Team",
		Prompt:      "summarize docs",
		RequestedAt: now,
	})

	events, unsubscribe, err := manager.SubscribeTeamResponse("team-response-live")
	if err != nil {
		t.Fatalf("SubscribeTeamResponse() error = %v", err)
	}
	defer unsubscribe()

	manager.emitTaskEvent("team-response-live", &TaskEvent{
		TaskID:      "team-response-live",
		SessionID:   "session-live",
		Kind:        AsyncTaskKindTeam,
		Status:      AsyncTaskStatusRunning,
		Type:        TaskEventTypeRuntime,
		TeamID:      "team-1",
		TeamName:    "Docs Team",
		CaptainName: "Docs Team Captain",
		Runtime: &Event{
			Type:      EventTypePartial,
			AgentName: "Docs Team Captain",
			Content:   "partial team output",
			Timestamp: time.Now(),
		},
		Timestamp: time.Now(),
	}, false)
	manager.completeAsyncTask("team-response-live", "done", "Docs Team Captain")

	select {
	case evt := <-events:
		if evt.Type != TeamResponseEventTypeProgress {
			t.Fatalf("Type = %q, want %q", evt.Type, TeamResponseEventTypeProgress)
		}
		if evt.RequestID != "team-request-live" {
			t.Fatalf("RequestID = %q, want %q", evt.RequestID, "team-request-live")
		}
		if evt.Runtime == nil || evt.Runtime.Content != "partial team output" {
			t.Fatalf("unexpected runtime payload: %#v", evt.Runtime)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for team response event")
	}
}
