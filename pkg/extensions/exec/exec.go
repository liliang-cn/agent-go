// Package exec runs an extension that is not written in Go.
//
// An extension is a bundle over the loop's seams, and nothing about those
// seams is Go-specific: every one of them is a question with a small answer.
// This package asks those questions over a pipe. A plugin is any executable
// that reads one JSON object per line on stdin and writes one JSON object per
// line on stdout; the process is started when the service is built and told
// to leave when it is closed. To the framework it is an ordinary
// agent.Extension, so nothing in the loop changes and nothing in the loop
// knows.
//
//	svc, err := agent.New("assistant").
//		WithExtensions(exec.New("redact", []string{"python3", "plugins/redact.py"})).
//		Build()
//
// # The handshake decides what is asked
//
// On Start the extension sends
//
//	{"id":1,"type":"hello","protocol":1,"name":"redact"}
//
// and the plugin answers
//
//	{"id":1,"type":"hello","protocol":1,"capabilities":["after_tool","lint"]}
//
// naming which of "context", "before_tool", "after_tool", "lint",
// "run_start" and "run_end" it implements. Only those are ever sent to it.
//
// This matters more than it looks. agent.Build detects an extension's
// capabilities by type assertion on the Go value, and this one Go type
// implements all of them — so the framework will call every seam of every
// exec plugin, whatever the plugin is for. The gate is therefore ours: each
// method checks the declared capability set first and returns "unchanged"
// without touching the pipe. The cost is one map lookup per seam per turn on
// a plugin that declared nothing there; the alternative — synthesising a
// different Go type per capability combination — buys a cheaper no-op with a
// constructor nobody can read.
//
// # Failing closed
//
// A plugin is a process, and a process can hang, crash, or be killed by
// something else entirely. Every seam has one answer for that, and it is the
// answer the seam's own contract already gives (docs/extensions.md):
//
//   - after_tool  — the model does not see the result. A filter that could
//     not inspect a result must not let it through.
//   - before_tool — the call is refused, with the failure as the reason.
//   - lint        — the answer is rejected. Exhausting the lint budget blocks
//     the run, which is the honest end for a run whose checker is gone.
//   - context     — nothing is contributed, and the failure is logged. A
//     missing note is not worth failing a run over.
//   - run_start   — the run is blocked before its first model turn.
//   - run_end     — logged only. It cannot change an outcome that already
//     happened.
//
// A timeout (WithTimeout, 5s by default) or a broken pipe retires the
// process rather than reusing it: the reply we gave up on may still arrive
// and would be read as the answer to the next request. Every later request
// then fails immediately, still closed. A plugin's *own* "error" in a reply
// is different — it answered, it just said no — and leaves the process alone.
//
// # Concurrency
//
// The protocol is one line out, one line back, so requests to one process are
// serialised by a mutex. A Service runs many tasks at once and shares its
// extensions between them: a plugin that takes 200ms per call makes every
// concurrent run queue behind every other. WithConcurrency(n) runs n
// identical processes and hands each request the first free one; they must
// all declare the same capabilities, since they are meant to be the same
// plugin. State a plugin keeps between requests is then per-process, which is
// a reason to keep plugins stateless.
//
// Retirement is per process and there is no restart: a retired process stays
// in the rotation and answers instantly with its failure, so it costs no
// latency, and with n processes one broken one fails one request in n. A
// plugin that dies is a plugin to fix, not one to route around.
//
// # Stderr
//
// Everything the plugin writes to stderr is forwarded to the framework logger
// line by line, tagged with the plugin's name. stdout is the protocol and
// must carry nothing else — a stray print there is an undecodable reply, and
// an undecodable reply retires the process.
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/log"
)

const logModule = "agent.extensions.exec"

// Defaults.
const (
	// DefaultTimeout bounds one request, including the wait for a free
	// process when WithConcurrency is in use.
	DefaultTimeout = 5 * time.Second
	// DefaultShutdownGrace is how long a plugin has to leave on its own
	// before it is killed.
	DefaultShutdownGrace = 2 * time.Second
)

// Option configures the extension.
type Option func(*Extension)

// WithTimeout bounds one request. Default DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(e *Extension) {
		if d > 0 {
			e.timeout = d
		}
	}
}

// WithShutdownGrace bounds how long Stop waits for the process to exit after
// the shutdown message before killing it. Default DefaultShutdownGrace.
func WithShutdownGrace(d time.Duration) Option {
	return func(e *Extension) {
		if d > 0 {
			e.grace = d
		}
	}
}

