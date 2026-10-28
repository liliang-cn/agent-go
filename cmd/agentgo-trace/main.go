// Command agentgo-trace reads the JSONL traces agent.TraceWriter writes and
// prints what a run did, why, and where its time and tokens went — without a
// person lining up timestamps by hand.
//
//	agentgo-trace run.jsonl                  one run: its waterfall and totals;
//	                                         several: a list of them
//	agentgo-trace run.jsonl --run <id>       one run's waterfall (run id or span id prefix)
//	agentgo-trace a.jsonl dir/ --trace <id>  the whole flow as one tree, across
//	                                         every file given (directories are
//	                                         searched for *.jsonl)
//	agentgo-trace dir/ --traces              list the flows
//
// A flow is everything one request fanned out into — sub-agents, segments,
// resumes, workers in other processes — joined by trace_id and span parent
// links (docs/trace-schema.md). A run whose parent is not in the files is
// shown as a dangling branch under the id it names, not dropped.
//
// Standard library only; output is plain text.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "agentgo-trace:", err)
		os.Exit(1)
	}
}

const usage = `usage: agentgo-trace <file|dir>... [--run <id>] [--trace <id>] [--traces]`

func run(args []string, out io.Writer) error {
	var paths []string
	var runID, traceID string
	listTraces := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--run" || a == "-run" || a == "--trace" || a == "-trace":
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs an id\n%s", a, usage)
			}
			if strings.HasSuffix(a, "run") {
				runID = args[i+1]
			} else {
				traceID = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "--run="):
			runID = strings.TrimPrefix(a, "--run=")
		case strings.HasPrefix(a, "--trace="):
			traceID = strings.TrimPrefix(a, "--trace=")
		case a == "--traces" || a == "-traces":
			listTraces = true
		case a == "-h" || a == "--help":
			fmt.Fprintln(out, usage)
			return nil
		default:
			paths = append(paths, a)
		}
	}
	if len(paths) == 0 {
		return fmt.Errorf("no trace files\n%s", usage)
	}
	lines, err := load(paths)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return fmt.Errorf("no trace lines in %s", strings.Join(paths, ", "))
	}
	f := build(lines)
	switch {
	case traceID != "":
		return f.printFlow(out, traceID)
	case listTraces:
		f.printTraces(out)
		return nil
	case runID != "":
		r := f.findRun(runID)
		if r == nil {
			return fmt.Errorf("no run %q", runID)
		}
		f.printRun(out, r)
		return nil
	case len(f.runs) == 1:
		f.printRun(out, f.runs[0])
		return nil
	default:
		f.printRuns(out)
		return nil
	}
}

// line is the part of a trace line this reader uses.
type line struct {
	TS           time.Time       `json:"-"`
	RawTS        string          `json:"ts"`
	Event        string          `json:"event"`
	TraceID      string          `json:"trace_id"`
	SpanID       string          `json:"span_id"`
	ParentSpanID string          `json:"parent_span_id"`
	TaskID       string          `json:"task_id"`
	RunID        string          `json:"run_id"`
	SessionID    string          `json:"session_id"`
	Agent        string          `json:"agent"`
	Round        int             `json:"round"`
	CallID       string          `json:"call_id"`
	DurationMs   int64           `json:"duration_ms"`
	Model        string          `json:"model"`
	Purpose      string          `json:"purpose"`
	Streamed     *bool           `json:"streamed"`
	TTFTMs       int64           `json:"ttft_ms"`
	Messages     int             `json:"messages"`
	Tools        int             `json:"tools"`
	ToolCalls    int             `json:"tool_calls"`
	Tokens       *tokens         `json:"tokens"`
	ServerTools  *serverTools    `json:"server_tools"`
	Tool         string          `json:"tool"`
	Inner        bool            `json:"inner"`
	Remote       []string        `json:"remote"`
	Error        string          `json:"error"`
	Goal         string          `json:"goal"`
	Host         string          `json:"host"`
	PID          int             `json:"pid"`
	Outcome      string          `json:"outcome"`
	StopReason   string          `json:"stop_reason"`
	Rounds       int             `json:"rounds"`
	Summary      *summary        `json:"summary"`
	What         string          `json:"what"`
	Reason       string          `json:"reason"`
	Detail       json.RawMessage `json:"detail"`
	Lint         string          `json:"lint"`
	Verdict      string          `json:"verdict"`
	Kind         string          `json:"kind"`
	Attempt      int             `json:"attempt"`
	Trigger      string          `json:"trigger"`
	Mode         string          `json:"mode"`
	Checkpoint   string          `json:"checkpoint_reason"`
	SegmentIndex *int            `json:"segment_index"`
	Marker       string          `json:"marker"`
	Message      string          `json:"message"`
	file         string
}

