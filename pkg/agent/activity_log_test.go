package agent

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The point of the log is that a run in flight stops being invisible. A soak
// run spent half an hour doing something none of the observable state could
// name; this is what would have answered it in one line.
func TestActivityLogNarratesARun(t *testing.T) {
	var buf bytes.Buffer
	llm := &toolLoopLLM{}

	svc, err := New("activity-log").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithObserver(NewActivityLog(&buf)).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	var ran int32
	svc.AddTool("noop", "Does nothing.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"n": map[string]interface{}{"type": "number"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			atomic.AddInt32(&ran, 1)
			return map[string]interface{}{"ok": true}, nil
		})

	events, err := svc.RunStreamWithOptions(context.Background(), "Work.", WithMaxTurns(3))
	if err != nil {
		t.Fatalf("RunStreamWithOptions: %v", err)
	}
	for range events {
	}

	out := buf.String()
	if out == "" {
		t.Fatal("the log is empty; a run happened and narrated none of it")
	}
	for _, want := range []string{"model>", "model<", "tool>", "tool<", "noop"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log never mentions %q:\n%s", want, out)
		}
	}
	// Every line has to carry a clock and an elapsed column, or a long log
	// cannot answer "when did it last do anything".
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if len(line) < 8 || line[2] != ':' || line[5] != ':' {
			t.Errorf("line has no wall clock: %q", line)
		}
	}
}

// A run executes tools in parallel, so the log has to survive that.
func TestActivityLogIsConcurrencySafe(t *testing.T) {
	var buf bytes.Buffer
	log := NewActivityLog(&buf)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 40; j++ {
				id := fmt.Sprintf("call-%d-%d", i, j)
				log.OnToolStart(context.Background(), ToolInfo{Tool: "t", CallID: id,
					Args: map[string]any{"a": 1, "b": "two"}})
				log.OnToolEnd(context.Background(), ToolInfo{Tool: "t", CallID: id}, "ok", nil)
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if n := strings.Count(buf.String(), "\n"); n != 8*40*2 {
		t.Errorf("wrote %d lines, want %d", n, 8*40*2)
	}
}

// Arguments come out of a Go map, which iterates randomly. A log whose lines
// differ only by field order cannot be diffed or eyeballed for repetition —
// which is exactly what you use it for.
func TestActivityLogArgsAreStablyOrdered(t *testing.T) {
	t.Parallel()
	log := NewActivityLog(nil)
	args := map[string]any{"zebra": 1, "alpha": 2, "middle": 3}
	first := log.formatArgs(args)
	for i := 0; i < 20; i++ {
		if got := log.formatArgs(args); got != first {
			t.Fatalf("arg order drifted: %q vs %q", got, first)
		}
	}
	if !strings.HasPrefix(first, "alpha=") {
		t.Errorf("args should be sorted, got %q", first)
	}
}

// A file write carries the whole file. Logging it buries the line it belongs to.
func TestActivityLogTruncatesHugeArguments(t *testing.T) {
	t.Parallel()
	log := NewActivityLog(nil)
	out := log.formatArgs(map[string]any{"content": strings.Repeat("x", 5000)})
	if len(out) > log.MaxArgLen+40 {
		t.Errorf("a 5000-byte argument produced a %d-char line", len(out))
	}
	if !strings.HasSuffix(out, "…") {
		t.Errorf("a truncated value should say so: %q", out)
	}
}

// The log is meant to be grepped and awked. A multi-byte character in it makes
// awk fail under a C locale, which is exactly where a log-processing script
// tends to run — this bit me on the first log I tried to summarise.
func TestActivityLogIsASCIISafe(t *testing.T) {
	t.Parallel()
	out := oneLine("first line\nsecond line\r\nthird", 0)
	for i, r := range out {
		if r > 127 {
			t.Fatalf("non-ASCII rune %q at %d in %q", r, i, out)
		}
	}
	if !strings.Contains(out, `\n`) {
		t.Errorf("newlines should survive as an escape, got %q", out)
	}
	if strings.Contains(out, "\n") {
		t.Errorf("a real newline leaked into a one-line rendering: %q", out)
	}
}

func TestShortDuration(t *testing.T) {
	t.Parallel()
	cases := map[time.Duration]string{
		250 * time.Millisecond:               "250ms",
		7*time.Second + 200*time.Millisecond: "7.2s",
		3*time.Minute + 5*time.Second:        "3m05s",
		2*time.Hour + 7*time.Minute:          "2h07m",
	}
	for in, want := range cases {
		if got := shortDuration(in); got != want {
			t.Errorf("shortDuration(%s) = %q, want %q", in, got, want)
		}
	}
}