// WithConcurrency runs n identical plugin processes so that n requests can be
// in flight at once. Default 1.
func WithConcurrency(n int) Option {
	return func(e *Extension) {
		if n > 0 {
			e.concurrency = n
		}
	}
}

// WithEnv adds "KEY=VALUE" entries to the process environment, which is
// otherwise the host's own.
func WithEnv(env ...string) Option {
	return func(e *Extension) { e.env = append(e.env, env...) }
}

// WithDir runs the process in dir. Default: the host's working directory.
func WithDir(dir string) Option {
	return func(e *Extension) { e.dir = dir }
}

// WithLogger routes this extension's own lines, and the plugin's stderr,
// through l instead of the framework logger.
func WithLogger(l *slog.Logger) Option {
	return func(e *Extension) {
		if l != nil {
			e.logger = l.With("module", logModule)
		}
	}
}

// Extension is an out-of-process plugin exposed as an ordinary
// agent.Extension. It implements agent.Lifecycle, agent.ContextContributor,
// agent.ToolCallFilter, agent.ToolResultFilter, agent.OutputLint and
// agent.RunLifecycle; each seam is a no-op unless the plugin declared the
// matching capability in its handshake.
type Extension struct {
	name        string
	command     []string
	env         []string
	dir         string
	timeout     time.Duration
	grace       time.Duration
	concurrency int
	logger      *slog.Logger

	mu      sync.RWMutex
	caps    map[string]bool
	workers []*worker
	pool    chan *worker
}

var (
	_ agent.Extension          = (*Extension)(nil)
	_ agent.Lifecycle          = (*Extension)(nil)
	_ agent.ContextContributor = (*Extension)(nil)
	_ agent.ToolCallFilter     = (*Extension)(nil)
	_ agent.ToolResultFilter   = (*Extension)(nil)
	_ agent.OutputLint         = (*Extension)(nil)
	_ agent.RunLifecycle       = (*Extension)(nil)
)