type tokens struct {
	Total      int `json:"total"`
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Cached     int `json:"cached"`
	CacheWrite int `json:"cache_write"`
}

func (t *tokens) add(o *tokens) {
	if o == nil {
		return
	}
	t.Prompt += o.Prompt
	t.Completion += o.Completion
	t.Cached += o.Cached
	t.CacheWrite += o.CacheWrite
}

func (t tokens) String() string {
	s := fmt.Sprintf("in=%d out=%d", t.Prompt, t.Completion)
	if t.Cached > 0 {
		s += fmt.Sprintf(" cached=%d", t.Cached)
	}
	if t.CacheWrite > 0 {
		s += fmt.Sprintf(" cache_write=%d", t.CacheWrite)
	}
	return s
}

type serverTools struct {
	Uses []struct {
		Name      string         `json:"name"`
		Input     map[string]any `json:"input"`
		Results   int            `json:"results"`
		ErrorCode string         `json:"error_code"`
	} `json:"uses"`
	Resumes   int     `json:"resumes"`
	RequestMs []int64 `json:"request_ms"`
	Citations int     `json:"citations"`
}

type purpose struct {
	Calls  int    `json:"calls"`
	Ms     int64  `json:"ms"`
	Tokens tokens `json:"tokens"`
}

type toolSum struct {
	Calls    int   `json:"calls"`
	Failures int   `json:"failures"`
	Ms       int64 `json:"ms"`
}

type summary struct {
	TotalMs            int64              `json:"total_ms"`
	FirstModelCallMs   int64              `json:"first_model_call_ms"`
	FirstAnswerTokenMs int64              `json:"first_answer_token_ms"`
	ModelMs            int64              `json:"model_ms"`
	ModelCalls         int                `json:"model_calls"`
	MemoryMs           int64              `json:"memory_ms"`
	ToolMs             int64              `json:"tool_ms"`
	ToolCalls          int                `json:"tool_calls"`
	ToolFailures       int                `json:"tool_failures"`
	Tokens             *tokens            `json:"tokens"`
	ByPurpose          map[string]purpose `json:"by_purpose"`
	Tools              map[string]toolSum `json:"tools"`
	LintRetries        int                `json:"lint_retries"`
	LintBlocks         int                `json:"lint_blocks"`
	Compactions        int                `json:"compactions"`
}

func load(paths []string) ([]*line, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".ndjson")) {
				files = append(files, path)
			}
			return nil
		})
	}
	var out []*line
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			raw := strings.TrimSpace(sc.Text())
			if raw == "" || raw[0] != '{' {
				continue
			}
			var l line
			if json.Unmarshal([]byte(raw), &l) != nil || l.Event == "" {
				continue // not a TraceWriter line (a pkg/trace record has "kind", not "event")
			}
			l.TS, _ = time.Parse(time.RFC3339Nano, l.RawTS)
			l.file = path
			out = append(out, &l)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out, nil
}

// node is one span: a run, a model call, a tool call, or a long task's span.
type node struct {
	kind              string // run, model, tool, task
	span, parent      string
	trace             string
	start, end        *line
	lines             []*line // events about this span (decisions, lints, checkpoints...)
	children          []*node
	runID, agent      string
	host              string
	pid               int
	file              string
	firstTS           time.Time
	remote            []string
	segmentsSeen      int
	syntheticDuration int64
}

