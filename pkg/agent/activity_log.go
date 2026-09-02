package agent

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Watching a run that is still going.
//
// A run in flight is nearly invisible. Its conversation is only written to the
// store when it ends, the events go to whoever called RunStream, and the only
// thing the framework logs of its own accord is the odd warning. That is
// survivable for a turn that takes four seconds and useless for one that has
// been going for half an hour: the questions you actually have — what is it
// doing, is it repeating itself, when did it last write a file, is it waiting
// on the model or on a tool — have no answer short of attaching a debugger.
//
// ActivityLog answers them with one line per thing the run does. It is an
// Observer, so it needs nothing from the loop that is not already exposed, and
// it costs nothing when not attached.
//
// The format is deliberately flat and greppable rather than pretty. What you
// do with a long run's log is `grep tool<` and `awk` over the durations.

// ActivityLog writes a line per model turn, tool call, sub-agent and
// checkpoint. Safe for concurrent use: a run executes tools in parallel.
type ActivityLog struct {
	BaseObserver

	mu       sync.Mutex
	w        io.Writer
	started  time.Time
	modelAt  map[string]time.Time
	toolAt   map[string]time.Time
	toolName map[string]string

	// MaxArgLen bounds how much of a tool's arguments is written. A file write
	// carries the whole file; logging it would bury the line it belongs to.
	MaxArgLen int

	// Last printed resource reading, so the log says something when the
	// numbers move and stays quiet when they do not.
	lastResHeap       uint64
	lastResGoroutines int
}

// NewActivityLog returns an Observer that narrates a run to w.
//
//	log, _ := os.Create("run.log")
//	svc, _ := agent.New("worker").WithObserver(agent.NewActivityLog(log)).Build()
func NewActivityLog(w io.Writer) *ActivityLog {
	return &ActivityLog{
		w:         w,
		started:   time.Now(),
		modelAt:   map[string]time.Time{},
		toolAt:    map[string]time.Time{},
		toolName:  map[string]string{},
		MaxArgLen: 120,
	}
}

func (l *ActivityLog) line(format string, args ...any) {
	if l == nil || l.w == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s %7s  %s\n",
		time.Now().Format("15:04:05"),
		shortDuration(time.Since(l.started)),
		fmt.Sprintf(format, args...))
}

func (l *ActivityLog) OnModelStart(_ context.Context, info ModelInfo) {
	l.mu.Lock()
	l.modelAt[info.SpanID] = time.Now()
	l.mu.Unlock()
	l.line("r%-3d model>   msgs=%d tools=%d", info.Round, info.Messages, info.Tools)
}

func (l *ActivityLog) OnModelEnd(_ context.Context, info ModelInfo, res *ModelResult, err error) {
	l.mu.Lock()
	took := time.Since(l.modelAt[info.SpanID])
	delete(l.modelAt, info.SpanID)
	l.mu.Unlock()

	switch {
	case err != nil:
		l.line("r%-3d model<   %s ERROR %s", info.Round, shortDuration(took), oneLine(err.Error(), 160))
	case res == nil:
		l.line("r%-3d model<   %s (no result)", info.Round, shortDuration(took))
	default:
		cached := ""
		if res.CachedTokens > 0 {
			cached = fmt.Sprintf(" cached=%d", res.CachedTokens)
		}
		l.line("r%-3d model<   %s calls=%d text=%d tokens=%d%s", info.Round,
			shortDuration(took), res.ToolCalls, len(res.Content), res.TokensUsed, cached)
	}
}

func (l *ActivityLog) OnToolStart(_ context.Context, info ToolInfo) {
	l.mu.Lock()
	l.toolAt[info.CallID] = time.Now()
	l.toolName[info.CallID] = info.Tool
	l.mu.Unlock()
	where := ""
	if info.Inner {
		where = " (sub)"
	}
	l.line("     tool>   %s%s %s", info.Tool, where, l.formatArgs(info.Args))
}

func (l *ActivityLog) OnToolEnd(_ context.Context, info ToolInfo, result any, err error) {
	l.mu.Lock()
	took := time.Since(l.toolAt[info.CallID])
	delete(l.toolAt, info.CallID)
	delete(l.toolName, info.CallID)
	l.mu.Unlock()

	if err != nil {
		l.line("     tool<   %s %s FAILED %s", info.Tool, shortDuration(took), oneLine(err.Error(), 160))
		return
	}
	l.line("     tool<   %s %s ok %s", info.Tool, shortDuration(took),
		oneLine(fmt.Sprintf("%v", result), 100))
}

func (l *ActivityLog) OnSubAgentStart(_ context.Context, info SubAgentInfo) {
	l.line("     sub>    %s %s", info.Name, oneLine(info.Goal, 120))
}

func (l *ActivityLog) OnSubAgentEnd(_ context.Context, info SubAgentInfo, result any, err error) {
	if err != nil {
		l.line("     sub<    %s FAILED %s", info.Name, oneLine(err.Error(), 120))
		return
	}
	l.line("     sub<    %s ok %s", info.Name, oneLine(fmt.Sprintf("%v", result), 100))
}

func (l *ActivityLog) OnLint(_ context.Context, info LintInfo) {
	verdict := "BLOCKED by"
	if info.Retrying {
		verdict = "retry after"
	}
	l.line("r%-3d lint     %s %s: %s", info.Round, verdict, info.Lint, oneLine(info.Reason, 200))
}