// New returns an extension that runs command as a plugin under the given
// name. The name identifies it in logs, events and lint feedback, and must be
// unique within a service. Nothing is started until Build() calls Start.
func New(name string, command []string, opts ...Option) *Extension {
	e := &Extension{
		name:        name,
		command:     append([]string(nil), command...),
		timeout:     DefaultTimeout,
		grace:       DefaultShutdownGrace,
		concurrency: 1,
		logger:      log.WithModule(logModule),
		caps:        map[string]bool{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Name implements agent.Extension.
func (e *Extension) Name() string { return e.name }

// DeclaredCapabilities reports what the plugin said it implements, after
// Start. Empty before it, and after Stop.
func (e *Extension) DeclaredCapabilities() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []string
	for _, c := range Capabilities {
		if e.caps[c] {
			out = append(out, c)
		}
	}
	return out
}

func (e *Extension) has(capability string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.caps[capability]
}

// Start implements agent.Lifecycle: it launches the processes and performs
// the handshake. A failure here fails the build, deliberately — an extension
// whose plugin will not start is one whose every seam would fail closed, and
// discovering that at Build is better than discovering it mid-run.
func (e *Extension) Start(ctx context.Context) error {
	if e.name == "" {
		return errors.New("exec: extension name is empty")
	}
	if len(e.command) == 0 {
		return fmt.Errorf("exec: plugin %q has no command", e.name)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.workers) > 0 {
		return fmt.Errorf("exec: plugin %q is already started", e.name)
	}

	var (
		workers []*worker
		agreed  []string
	)
	fail := func(err error) error {
		for _, w := range workers {
			w.shutdown(e.grace)
		}
		return err
	}
	for i := 0; i < e.concurrency; i++ {
		w, err := startWorker(e.name, e.command, e.env, e.dir, e.logger)
		if err != nil {
			return fail(fmt.Errorf("exec: plugin %q: %w", e.name, err))
		}
		workers = append(workers, w)

		caps, err := e.handshake(ctx, w)
		if err != nil {
			return fail(err)
		}
		if i == 0 {
			agreed = caps
			continue
		}
		if strings.Join(agreed, ",") != strings.Join(caps, ",") {
			return fail(fmt.Errorf("exec: plugin %q: process %d declared [%s], process 0 declared [%s]",
				e.name, i, strings.Join(caps, ","), strings.Join(agreed, ",")))
		}
	}

	e.caps = map[string]bool{}
	for _, c := range agreed {
		e.caps[c] = true
	}
	e.workers = workers
	e.pool = make(chan *worker, len(workers))
	for _, w := range workers {
		e.pool <- w
	}
	e.logger.Info("plugin started", "plugin", e.name,
		"processes", len(workers), "capabilities", strings.Join(agreed, ","))
	return nil
}

// handshake exchanges hello and returns the capabilities the plugin declared,
// in the canonical order of Capabilities so two processes can be compared.
func (e *Extension) handshake(ctx context.Context, w *worker) ([]string, error) {
	rctx, cancel := context.WithTimeout(orBackground(ctx), e.timeout)
	defer cancel()

	rep, err := w.roundTrip(rctx, request{Type: typeHello, Protocol: ProtocolVersion, Name: e.name}, e.timeout)
	if err != nil {
		return nil, fmt.Errorf("exec: plugin %q: handshake: %w", e.name, err)
	}
	if rep.Type != "" && rep.Type != typeHello {
		return nil, fmt.Errorf("exec: plugin %q: handshake answered with type %q", e.name, rep.Type)
	}
	if rep.Protocol != ProtocolVersion {
		return nil, fmt.Errorf("exec: plugin %q speaks protocol %d, this framework speaks %d",
			e.name, rep.Protocol, ProtocolVersion)
	}
	declared := map[string]bool{}
	for _, c := range rep.Capabilities {
		if !knownCapability(c) {
			// Loud, not lenient: a typo that silently disables a seam is a
			// plugin that looks installed and does nothing.
			return nil, fmt.Errorf("exec: plugin %q declared unknown capability %q (known: %s)",
				e.name, c, strings.Join(Capabilities, ", "))
		}
		declared[c] = true
	}
	var out []string
	for _, c := range Capabilities {
		if declared[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

// Stop implements agent.Lifecycle. It sends the shutdown message, closes
// stdin, and kills whatever is still running when the grace period is up. It
// reports no error: a plugin that had to be killed is a logged fact, not a
// reason for Close to fail.
func (e *Extension) Stop(context.Context) error {
	e.mu.Lock()
	workers := e.workers
	e.workers = nil
	e.pool = nil
	e.caps = map[string]bool{}
	e.mu.Unlock()

	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w *worker) {
			defer wg.Done()
			w.shutdown(e.grace)
		}(w)
	}
	wg.Wait()
	return nil
}

func orBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// call runs one request against a free process. The timeout covers the wait
// for that process as well as the exchange itself, so a saturated plugin
// fails closed the same way a hung one does.
func (e *Extension) call(ctx context.Context, req request) (*reply, error) {
	rctx, cancel := context.WithTimeout(orBackground(ctx), e.timeout)
	defer cancel()

	e.mu.RLock()
	pool := e.pool
	e.mu.RUnlock()
	if pool == nil {
		return nil, fmt.Errorf("exec: plugin %q: %w", e.name, ErrNotRunning)
	}

	var w *worker
	select {
	case w = <-pool:
	case <-rctx.Done():
		return nil, fmt.Errorf("exec: plugin %q: %s: no free process within %s", e.name, req.Type, e.timeout)
	}
	defer func() {
		select {
		case pool <- w:
		default:
		}
	}()

	rep, err := w.roundTrip(rctx, req, e.timeout)
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	return rep, nil
}

// ContributeContext implements agent.ContextContributor. A failure
// contributes nothing and is logged: an extension that cannot add a note is
// not a reason to fail the run.
func (e *Extension) ContributeContext(ctx context.Context, in agent.ContextInput) ([]domain.Message, error) {
	if !e.has(CapContext) {
		return nil, nil
	}
	rep, err := e.call(ctx, request{Type: typeContext, Context: &contextInput{
		Goal: in.Goal, SessionID: in.SessionID, AgentID: in.AgentID,
	}})
	if err != nil {
		e.logger.Warn("plugin contributed no context", "plugin", e.name, "error", err)
		return nil, nil
	}
	var out []domain.Message
	for _, m := range rep.Messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		role := m.Role
		if role == "" {
			role = "system"
		}
		out = append(out, domain.Message{Role: role, Content: m.Content})
	}
	return out, nil
}

// BeforeTool implements agent.ToolCallFilter. A failure refuses the call: a
// gate that cannot answer is not one the call may walk past.
func (e *Extension) BeforeTool(ctx context.Context, call agent.ToolCallInfo) (agent.ToolVerdict, error) {
	if !e.has(CapBeforeTool) {
		return agent.ToolVerdict{}, nil
	}
	rep, err := e.call(ctx, request{Type: typeBeforeTool, Call: &toolCallInfo{
		Name: call.Name, Args: call.Args, SessionID: call.SessionID, AgentID: call.AgentID,
	}})
	if err != nil {
		return agent.ToolVerdict{}, fmt.Errorf("could not check tool %s: %w", call.Name, err)
	}
	return agent.ToolVerdict{Args: rep.Args, Block: rep.Block}, nil
}

// AfterTool implements agent.ToolResultFilter. A failure returns an error, so
// the model sees the failure instead of the result the plugin was meant to
// inspect.
func (e *Extension) AfterTool(ctx context.Context, res agent.ToolResultInfo) (interface{}, bool, error) {
	if !e.has(CapAfterTool) {
		return nil, false, nil
	}
	payload := &toolResultInfo{
		Name: res.Name, Args: res.Args, Result: res.Result,
		SessionID: res.SessionID, AgentID: res.AgentID,
	}
	if res.Err != nil {
		payload.Error = res.Err.Error()
	}
	rep, err := e.call(ctx, request{Type: typeAfterTool, Result: payload})
	if err != nil {
		return nil, false, fmt.Errorf("could not inspect the result of %s: %w", res.Name, err)
	}
	if !rep.Replaced {
		return nil, false, nil
	}
	var replacement interface{}
	if len(rep.Result) > 0 {
		if err := json.Unmarshal(rep.Result, &replacement); err != nil {
			return nil, false, fmt.Errorf("plugin %q returned an undecodable replacement for %s: %w", e.name, res.Name, err)
		}
	}
	return replacement, true, nil
}

// Check implements agent.OutputLint. A failure rejects the answer, because a
// checker that did not run has not passed anything.
func (e *Extension) Check(text string, lc agent.LintContext) (bool, string) {
	if !e.has(CapLint) {
		return true, ""
	}
	rep, err := e.call(context.Background(), request{Type: typeLint, Lint: &lintInput{
		Text: text, AgentName: lc.AgentName, TaskID: lc.TaskID, SessionID: lc.SessionID,
		TurnIndex: lc.TurnIndex, Goal: lc.Goal, ToolCalls: lc.ToolCalls,
		AvailableTools: lc.AvailableTools, Deliverables: lc.Deliverables,
		RequestedActions: lc.RequestedActions, Workspace: lc.Workspace,
		IsRetry: lc.IsRetry, RetryCount: lc.RetryCount,
	}})
	if err != nil {
		e.logger.Error("plugin lint failed; rejecting the answer", "plugin", e.name, "error", err)
		return false, fmt.Sprintf("the %s check could not run (%v); the answer cannot be accepted until it does", e.name, err)
	}
	if rep.OK {
		return true, ""
	}
	reason := rep.Reason
	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("the %s check rejected this answer", e.name)
	}
	return false, reason
}

// OnRunStart implements agent.RunLifecycle. An error here blocks the run
// before its first model turn.
func (e *Extension) OnRunStart(ctx context.Context, run agent.RunInfo) error {
	if !e.has(CapRunStart) {
		return nil
	}
	_, err := e.call(ctx, request{Type: typeRunStart, Run: &runInfo{
		Goal: run.Goal, SessionID: run.SessionID, AgentID: run.AgentID, TaskID: run.TaskID,
	}})
	return err
}

// OnRunEnd implements agent.RunLifecycle. It cannot change the outcome, so a
// failure is only logged.
func (e *Extension) OnRunEnd(ctx context.Context, run agent.RunInfo, outcome agent.RunOutcome) {
	if !e.has(CapRunEnd) {
		return
	}
	_, err := e.call(ctx, request{Type: typeRunEnd,
		Run: &runInfo{Goal: run.Goal, SessionID: run.SessionID, AgentID: run.AgentID, TaskID: run.TaskID},
		Outcome: &runOutcome{
			StopReason: string(outcome.StopReason), Text: outcome.Text,
			Blocked: outcome.Blocked, Cancelled: outcome.Cancelled,
			DurationMS: outcome.Duration.Milliseconds(),
		}})
	if err != nil {
		e.logger.Warn("plugin did not record the run's end", "plugin", e.name, "error", err)
	}
}