func (n *node) at() time.Time {
	if n.start != nil {
		return n.start.TS
	}
	return n.firstTS
}

func (n *node) durMs() int64 {
	if n.end != nil && n.end.DurationMs > 0 {
		return n.end.DurationMs
	}
	if n.start != nil && n.end != nil {
		return n.end.TS.Sub(n.start.TS).Milliseconds()
	}
	return n.syntheticDuration
}

type flows struct {
	nodes map[string]*node
	runs  []*node
	lines []*line
}

func build(lines []*line) *flows {
	f := &flows{nodes: map[string]*node{}, lines: lines}
	get := func(l *line, kind string) *node {
		key := l.SpanID
		if key == "" {
			// A trace written before flows were traced: one run per run_id.
			key = "run:" + l.RunID + ":" + l.SessionID
			kind = "run"
		}
		n := f.nodes[key]
		if n == nil {
			n = &node{kind: kind, span: l.SpanID, parent: l.ParentSpanID, trace: l.TraceID, firstTS: l.TS, file: l.file}
			f.nodes[key] = n
		}
		if n.kind == "" || (kind == "run" && n.kind != "run") {
			n.kind = kind
		}
		if n.parent == "" {
			n.parent = l.ParentSpanID
		}
		if n.trace == "" {
			n.trace = l.TraceID
		}
		if l.RunID != "" && n.runID == "" {
			n.runID = l.RunID
		}
		if l.Agent != "" && n.agent == "" {
			n.agent = l.Agent
		}
		return n
	}
	for _, l := range lines {
		switch l.Event {
		case "run_start":
			n := get(l, "run")
			n.start, n.host, n.pid = l, l.Host, l.PID
		case "run_end":
			n := get(l, "run")
			n.end = l
			if n.host == "" {
				n.host, n.pid = l.Host, l.PID
			}
		case "model_start":
			get(l, "model").start = l
		case "model_end":
			get(l, "model").end = l
		case "tool_start":
			get(l, "tool").start = l
		case "tool_end":
			n := get(l, "tool")
			n.end = l
			n.remote = l.Remote
		case "segment_start", "segment_end":
			n := get(l, "task")
			n.lines = append(n.lines, l)
			if l.Event == "segment_end" {
				n.segmentsSeen++
				n.syntheticDuration = l.TS.Sub(n.firstTS).Milliseconds()
			}
		case "model_delta", "model_wire", "resource", "permission", "step":
			// Too fine for the waterfall; still in the file.
		default:
			if l.SpanID == "" && l.RunID == "" {
				continue
			}
			n := get(l, "")
			n.lines = append(n.lines, l)
		}
	}
	// Spans whose only lines were events (a decision made inside a model
	// call already has the model's node; anything else becomes a run).
	for _, n := range f.nodes {
		if n.kind == "" {
			n.kind = "run"
		}
		if n.kind == "run" {
			f.runs = append(f.runs, n)
		}
	}
	for _, n := range f.nodes {
		if p := f.nodes[n.parent]; p != nil && n.parent != "" {
			p.children = append(p.children, n)
		}
	}
	for _, n := range f.nodes {
		sort.SliceStable(n.children, func(i, j int) bool { return n.children[i].at().Before(n.children[j].at()) })
	}
	sort.SliceStable(f.runs, func(i, j int) bool { return f.runs[i].at().Before(f.runs[j].at()) })
	return f
}

func (f *flows) findRun(id string) *node {
	for _, r := range f.runs {
		if r.runID == id || (r.span != "" && strings.HasPrefix(r.span, id)) {
			return r
		}
	}
	return nil
}

