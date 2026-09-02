// Package main shows the two ways to watch a run you cannot sit and watch.
//
// A run in flight is nearly invisible: its conversation reaches the store only
// when it ends, its events go to whoever called RunStream, and the framework
// logs nothing of its own accord. Two Observers close that, and they are
// siblings on purpose — same seams, same fields, different readers.
//
//   - agent.NewActivityLog(w) writes one flat, greppable line per model turn,
//     tool call, sub-agent and checkpoint. It is for a person at a terminal
//     with grep and awk.
//   - agent.NewTraceWriter(w) writes the same run as JSONL, one JSON object
//     per line, each carrying ts plus whichever of task_id / run_id /
//     session_id / agent / round / span_id / call_id apply. It is for a
//     program: an aggregator, a notebook, a regression harness asking whether
//     this build got slower and where.
//
// Attach either (or both) to anything long-running. Deltas are off by default
// on the trace — a reasoning model emits thousands per turn — and every
// free-text field is clipped, so a file write does not end up in the trace
// verbatim.
//
// Usage:
//
//	go run ./examples/trace
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dir, err := os.MkdirTemp("", "agentgo-trace-*")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	tracePath := filepath.Join(dir, "run.jsonl")
	traceFile, err := os.Create(tracePath)
	if err != nil {
		log.Fatalf("create trace: %v", err)
	}
	defer traceFile.Close()

	trace := agent.NewTraceWriter(traceFile)
	// trace.IncludeDeltas = true  // one line per streamed fragment; noisy
	// trace.MaxTextLen = 2000     // how much of a result or error is kept

	svc, err := agent.New("trace-demo").
		WithObserver(trace).
		WithObserver(agent.NewActivityLog(os.Stdout)). // the human-readable sibling
		Build()
	if err != nil {
		log.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	// WithRunID names the run, so every trace line — and CancelRun — refer to
	// it by the same id. Without it the runtime mints one and the trace still
	// carries it; naming it is what lets a caller correlate its own records.
	result, err := svc.Run(ctx, "In one sentence, say what a JSONL trace is for.",
		agent.WithRunID("trace-demo-run"))
	if err != nil {
		log.Fatalf("run failed: %v", err)
	}
	fmt.Printf("\nanswer: %s\n\n", result.Text())

	if err := traceFile.Sync(); err != nil {
		log.Fatalf("sync: %v", err)
	}
	raw, err := os.ReadFile(tracePath)
	if err != nil {
		log.Fatalf("read trace: %v", err)
	}

	fmt.Println("=== run.jsonl ===")
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			log.Fatalf("trace line is not JSON: %v", err)
		}
		fmt.Printf("%-14s run=%v round=%v %s\n",
			obj["event"], obj["run_id"], obj["round"], line)
	}
}
