package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newBuiltInRuntimeTestManager(t *testing.T) *TeamManager {
	t.Helper()

	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}

	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}
	return manager
}

func TestBuiltInRuntimeSerializesRequestsPerAgent(t *testing.T) {
	manager := newBuiltInRuntimeTestManager(t)

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	callDone := make(chan struct{}, 2)

	var mu sync.Mutex
	callCount := 0
	manager.builtInDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (*ExecutionResult, error) {
		mu.Lock()
		callCount++
		currentCall := callCount
		mu.Unlock()
		switch currentCall {
		case 1:
			close(firstStarted)
			<-releaseFirst
		case 2:
			close(secondStarted)
		}
		callDone <- struct{}{}
		return &ExecutionResult{Success: true, FinalResult: instruction}, nil
	}

	go func() {
		_, _ = manager.DispatchTask(context.Background(), defaultAssistantAgentName, "first")
	}()
	go func() {
		_, _ = manager.DispatchTask(context.Background(), defaultAssistantAgentName, "second")
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first built-in request did not start")
	}

	select {
	case <-secondStarted:
		t.Fatal("second request started before the first one completed")
	case <-time.After(200 * time.Millisecond):
	}

	close(releaseFirst)

	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second request did not start after releasing the first")
	}

	for i := 0; i < 2; i++ {
		select {
		case <-callDone:
		case <-time.After(time.Second):
			t.Fatal("dispatch call did not complete")
		}
	}
}

func TestBuiltInRuntimeRunsDifferentAgentsInParallel(t *testing.T) {
	manager := newBuiltInRuntimeTestManager(t)

	assistantStarted := make(chan struct{})
	archivistStarted := make(chan struct{})
	release := make(chan struct{})
	callDone := make(chan struct{}, 2)

	manager.builtInDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (*ExecutionResult, error) {
		switch agentName {
		case defaultAssistantAgentName:
			close(assistantStarted)
		case defaultArchivistAgentName:
			close(archivistStarted)
		}
		<-release
		callDone <- struct{}{}
		return &ExecutionResult{Success: true, FinalResult: instruction}, nil
	}

	go func() {
		_, _ = manager.DispatchTask(context.Background(), defaultAssistantAgentName, "assistant work")
	}()
	go func() {
		_, _ = manager.DispatchTask(context.Background(), defaultArchivistAgentName, "archivist work")
	}()

	select {
	case <-assistantStarted:
	case <-time.After(time.Second):
		t.Fatal("assistant worker did not start")
	}

	select {
	case <-archivistStarted:
	case <-time.After(time.Second):
		t.Fatal("archivist worker did not start in parallel")
	}

	close(release)

	for i := 0; i < 2; i++ {
		select {
		case <-callDone:
		case <-time.After(time.Second):
			t.Fatal("parallel built-in dispatch did not complete")
		}
	}
}

func TestConciergeRuntimeHandlesConcurrentRequests(t *testing.T) {
	manager := newBuiltInRuntimeTestManager(t)

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	release := make(chan struct{})
	callDone := make(chan struct{}, 2)

	var mu sync.Mutex
	started := 0
	manager.builtInDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (*ExecutionResult, error) {
		if agentName != defaultConciergeAgentName {
			return &ExecutionResult{Success: true, FinalResult: instruction}, nil
		}
		mu.Lock()
		started++
		current := started
		mu.Unlock()
		switch current {
		case 1:
			close(firstStarted)
		case 2:
			close(secondStarted)
		}
		<-release
		callDone <- struct{}{}
		return &ExecutionResult{Success: true, FinalResult: instruction}, nil
	}

	go func() {
		_, _ = manager.DispatchTask(context.Background(), defaultConciergeAgentName, "user request 1")
	}()
	go func() {
		_, _ = manager.DispatchTask(context.Background(), defaultConciergeAgentName, "user request 2")
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first concierge request did not start")
	}

	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second concierge request did not start concurrently")
	}

	close(release)

	for i := 0; i < 2; i++ {
		select {
		case <-callDone:
		case <-time.After(time.Second):
			t.Fatal("concierge concurrent dispatch did not complete")
		}
	}
}

func TestBuiltInRuntimeStatusExposesWorkerObservability(t *testing.T) {
	manager := newBuiltInRuntimeTestManager(t)

	manager.builtInDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (*ExecutionResult, error) {
		return &ExecutionResult{Success: true, FinalResult: "done"}, nil
	}

	if _, err := manager.DispatchTask(context.Background(), defaultAssistantAgentName, "status please"); err != nil {
		t.Fatalf("DispatchTask failed: %v", err)
	}

	status, err := manager.GetAgentStatus(defaultAssistantAgentName)
	if err != nil {
		t.Fatalf("GetAgentStatus failed: %v", err)
	}
	if status.RuntimeMode != "worker" {
		t.Fatalf("expected worker runtime mode, got %+v", status)
	}
	if status.WorkerCount != 1 {
		t.Fatalf("expected assistant worker count to stay 1, got %+v", status)
	}
	if status.ProcessedMessages == 0 {
		t.Fatalf("expected processed message count to be tracked, got %+v", status)
	}
	if status.LastMessageType != AgentMessageTypeRequest {
		t.Fatalf("expected last message type to be request, got %+v", status)
	}
	if status.LastCorrelationID == "" {
		t.Fatalf("expected last correlation id to be populated, got %+v", status)
	}

	conciergeStatus, err := manager.GetAgentStatus(defaultConciergeAgentName)
	if err != nil {
		t.Fatalf("GetAgentStatus(Concierge) failed: %v", err)
	}
	if conciergeStatus.WorkerCount < 2 {
		t.Fatalf("expected concierge worker count > 1, got %+v", conciergeStatus)
	}
}