func ms(v int64) string {
	switch {
	case v < 1000:
		return fmt.Sprintf("%dms", v)
	case v < 60_000:
		return fmt.Sprintf("%.2fs", float64(v)/1000)
	default:
		return fmt.Sprintf("%dm%02ds", v/60_000, (v%60_000)/1000)
	}
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (f *flows) printRuns(out io.Writer) {
	fmt.Fprintf(out, "%d runs (agentgo-trace <file> --run <id> for one, --traces for flows)\n\n", len(f.runs))
	fmt.Fprintf(out, "  %-16s %-12s %-16s %-24s %9s  %-10s %s\n", "SPAN", "TRACE", "RUN", "AGENT", "WALL", "OUTCOME", "TOKENS")
	for _, r := range f.runs {
		fmt.Fprintf(out, "  %-16s %-12s %-16s %-24s %9s  %-10s %s\n", nz(r.span), short(nz(r.trace), 12), short(nz(r.runID), 16),
			short(nz(r.agent), 24), ms(r.durMs()), nz(outcome(r)), runTokens(f, r))
	}
}

func nz(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func outcome(r *node) string {
	if r.end == nil {
		return "(open)"
	}
	return r.end.Outcome
}

func runTokens(f *flows, r *node) string {
	if r.end != nil && r.end.Summary != nil && r.end.Summary.Tokens != nil {
		return r.end.Summary.Tokens.String()
	}
	var t tokens
	for _, c := range r.children {
		if c.kind == "model" && c.end != nil {
			t.add(c.end.Tokens)
		}
	}
	return t.String()
}

// printRun is one run's waterfall: every model and tool call, offset from
// the run's start, and the decisions, lints and checkpoints between them.
func (f *flows) printRun(out io.Writer, r *node) {
	t0 := r.at()
	fmt.Fprintf(out, "run %s  agent=%s", nz(r.runID), nz(r.agent))
	if r.span != "" {
		fmt.Fprintf(out, "  trace=%s span=%s", r.trace, r.span)
		if r.parent != "" {
			fmt.Fprintf(out, " parent=%s", r.parent)
		}
	}
	fmt.Fprintln(out)
	if r.host != "" {
		fmt.Fprintf(out, "  on %s pid %d\n", r.host, r.pid)
	}
	if r.start != nil {
		if r.start.Model != "" {
			fmt.Fprintf(out, "  model %s\n", r.start.Model)
		}
		if r.start.Goal != "" {
			fmt.Fprintf(out, "  goal  %s\n", short(r.start.Goal, 100))
		}
	}
	fmt.Fprintln(out)

	type row struct {
		at   time.Time
		text string
	}
	var rows []row
	add := func(at time.Time, text string) { rows = append(rows, row{at, text}) }
	off := func(t time.Time) string { return fmt.Sprintf("+%8s", ms(t.Sub(t0).Milliseconds())) }
	for _, c := range r.children {
		switch c.kind {
		case "model":
			add(c.at(), modelRow(off(c.at()), c))
			if c.end != nil && c.end.ServerTools != nil {
				for _, s := range serverRows(c.end.ServerTools) {
					add(c.at(), "            "+s)
				}
			}
			for _, l := range c.lines {
				add(l.TS, eventRow(off(l.TS), l))
			}
		case "tool":
			add(c.at(), toolRow(off(c.at()), c))
			for _, cc := range c.children {
				if cc.kind == "run" {
					add(cc.at(), fmt.Sprintf("%s    sub-run %-12s %8s  %s span=%s", off(cc.at()), short(nz(cc.agent), 12), ms(cc.durMs()), outcome(cc), cc.span))
				}
			}
		case "run":
			add(c.at(), fmt.Sprintf("%s  sub-run  %-12s %8s  %s span=%s", off(c.at()), short(nz(c.agent), 12), ms(c.durMs()), outcome(c), c.span))
		}
	}
	for _, l := range r.lines {
		add(l.TS, eventRow(off(l.TS), l))
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) })
	for _, rw := range rows {
		fmt.Fprintln(out, rw.text)
	}
	fmt.Fprintln(out)
	printSummary(out, r)
}

