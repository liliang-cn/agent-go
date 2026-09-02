package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// Every line the loop logs must say which run wrote it. A host that collects
// the framework's logs alongside its own can otherwise not tell two
// concurrent runs apart, and "the run that failed" is exactly the one they
// need to find.
func TestLoopLogsCarryTheRunIDs(t *testing.T) {
	var buf bytes.Buffer
	prev := agentgolog.GetLogger()
	agentgolog.SetLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer agentgolog.SetLogger(prev)

	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "done"}}}
	svc, err := New("logged").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	events, err := svc.RunStreamWithOptions(context.Background(), "log me",
		WithRunID("run-log-test"), WithSessionID("session-log-test"), WithTaskID("task-log-test"))
	if err != nil {
		t.Fatal(err)
	}
	collectStreamContent(t, events)

	var started, ended bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		switch rec["msg"] {
		case "run started", "run ended":
			for _, k := range []string{"run_id", "session_id", "task_id"} {
				if rec[k] == "" || rec[k] == nil {
					t.Fatalf("%q line lacks %s: %s", rec["msg"], k, line)
				}
			}
			if rec["run_id"] != "run-log-test" || rec["session_id"] != "session-log-test" || rec["task_id"] != "task-log-test" {
				t.Fatalf("ids do not match the run's: %s", line)
			}
			if rec["msg"] == "run started" {
				started = true
			} else {
				ended = true
				if rec["stop_reason"] != string(StopReasonEndTurn) {
					t.Fatalf("stop_reason = %v", rec["stop_reason"])
				}
			}
		}
	}
	if !started || !ended {
		t.Fatalf("expected both run started and run ended lines, got started=%v ended=%v:\n%s", started, ended, buf.String())
	}
}
