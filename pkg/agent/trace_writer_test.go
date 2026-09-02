package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a bytes.Buffer that can be written to from the run's
// goroutines while the test reads it afterwards.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// parseTrace decodes a JSONL trace, failing the test on the first line that is
// not a JSON object. Every line must parse: a trace with one malformed line is
// a trace no consumer can stream.
func parseTrace(t *testing.T, raw string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for i, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			t.Fatalf("line %d is empty", i)
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", i, err, line)
		}
		out = append(out, obj)
	}
	return out
}

func TestTraceWriterEmitsParsableJSONLForAScriptedRun(t *testing.T) {
	buf := &syncBuffer{}
	tr := NewTraceWriter(buf)
	svc := buildObserverTestService(t, tr)
	defer svc.Close()

	events, err := svc.RunStreamWithOptions(context.Background(), "please ping", WithRunID("trace-run-1"))
	if err != nil {
		t.Fatalf("RunStream failed: %v", err)
	}
	if final := Concat(events); final != "all done" {
		t.Fatalf("final = %q", final)
	}

	lines := parseTrace(t, buf.String())
	if len(lines) == 0 {
		t.Fatal("trace is empty")
	}

	var events2 []string
	sessionID := ""
	taskID := ""
	for _, l := range lines {
		name, _ := l["event"].(string)
		if name == "" {
			t.Fatalf("line has no event name: %v", l)
		}
		events2 = append(events2, name)

		if l["ts"] == nil || l["ts"].(string) == "" {
			t.Fatalf("%s line has no ts: %v", name, l)
		}
		// Every line the runtime produces belongs to this run, and says so.
		if got, _ := l["run_id"].(string); got != "trace-run-1" {
			t.Errorf("%s line run_id = %q, want trace-run-1", name, got)
		}
		if s, _ := l["session_id"].(string); s != "" {
			if sessionID == "" {
				sessionID = s
			} else if s != sessionID {
				t.Errorf("%s line session_id = %q, want %q", name, s, sessionID)
			}
		}
		if s, _ := l["task_id"].(string); s != "" {
			if taskID == "" {
				taskID = s
			} else if s != taskID {
				t.Errorf("%s line task_id = %q, want %q", name, s, taskID)
			}
		}
	}
	if sessionID == "" || taskID == "" {
		t.Fatalf("trace never named its session/task: %q %q", sessionID, taskID)
	}

	// The scripted run is: model turn asks for ping, ping runs (dispatched
	// mid-stream, so tool_end precedes model_end), a round-end checkpoint,
	// a second model turn that answers, and the terminal checkpoint. The
	// trace has to say exactly that, in that order.
	want := []string{"model_start", "tool_start", "tool_end", "model_end", "checkpoint", "model_start", "model_end", "checkpoint"}
	if len(events2) != len(want) {
		t.Fatalf("event sequence = %v, want %v", events2, want)
	}
	for i := range want {
		if events2[i] != want[i] {
			t.Fatalf("event sequence = %v, want %v", events2, want)
		}
	}

	// Spot-check the per-event fields the human-readable sibling also prints,
	// so the two cannot drift apart unnoticed.
	byEvent := map[string]map[string]any{}
	for _, l := range lines {
		byEvent[l["event"].(string)] = l
	}
	if got := byEvent["model_start"]["messages"]; got == nil {
		t.Errorf("model_start carries no message count")
	}
	if got, _ := byEvent["tool_start"]["tool"].(string); got != "ping" {
		t.Errorf("tool_start tool = %v", byEvent["tool_start"]["tool"])
	}
	if args, ok := byEvent["tool_start"]["args"].(map[string]any); !ok || args["msg"] != "hi" {
		t.Errorf("tool_start args = %v", byEvent["tool_start"]["args"])
	}
	startCall, _ := byEvent["tool_start"]["call_id"].(string)
	endCall, _ := byEvent["tool_end"]["call_id"].(string)
	if startCall == "" || startCall != endCall {
		t.Errorf("tool call not paired by call_id: start=%q end=%q", startCall, endCall)
	}
	if res, _ := byEvent["tool_end"]["result"].(string); !strings.Contains(res, "\"echo\":\"hi\"") {
		t.Errorf("tool_end result = %v", byEvent["tool_end"]["result"])
	}
	if byEvent["tool_end"]["error"] != nil {
		t.Errorf("tool_end reported an error: %v", byEvent["tool_end"]["error"])
	}
	var checkpointReasons []string
	for _, l := range lines {
		if l["event"] == "checkpoint" {
			reason, _ := l["checkpoint_reason"].(string)
			if reason == "" {
				t.Errorf("checkpoint line has no reason: %v", l)
			}
			checkpointReasons = append(checkpointReasons, reason)
		}
	}
	if len(checkpointReasons) != 2 || checkpointReasons[0] == checkpointReasons[1] {
		t.Errorf("checkpoint reasons = %v, want a round-end then a terminal one", checkpointReasons)
	}
}

// TestTraceWriterSkipsDeltasByDefault keeps the trace off the token firehose
// unless the caller asked for it.
func TestTraceWriterSkipsDeltasByDefault(t *testing.T) {
	buf := &syncBuffer{}
	tr := NewTraceWriter(buf)
	tr.OnModelDelta(context.Background(), ModelDelta{SpanID: "s", Kind: "reasoning", Text: "thinking"})
	if buf.String() != "" {
		t.Fatalf("delta was written by default: %q", buf.String())
	}

	tr.IncludeDeltas = true
	tr.OnModelDelta(context.Background(), ModelDelta{SpanID: "s", Kind: "reasoning", Text: "thinking"})
	lines := parseTrace(t, buf.String())
	if len(lines) != 1 || lines[0]["event"] != "model_delta" || lines[0]["delta"] != "thinking" {
		t.Fatalf("delta line = %v", lines)
	}
}

// TestTraceWriterConcurrentLinesStayWhole is the property a JSONL consumer
// depends on: a run executes tools in parallel, and a torn line is not
// recoverable.
func TestTraceWriterConcurrentLinesStayWhole(t *testing.T) {
	buf := &syncBuffer{}
	tr := NewTraceWriter(buf)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			info := ToolInfo{
				RunID:     "r",
				SessionID: "s",
				Tool:      "concurrent_tool",
				CallID:    string(rune('a' + i%26)),
				Args:      map[string]any{"payload": strings.Repeat("x", 200)},
			}
			tr.OnToolStart(context.Background(), info)
			tr.OnToolEnd(context.Background(), info, strings.Repeat("y", 200), nil)
		}(i)
	}
	wg.Wait()

	lines := parseTrace(t, buf.String())
	if len(lines) != 64 {
		t.Fatalf("got %d lines, want 64", len(lines))
	}
}

// TestTraceWriterClipsLongText keeps a file write from ending up in the trace
// verbatim.
func TestTraceWriterClipsLongText(t *testing.T) {
	buf := &syncBuffer{}
	tr := NewTraceWriter(buf)
	tr.MaxTextLen = 16
	info := ToolInfo{Tool: "fs_write", CallID: "c1"}
	tr.OnToolEnd(context.Background(), info, strings.Repeat("z", 4096), nil)

	lines := parseTrace(t, buf.String())
	res, _ := lines[0]["result"].(string)
	if len([]rune(res)) != 17 || !strings.HasSuffix(res, "…") {
		t.Fatalf("result was not clipped: %d runes", len([]rune(res)))
	}
}