func modelRow(at string, c *node) string {
	l := c.start
	if l == nil {
		l = c.end
	}
	b := fmt.Sprintf("%s  model    %-14s", at, l.Purpose)
	if c.end == nil {
		return b + " (no end)"
	}
	e := c.end
	b += fmt.Sprintf(" %8s", ms(c.durMs()))
	if l.Round > 0 {
		b += fmt.Sprintf("  r%d", l.Round)
	}
	if e.Streamed != nil && *e.Streamed && e.TTFTMs > 0 {
		b += "  ttft=" + ms(e.TTFTMs)
	} else if e.Streamed != nil && !*e.Streamed {
		b += "  (whole)"
	}
	if e.Tokens != nil {
		b += "  " + e.Tokens.String()
	}
	if e.ToolCalls > 0 {
		b += fmt.Sprintf("  calls=%d", e.ToolCalls)
	}
	if e.Error != "" {
		b += "  ERROR " + short(e.Error, 80)
	}
	return b
}

func serverRows(s *serverTools) []string {
	var out []string
	for _, u := range s.Uses {
		q := ""
		for _, k := range []string{"query", "url", "q"} {
			if v, ok := u.Input[k].(string); ok {
				q = fmt.Sprintf(" %q", short(v, 50))
				break
			}
		}
		if u.ErrorCode != "" {
			out = append(out, fmt.Sprintf("↳ %s%s  error=%s", u.Name, q, u.ErrorCode))
		} else {
			out = append(out, fmt.Sprintf("↳ %s%s  %d results", u.Name, q, u.Results))
		}
	}
	var tail []string
	if s.Resumes > 0 {
		tail = append(tail, fmt.Sprintf("pause_turn resumes=%d", s.Resumes))
	}
	if len(s.RequestMs) > 1 {
		var parts []string
		for _, v := range s.RequestMs {
			parts = append(parts, ms(v))
		}
		tail = append(tail, "requests "+strings.Join(parts, " + "))
	}
	if s.Citations > 0 {
		tail = append(tail, fmt.Sprintf("citations=%d", s.Citations))
	}
	if len(tail) > 0 {
		out = append(out, "↳ "+strings.Join(tail, "  "))
	}
	return out
}

func toolRow(at string, c *node) string {
	l := c.start
	if l == nil {
		l = c.end
	}
	b := fmt.Sprintf("%s  tool     %-14s", at, short(l.Tool, 14))
	if c.end == nil {
		return b + " (no end)"
	}
	b += fmt.Sprintf(" %8s", ms(c.durMs()))
	if c.end.Error != "" {
		b += "  FAILED " + short(c.end.Error, 80)
	} else {
		b += "  ok"
	}
	for _, rm := range c.remote {
		b += "  → " + rm
	}
	return b
}

func eventRow(at string, l *line) string {
	switch l.Event {
	case "decision":
		d := ""
		if len(l.Detail) > 0 && string(l.Detail) != "null" {
			d = " " + short(string(l.Detail), 80)
		}
		return fmt.Sprintf("%s  decide   %s: %s%s", at, l.What, short(l.Reason, 90), d)
	case "lint":
		return fmt.Sprintf("%s  lint     %s %s: %s", at, l.Lint, l.Verdict, short(l.Reason, 80))
	case "model_retry":
		return fmt.Sprintf("%s  retry    %s attempt %d: %s", at, l.Kind, l.Attempt, short(l.Reason, 80))
	case "compaction":
		return fmt.Sprintf("%s  compact  %s mode=%s", at, l.Trigger, l.Mode)
	case "checkpoint":
		return fmt.Sprintf("%s  ckpt     %s", at, l.Checkpoint)
	case "error":
		return fmt.Sprintf("%s  error    %s %s", at, l.Marker, short(l.Message, 90))
	case "subagent_start", "subagent_end":
		return fmt.Sprintf("%s  %s", at, l.Event)
	}
	return fmt.Sprintf("%s  %s", at, l.Event)
}

