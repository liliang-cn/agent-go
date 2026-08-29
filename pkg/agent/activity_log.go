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
func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", "⏎"), "\r", ""))
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
