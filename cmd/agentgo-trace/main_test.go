package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tid = "4bf92f3577b34da6a3ce929d0e0e4736"

// Process A: the parent run, which hands work to process B over its own link.
const fileA = `{"ts":"2026-10-28T10:00:00.000Z","event":"run_start","trace_id":"` + tid + `","span_id":"aaaaaaaaaaaaaaaa","run_id":"r-parent","agent":"queen","model":"claude-opus-5-5","goal":"plan the trip","host":"core","pid":10,"elapsed_ms":0}
{"ts":"2026-10-28T10:00:00.100Z","event":"model_start","trace_id":"` + tid + `","span_id":"m1m1m1m1m1m1m1m1","parent_span_id":"aaaaaaaaaaaaaaaa","run_id":"r-parent","purpose":"turn","round":1,"elapsed_ms":100}
{"ts":"2026-10-28T10:00:00.150Z","event":"decision","trace_id":"` + tid + `","span_id":"m1m1m1m1m1m1m1m1","parent_span_id":"aaaaaaaaaaaaaaaa","run_id":"r-parent","what":"hide_mcp_search","reason":"native search proved itself","detail":{"tools":["mcp_websearch_basic"]},"elapsed_ms":150}
{"ts":"2026-10-28T10:00:02.100Z","event":"model_end","trace_id":"` + tid + `","span_id":"m1m1m1m1m1m1m1m1","parent_span_id":"aaaaaaaaaaaaaaaa","run_id":"r-parent","purpose":"turn","round":1,"duration_ms":2000,"streamed":true,"ttft_ms":1500,"tool_calls":1,"tokens":{"total":1100,"prompt":1000,"completion":100,"cached":800,"cache_write":200},"server_tools":{"uses":[{"name":"web_search","input":{"query":"lisbon weather"},"results":5}],"resumes":1,"request_ms":[1200,700],"citations":2},"elapsed_ms":2100}
{"ts":"2026-10-28T10:00:02.200Z","event":"tool_start","trace_id":"` + tid + `","span_id":"t1t1t1t1t1t1t1t1","parent_span_id":"aaaaaaaaaaaaaaaa","run_id":"r-parent","tool":"delegate","call_id":"c1","elapsed_ms":2200}
{"ts":"2026-10-28T10:00:05.200Z","event":"tool_end","trace_id":"` + tid + `","span_id":"t1t1t1t1t1t1t1t1","parent_span_id":"aaaaaaaaaaaaaaaa","run_id":"r-parent","tool":"delegate","call_id":"c1","duration_ms":3000,"remote":["00-` + tid + `-cccccccccccccccc-01"],"elapsed_ms":5200}
{"ts":"2026-10-28T10:00:05.300Z","event":"lint","trace_id":"` + tid + `","span_id":"aaaaaaaaaaaaaaaa","run_id":"r-parent","lint":"non_empty_final_answer","verdict":"retry","reason":"empty","elapsed_ms":5300}
{"ts":"2026-10-28T10:00:06.000Z","event":"run_end","trace_id":"` + tid + `","span_id":"aaaaaaaaaaaaaaaa","run_id":"r-parent","agent":"queen","outcome":"completed","stop_reason":"end_turn","duration_ms":6000,"rounds":2,"host":"core","pid":10,"summary":{"total_ms":6000,"model_ms":2000,"model_calls":1,"tool_ms":3000,"tool_calls":1,"tokens":{"prompt":1000,"completion":100,"cached":800,"cache_write":200},"by_purpose":{"turn":{"calls":1,"ms":2000,"tokens":{"prompt":1000,"completion":100,"cached":800,"cache_write":200}}},"tools":{"delegate":{"calls":1,"ms":3000}},"lint_retries":1},"elapsed_ms":6000}
`