func printSummary(out io.Writer, r *node) {
	if r.end == nil {
		fmt.Fprintln(out, "(no run_end: the run did not finish in these files)")
		return
	}
	e := r.end
	fmt.Fprintf(out, "end  %s", nz(e.Outcome))
	if e.StopReason != "" {
		fmt.Fprintf(out, " (%s)", e.StopReason)
	}
	fmt.Fprintf(out, "  wall=%s  rounds=%d\n", ms(e.DurationMs), e.Rounds)
	s := e.Summary
	if s == nil {
		return
	}
	fmt.Fprintf(out, "     model %s in %d calls", ms(s.ModelMs), s.ModelCalls)
	if s.FirstAnswerTokenMs > 0 {
		fmt.Fprintf(out, ", first answer token at %s", ms(s.FirstAnswerTokenMs))
	}
	fmt.Fprintf(out, "; tools %s in %d calls", ms(s.ToolMs), s.ToolCalls)
	if s.ToolFailures > 0 {
		fmt.Fprintf(out, " (%d failed)", s.ToolFailures)
	}
	if s.MemoryMs > 0 {
		fmt.Fprintf(out, "; memory %s", ms(s.MemoryMs))
	}
	fmt.Fprintln(out)
	if s.Tokens != nil {
		fmt.Fprintf(out, "     tokens %s\n", s.Tokens.String())
	}
	for _, k := range sortedKeys(s.ByPurpose) {
		p := s.ByPurpose[k]
		fmt.Fprintf(out, "       %-16s %3d calls %9s  %s\n", k, p.Calls, ms(p.Ms), p.Tokens.String())
	}
	for _, k := range sortedKeys(s.Tools) {
		t := s.Tools[k]
		fail := ""
		if t.Failures > 0 {
			fail = fmt.Sprintf("  %d failed", t.Failures)
		}
		fmt.Fprintf(out, "     tool %-16s %3d calls %9s%s\n", k, t.Calls, ms(t.Ms), fail)
	}
	if s.LintRetries+s.LintBlocks+s.Compactions > 0 {
		fmt.Fprintf(out, "     lint retries=%d blocks=%d  compactions=%d\n", s.LintRetries, s.LintBlocks, s.Compactions)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (f *flows) printTraces(out io.Writer) {
	type agg struct {
		id         string
		first      time.Time
		runs       int
		files      map[string]bool
		procs      map[string]bool
		start, end time.Time
	}
	byTrace := map[string]*agg{}
	for _, l := range f.lines {
		if l.TraceID == "" {
			continue
		}
		a := byTrace[l.TraceID]
		if a == nil {
			a = &agg{id: l.TraceID, start: l.TS, files: map[string]bool{}, procs: map[string]bool{}}
			byTrace[l.TraceID] = a
		}
		if l.Event == "run_start" {
			a.runs++
			a.procs[fmt.Sprintf("%s:%d", l.Host, l.PID)] = true
		}
		a.files[l.file] = true
		if l.TS.After(a.end) {
			a.end = l.TS
		}
	}
	list := make([]*agg, 0, len(byTrace))
	for _, a := range byTrace {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].start.Before(list[j].start) })
	fmt.Fprintf(out, "%d flows\n\n  %-32s %-19s %9s %5s %6s %6s\n", len(list), "TRACE", "STARTED", "WALL", "RUNS", "PROCS", "FILES")
	for _, a := range list {
		fmt.Fprintf(out, "  %-32s %-19s %9s %5d %6d %6d\n", a.id, a.start.Format("2006-01-02 15:04:05"),
			ms(a.end.Sub(a.start).Milliseconds()), a.runs, len(a.procs), len(a.files))
	}
}