// OnModelRetry records a re-ask inside a model turn. Without it a turn that
// took three attempts is indistinguishable from one that took one — the span
// opens, time passes, an answer arrives — and a run quietly paying for two
// extra calls every round looks merely slow.
func (l *ActivityLog) OnModelRetry(_ context.Context, info ModelRetryInfo) {
	switch info.Kind {
	case "max_tokens_truncation":
		l.line("r%-3d retry    truncated (%s), max_tokens %d -> %d",
			info.Round, info.Reason, info.MaxTokensFrom, info.MaxTokensTo)
	default:
		l.line("r%-3d retry    %s attempt=%d wait=%s: %s",
			info.Round, info.Kind, info.Attempt,
			shortDuration(info.Delay), oneLine(info.Reason, 160))
	}
}

// OnCompaction records history being folded away. Read this line as "the
// model just forgot everything older than the last few messages": the
// re-reads that follow it are not the agent being redundant.
func (l *ActivityLog) OnCompaction(_ context.Context, info CompactionInfo) {
	l.line("r%-3d compact  %s msgs %d -> %d (context ~%d tokens)",
		info.Round, info.Trigger, info.MessagesBefore, info.MessagesAfter, info.ContextTokens)
}

// OnError records what went wrong. A long run's tool failures reach nobody
// otherwise: its events go to whoever called RunStream, and on a run that
// lasts hours that is a channel nobody is reading.
func (l *ActivityLog) OnError(_ context.Context, info ErrorInfo) {
	marker := info.Marker
	if marker == "" {
		marker = "error"
	}
	l.line("     ERROR    %s: %s", marker, oneLine(info.Message, 240))
}

// OnSegment brackets a segment of a long run, so the boundaries are in the log
// rather than inferred from round numbers restarting at 1.
func (l *ActivityLog) OnSegment(_ context.Context, info SegmentInfo) {
	if !info.Ending {
		l.line("──── segment %d/%d start  session=%s", info.Index, info.Total, shortSessionID(info.SessionID))
		return
	}
	status := string(info.StopReason)
	if info.Err != "" {
		status = "FAILED: " + oneLine(info.Err, 80)
	}
	productive := ""
	if !info.Productive {
		productive = " (changed nothing)"
	}
	l.line("──── segment %d/%d end    %s %s $%.4f%s",
		info.Index, info.Total, status, shortDuration(info.Duration), info.CostUSD, productive)
}

func (l *ActivityLog) OnCheckpoint(_ context.Context, info CheckpointInfo) {
	l.line("r%-3d ckpt     %s msgs=%d", info.Round, info.Reason, info.Messages)
}

// formatArgs renders a tool's arguments in a stable order and bounded length.
// Sorted because a Go map iterates randomly, and a log whose lines differ only
// by field order cannot be diffed or deduplicated by eye.
func (l *ActivityLog) formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+oneLine(fmt.Sprintf("%v", args[k]), l.MaxArgLen))
	}
	return strings.Join(parts, " ")
}

// oneLine flattens and truncates text so a line stays a line.
//
// Newlines become a literal backslash-n rather than a prettier ↵ or ⏎: the log
// exists to be run through grep and awk, and a multi-byte character in it makes
// awk fail outright under a C locale — which is the locale a log-processing
// script is most likely to be running in.
func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", "\\n"), "\r", ""))
	if max > 0 && len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// shortDuration formats a duration at a width a column can rely on.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// shortSessionID trims a UUID to its first block: enough to tell two segments
// apart in a log, short enough not to dominate the line.
func shortSessionID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// OnResourceSample narrates the process itself.
//
// Not every round: a line per round would double the log, and the reason to
// read this is the shape, not the samples. It prints the first reading, the
// last, one every tenth round, and any round where the heap or the goroutine
// count grew by a quarter since the last line — which is the shape of a leak
// showing up in a log a human greps rather than a dashboard nobody opened.
func (a *ActivityLog) OnResourceSample(_ context.Context, s ResourceSample) {
	// The decision is taken under the lock; the write is not. line() takes
	// the same mutex, so holding it across the call deadlocks the run — which
	// is exactly what it did the first time this was written.
	a.mu.Lock()
	grew := func(now, last uint64) bool { return last > 0 && now > last+last/4 }
	print := s.Final || a.lastResHeap == 0 || s.Round%10 == 0 ||
		grew(s.Stats.HeapAllocBytes, a.lastResHeap) ||
		grew(uint64(s.Stats.Goroutines), uint64(a.lastResGoroutines))
	if !print {
		a.mu.Unlock()
		return
	}
	a.lastResHeap = s.Stats.HeapAllocBytes
	a.lastResGoroutines = s.Stats.Goroutines
	a.mu.Unlock()

	round := "r" + itoa(s.Round)
	if s.Final {
		round = "end"
	}
	rss := ""
	if s.Stats.RSSKnown {
		rss = " rss=" + humanBytes(s.Stats.RSSBytes)
	} else if s.Stats.PeakRSSBytes > 0 {
		rss = " peak_rss=" + humanBytes(s.Stats.PeakRSSBytes)
	}
	a.line("res  %-4s heap=%s objs=%d goroutines=%d%s cpu=%.1fs gc=%d",
		round, humanBytes(s.Stats.HeapAllocBytes), s.Stats.HeapObjects,
		s.Stats.Goroutines, rss, s.Stats.CPUSeconds(), s.Stats.NumGC)
}

// humanBytes renders a byte count the way a log reader scans it.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return itoa(int(n)) + "B"
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	whole := n / div
	frac := (n % div) * 10 / div
	return itoa(int(whole)) + "." + itoa(int(frac)) + string("KMGT"[exp]) + "iB"
}