// Process B: the worker's run (a child of A's tool call) and a run whose
// parent was written to a file not given.
const fileB = `{"ts":"2026-10-28T10:00:02.300Z","event":"run_start","trace_id":"` + tid + `","span_id":"cccccccccccccccc","parent_span_id":"t1t1t1t1t1t1t1t1","agent":"worker","host":"node-7","pid":22,"elapsed_ms":0}
{"ts":"2026-10-28T10:00:02.400Z","event":"model_start","trace_id":"` + tid + `","span_id":"m2m2m2m2m2m2m2m2","parent_span_id":"cccccccccccccccc","purpose":"turn","round":1,"elapsed_ms":100}
{"ts":"2026-10-28T10:00:04.400Z","event":"model_end","trace_id":"` + tid + `","span_id":"m2m2m2m2m2m2m2m2","parent_span_id":"cccccccccccccccc","purpose":"turn","round":1,"duration_ms":2000,"streamed":false,"tokens":{"total":550,"prompt":500,"completion":50},"elapsed_ms":2100}
{"ts":"2026-10-28T10:00:05.000Z","event":"run_end","trace_id":"` + tid + `","span_id":"cccccccccccccccc","parent_span_id":"t1t1t1t1t1t1t1t1","agent":"worker","outcome":"completed","duration_ms":2700,"rounds":1,"host":"node-7","pid":22,"summary":{"total_ms":2700,"model_calls":1,"tokens":{"prompt":500,"completion":50}},"elapsed_ms":2700}
{"ts":"2026-10-28T10:00:03.000Z","event":"run_start","trace_id":"` + tid + `","span_id":"dddddddddddddddd","parent_span_id":"eeeeeeeeeeeeeeee","agent":"helper","host":"node-7","pid":22,"elapsed_ms":0}
{"ts":"2026-10-28T10:00:03.500Z","event":"run_end","trace_id":"` + tid + `","span_id":"dddddddddddddddd","parent_span_id":"eeeeeeeeeeeeeeee","agent":"helper","outcome":"blocked","duration_ms":500,"host":"node-7","pid":22,"elapsed_ms":500}
{"ts":"2026-10-28T11:00:00.000Z","event":"run_start","trace_id":"99999999999999999999999999999999","span_id":"ffffffffffffffff","agent":"other","elapsed_ms":0}
`

func write(t *testing.T) (string, string) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	sub := filepath.Join(dir, "worker")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte(fileA), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.jsonl"), []byte(fileB+"not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return a, sub
}

func runCmd(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(args, &out); err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return out.String()
}

func TestAWholeFlowAcrossTwoFilesIsOneTree(t *testing.T) {
	a, dir := write(t)
	out := runCmd(t, a, dir, "--trace", tid)
	for _, want := range []string{
		"flow " + tid,
		"run queen +0ms  6.00s completed",
		"[core pid 10]",
		"tool     delegate",
		"→ 00-" + tid + "-cccccccccccccccc-01",
		"run worker +2.30s  2.70s completed",
		"[node-7 pid 22]",
		"(parent eeeeeeeeeeeeeeee not in these files)",
		"run helper",
		"total  wall=6.00s  runs=3  model calls=2  tool calls=1",
		"tokens in=1500 out=150 cached=800 cache_write=200",
		"ran on core pid 10, node-7 pid 22",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("flow output lacks %q:\n%s", want, out)
		}
	}
	// The worker's run sits under the tool call that handed it over.
	if strings.Index(out, "delegate") > strings.Index(out, "run worker") {
		t.Fatalf("worker not under the tool call:\n%s", out)
	}
	if strings.Contains(out, "run other") {
		t.Fatalf("another flow leaked in:\n%s", out)
	}
}

func TestOneRunsWaterfall(t *testing.T) {
	a, _ := write(t)
	out := runCmd(t, a)
	for _, want := range []string{
		"run r-parent  agent=queen",
		"model    turn              2.00s  r1  ttft=1.50s  in=1000 out=100 cached=800 cache_write=200  calls=1",
		`↳ web_search "lisbon weather"  5 results`,
		"↳ pause_turn resumes=1  requests 1.20s + 700ms  citations=2",
		"decide   hide_mcp_search: native search proved itself",
		"tool     delegate          3.00s  ok",
		"lint     non_empty_final_answer retry",
		"end  completed (end_turn)  wall=6.00s  rounds=2",
		"lint retries=1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("waterfall lacks %q:\n%s", want, out)
		}
	}
}

func TestSeveralRunsAreListed(t *testing.T) {
	a, dir := write(t)
	out := runCmd(t, a, dir)
	if !strings.Contains(out, "4 runs") || !strings.Contains(out, "worker") {
		t.Fatalf("list:\n%s", out)
	}
	if one := runCmd(t, a, dir, "--run", "cccc"); !strings.Contains(one, "agent=worker") || !strings.Contains(one, "(whole)") {
		t.Fatalf("--run by span prefix:\n%s", one)
	}
	if flows := runCmd(t, a, dir, "--traces"); !strings.Contains(flows, "2 flows") || !strings.Contains(flows, tid) {
		t.Fatalf("--traces:\n%s", flows)
	}
}