// printFlow prints every span of one trace as a tree in time order.
func (f *flows) printFlow(out io.Writer, traceID string) error {
	var members []*node
	for _, n := range f.nodes {
		if n.trace == traceID || (len(traceID) >= 8 && strings.HasPrefix(n.trace, traceID)) {
			members = append(members, n)
		}
	}
	if len(members) == 0 {
		return fmt.Errorf("no spans of trace %q", traceID)
	}
	full := members[0].trace
	in := map[string]bool{}
	for _, n := range members {
		in[n.span] = true
	}
	// Roots: a span with no parent, or whose parent is not in the files.
	dangling := map[string][]*node{}
	var roots []*node
	for _, n := range members {
		switch {
		case n.parent == "":
			roots = append(roots, n)
		case !in[n.parent]:
			dangling[n.parent] = append(dangling[n.parent], n)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].at().Before(roots[j].at()) })

	var first, last time.Time
	var tok tokens
	runs, models, tools, failures := 0, 0, 0, 0
	procs := map[string]bool{}
	for _, n := range members {
		if first.IsZero() || n.at().Before(first) {
			first = n.at()
		}
		end := n.at().Add(time.Duration(n.durMs()) * time.Millisecond)
		if end.After(last) {
			last = end
		}
		switch n.kind {
		case "run":
			runs++
			if n.host != "" {
				procs[fmt.Sprintf("%s pid %d", n.host, n.pid)] = true
			}
		case "model":
			models++
			if n.end != nil {
				tok.add(n.end.Tokens)
			}
		case "tool":
			tools++
			if n.end != nil && n.end.Error != "" {
				failures++
			}
		}
	}

	fmt.Fprintf(out, "flow %s\n\n", full)
	var walk func(n *node, prefix string, lastChild bool)
	walk = func(n *node, prefix string, lastChild bool) {
		branch, next := "├─ ", "│  "
		if lastChild {
			branch, next = "└─ ", "   "
		}
		fmt.Fprintf(out, "%s%s%s\n", prefix, branch, flowLabel(n, first))
		kids := n.children
		for i, c := range kids {
			walk(c, prefix+next, i == len(kids)-1)
		}
	}
	for i, r := range roots {
		walk(r, "", i == len(roots)-1 && len(dangling) == 0)
	}
	parents := sortedKeys(dangling)
	for i, p := range parents {
		branch, next := "├─ ", "│  "
		if i == len(parents)-1 {
			branch, next = "└─ ", "   "
		}
		fmt.Fprintf(out, "%s(parent %s not in these files)\n", branch, p)
		kids := dangling[p]
		sort.SliceStable(kids, func(a, b int) bool { return kids[a].at().Before(kids[b].at()) })
		for j, c := range kids {
			walk(c, next, j == len(kids)-1)
		}
	}
	fmt.Fprintf(out, "\ntotal  wall=%s  runs=%d  model calls=%d  tool calls=%d", ms(last.Sub(first).Milliseconds()), runs, models, tools)
	if failures > 0 {
		fmt.Fprintf(out, " (%d failed)", failures)
	}
	fmt.Fprintf(out, "\n       tokens %s\n", tok.String())
	if len(procs) > 0 {
		fmt.Fprintf(out, "       ran on %s\n", strings.Join(sortedKeys(procs), ", "))
	}
	return nil
}

func flowLabel(n *node, t0 time.Time) string {
	at := fmt.Sprintf("+%s", ms(n.at().Sub(t0).Milliseconds()))
	switch n.kind {
	case "run":
		s := fmt.Sprintf("run %s %s  %s %s", nz(n.agent), at, ms(n.durMs()), outcome(n))
		if n.end != nil && n.end.Summary != nil && n.end.Summary.Tokens != nil {
			s += "  " + n.end.Summary.Tokens.String()
		}
		if n.host != "" {
			s += fmt.Sprintf("  [%s pid %d]", n.host, n.pid)
		}
		return s + "  " + n.span
	case "model":
		row := modelRow(at, n)
		if n.end != nil && n.end.ServerTools != nil {
			row += fmt.Sprintf("  server_tools=%d", len(n.end.ServerTools.Uses))
		}
		return strings.TrimSpace(row)
	case "tool":
		return strings.TrimSpace(toolRow(at, n))
	case "task":
		return fmt.Sprintf("long task %s  %d segments  %s", at, n.segmentsSeen, n.span)
	}
	return n.kind + " " + n.span
}
