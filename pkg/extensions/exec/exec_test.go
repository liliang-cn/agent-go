package exec

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

// newExtension builds an extension in the given fake-plugin mode without
// starting it — the service's Build does that.
func newExtension(mode string, opts ...Option) *Extension {
	return New(mode+"-plugin", pluginCommand(), append([]Option{pluginMode(mode)}, opts...)...)
}

// newPlugin builds and starts one, for the tests that drive the seams
// directly rather than through a service.
func newPlugin(t *testing.T, mode string, opts ...Option) *Extension {
	t.Helper()
	e := newExtension(mode, opts...)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start %s: %v", mode, err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e
}

func TestHandshakeDeclaresWhatIsWired(t *testing.T) {
	e := newPlugin(t, "lint-only")
	if got := strings.Join(e.DeclaredCapabilities(), ","); got != CapLint {
		t.Fatalf("declared capabilities = %q, want %q", got, CapLint)
	}

	// Everything the plugin did not declare must be a no-op that never
	// reaches the pipe — the fake plugin answers "unexpected request type"
	// for those, so a leak would surface as an error here.
	if msgs, err := e.ContributeContext(context.Background(), agent.ContextInput{Goal: "g"}); err != nil || msgs != nil {
		t.Fatalf("ContributeContext = %v, %v; want nil, nil", msgs, err)
	}
	if v, err := e.BeforeTool(context.Background(), agent.ToolCallInfo{Name: "echo"}); err != nil || v.Block != "" || v.Args != nil {
		t.Fatalf("BeforeTool = %+v, %v; want a zero verdict", v, err)
	}
	if res, replaced, err := e.AfterTool(context.Background(), agent.ToolResultInfo{Name: "echo"}); err != nil || replaced || res != nil {
		t.Fatalf("AfterTool = %v, %v, %v; want unchanged", res, replaced, err)
	}
	if err := e.OnRunStart(context.Background(), agent.RunInfo{}); err != nil {
		t.Fatalf("OnRunStart = %v; want nil", err)
	}
	e.OnRunEnd(context.Background(), agent.RunInfo{}, agent.RunOutcome{})

	if ok, reason := e.Check("anything", agent.LintContext{}); !ok {
		t.Fatalf("declared lint rejected a clean answer: %s", reason)
	}
}

func TestHandshakeRefusesAProtocolItDoesNotSpeak(t *testing.T) {
	e := New("bad-version-plugin", pluginCommand(), pluginMode("bad-version"))
	err := e.Start(context.Background())
	if err == nil {
		_ = e.Stop(context.Background())
		t.Fatal("a plugin speaking protocol 99 started anyway")
	}
	if !strings.Contains(err.Error(), "protocol 99") {
		t.Fatalf("error = %v", err)
	}
}

func TestHandshakeRefusesAnUnknownCapability(t *testing.T) {
	e := New("unknown-cap-plugin", pluginCommand(), pluginMode("unknown-cap"))
	err := e.Start(context.Background())
	if err == nil {
		_ = e.Stop(context.Background())
		t.Fatal("a plugin declaring telepathy started anyway")
	}
	if !strings.Contains(err.Error(), "telepathy") {
		t.Fatalf("error = %v", err)
	}
}

func TestStartFailsWhenTheCommandDoesNot(t *testing.T) {
	e := New("missing", []string{"./definitely-not-here"})
	if err := e.Start(context.Background()); err == nil {
		_ = e.Stop(context.Background())
		t.Fatal("start of a missing command reported success")
	}
}

// TestEverySeamThroughTheRealLoop drives one run through the framework with a
// plugin that implements all six capabilities, and asserts on what the model
// was actually shown.
func TestEverySeamThroughTheRealLoop(t *testing.T) {
	logs := newLogSink()
	ext := newExtension("full", WithLogger(logs.logger()))

	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "the raw secret"}),
		extensiontest.Answer("the secret is out"), // rejected by the plugin's lint
		extensiontest.Answer("all clear"),
	)
	svc := extensiontest.NewService(t, llm, extensiontest.EchoTool(), ext)
	out := extensiontest.Run(t, svc, "what did echo say?")

	if out.Blocked != "" || out.Final != "all clear" {
		t.Fatalf("final=%q blocked=%q errors=%v", out.Final, out.Blocked, out.Errors)
	}
	if llm.Calls() != 3 {
		t.Fatalf("expected the plugin lint to force a third turn, got %d calls", llm.Calls())
	}

	rounds := llm.Rounds()
	if !strings.Contains(allContent(rounds[0]), "house rule: what did echo say?") {
		t.Fatalf("the contributed context never reached the model: %s", allContent(rounds[0]))
	}
	tools := extensiontest.ToolMessages(rounds[1])
	if len(tools) != 1 {
		t.Fatalf("expected one tool message, got %d", len(tools))
	}
	if !strings.Contains(tools[0].Content, "cooked") {
		t.Fatalf("before_tool did not rewrite the arguments: %q", tools[0].Content)
	}
	if !strings.Contains(tools[0].Content, "[redacted]") || strings.Contains(tools[0].Content, "secret") {
		t.Fatalf("after_tool did not mask the result: %q", tools[0].Content)
	}

	// The plugin's stderr is forwarded, named, to the framework logger.
	if !logs.waitFor(t, "masked a value in echo") {
		t.Fatalf("plugin stderr was not forwarded; log = %s", logs.String())
	}
	if !strings.Contains(logs.String(), "full-plugin") {
		t.Fatalf("forwarded stderr did not name the plugin: %s", logs.String())
	}
}

func TestBeforeToolBlockRefusesTheCall(t *testing.T) {
	ext := newExtension("block")

	var calls int
	counter := extensiontest.ToolModule("echo", "echoes its input",
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			calls++
			return map[string]interface{}{"echoed": args["text"]}, nil
		})

	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "the forbidden word"}),
		extensiontest.Answer("I could not say it"),
	)
	svc := extensiontest.NewService(t, llm, counter, ext)
	out := extensiontest.Run(t, svc, "say the forbidden word")

	if calls != 0 {
		t.Fatalf("the tool ran %d times despite the block", calls)
	}
	tools := extensiontest.ToolMessages(llm.Rounds()[1])
	if len(tools) != 1 || !strings.Contains(tools[0].Content, "may not be sent anywhere") {
		t.Fatalf("the model was not told why the call was refused: %+v", tools)
	}
	if out.Final != "I could not say it" {
		t.Fatalf("final=%q blocked=%q", out.Final, out.Blocked)
	}
}

// TestAfterToolTimeoutFailsClosed is the important one: a plugin that never
// answers must not let the result it was meant to inspect reach the model.
func TestAfterToolTimeoutFailsClosed(t *testing.T) {
	ext := newExtension("hang", WithTimeout(300*time.Millisecond), WithShutdownGrace(200*time.Millisecond))

	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "the secret"}),
		extensiontest.Answer("nothing to report"),
	)
	svc := extensiontest.NewService(t, llm, extensiontest.EchoTool(), ext)
	out := extensiontest.Run(t, svc, "what did echo say?")

	tools := extensiontest.ToolMessages(llm.Rounds()[1])
	if len(tools) != 1 {
		t.Fatalf("expected one tool message, got %d", len(tools))
	}
	if strings.Contains(tools[0].Content, "secret") {
		t.Fatalf("the unchecked result reached the model: %q", tools[0].Content)
	}
	if !strings.Contains(tools[0].Content, "timed out") {
		t.Fatalf("the model was not told the check failed: %q", tools[0].Content)
	}
	if out.Final != "nothing to report" {
		t.Fatalf("final=%q blocked=%q", out.Final, out.Blocked)
	}

	// The hung process is retired, not reused: a later request fails at once
	// rather than waiting out another timeout.
	start := time.Now()
	if _, _, err := ext.AfterTool(context.Background(), agent.ToolResultInfo{Name: "echo"}); err == nil {
		t.Fatal("a retired plugin answered a later request")
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("a retired plugin took %s to refuse", elapsed)
	}
}

func TestAfterToolCrashFailsClosed(t *testing.T) {
	ext := newExtension("crash")

	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "the secret"}),
		extensiontest.Answer("nothing to report"),
	)
	svc := extensiontest.NewService(t, llm, extensiontest.EchoTool(), ext)
	extensiontest.Run(t, svc, "what did echo say?")

	tools := extensiontest.ToolMessages(llm.Rounds()[1])
	if len(tools) != 1 {
		t.Fatalf("expected one tool message, got %d", len(tools))
	}
	if strings.Contains(tools[0].Content, "secret") {
		t.Fatalf("the unchecked result reached the model: %q", tools[0].Content)
	}
	if !strings.Contains(tools[0].Content, "could not inspect the result of echo") {
		t.Fatalf("the model was not told the check failed: %q", tools[0].Content)
	}
}

func TestLintFailureRejectsTheAnswer(t *testing.T) {
	lint := newPlugin(t, "lint-only")

	// Kill the process behind its back, the way an OOM killer would.
	lint.mu.RLock()
	w := lint.workers[0]
	lint.mu.RUnlock()
	w.kill()
	<-w.exited

	ok, reason := lint.Check("a perfectly good answer", agent.LintContext{})
	if ok {
		t.Fatal("a lint whose plugin is dead passed the answer")
	}
	if !strings.Contains(reason, "could not run") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestRunStartErrorBlocksTheRun(t *testing.T) {
	ext := newExtension("veto")
	llm := extensiontest.Script(extensiontest.Answer("never asked"))
	svc := extensiontest.NewService(t, llm, ext)
	out := extensiontest.Run(t, svc, "anything")

	if !strings.Contains(out.Blocked, "the budget for this agent is spent") {
		t.Fatalf("final=%q blocked=%q", out.Final, out.Blocked)
	}
	if llm.Calls() != 0 {
		t.Fatalf("the model was called %d times despite the veto", llm.Calls())
	}
}

func TestStopKillsAPluginThatIgnoresShutdown(t *testing.T) {
	e := New("stubborn-plugin", pluginCommand(), pluginMode("stubborn"), WithShutdownGrace(200*time.Millisecond))
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	e.mu.RLock()
	w := e.workers[0]
	e.mu.RUnlock()

	start := time.Now()
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("stop returned in %s, before the grace period was up", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("stop took %s", elapsed)
	}
	if w.cmd.ProcessState == nil {
		t.Fatal("the process was never reaped")
	}
	if !strings.Contains(w.cmd.ProcessState.String(), "killed") {
		t.Fatalf("process ended as %q, want a kill", w.cmd.ProcessState)
	}
	if e.DeclaredCapabilities() != nil {
		t.Fatalf("a stopped plugin still declares %v", e.DeclaredCapabilities())
	}
}

// TestConcurrentRequestsUseEveryProcess proves WithConcurrency actually runs
// more than one process: six overlapping requests against three processes
// must be served by three distinct pids.
func TestConcurrentRequestsUseEveryProcess(t *testing.T) {
	ext := newPlugin(t, "slow", WithConcurrency(3), WithTimeout(10*time.Second))

	var (
		mu   sync.Mutex
		pids = map[float64]bool{}
		wg   sync.WaitGroup
	)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, replaced, err := ext.AfterTool(context.Background(), agent.ToolResultInfo{
				Name:   "echo",
				Result: map[string]interface{}{"echoed": "hi"},
			})
			if err != nil || !replaced {
				t.Errorf("AfterTool = %v, %v, %v", res, replaced, err)
				return
			}
			m, ok := res.(map[string]interface{})
			if !ok {
				t.Errorf("result %T is not an object", res)
				return
			}
			mu.Lock()
			pids[m["pid"].(float64)] = true
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(pids) != 3 {
		t.Fatalf("six overlapping requests hit %d processes, want 3", len(pids))
	}
}

// TestConcurrentRunsShareOnePlugin is the -race case the shipped extensions
// have too: one service, many runs, every seam crossed at once.
func TestConcurrentRunsShareOnePlugin(t *testing.T) {
	ext := newExtension("quiet", WithConcurrency(2), WithTimeout(10*time.Second))
	llm := extensiontest.Script(extensiontest.Answer("all clear"))
	svc := extensiontest.NewService(t, llm, ext)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answer, err := svc.Ask(context.Background(), "say something harmless")
			if err != nil {
				t.Errorf("ask: %v", err)
				return
			}
			if answer != "all clear" {
				t.Errorf("answer = %q", answer)
			}
		}()
	}
	wg.Wait()
}

// TestTheReferencePythonPluginWorks runs the plugin shipped in
// examples/extensions-exec through the real loop. It is the only test that
// proves the protocol is writable by something that is not this package: the
// Go fake plugin shares these types, a Python one has nothing but the docs.
func TestTheReferencePythonPluginWorks(t *testing.T) {
	python, err := osexec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", "extensions-exec", "plugins", "redact.py"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("reference plugin not found: %v", err)
	}

	ext := New("redact", []string{python, script})
	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "write to alice@example.com"}),
		extensiontest.Answer("mail alice@example.com"), // rejected by the plugin's lint
		extensiontest.Answer("use the address on file"),
	)
	svc := extensiontest.NewService(t, llm, extensiontest.EchoTool(), ext)
	out := extensiontest.Run(t, svc, "how do I contact them?")

	if out.Final != "use the address on file" {
		t.Fatalf("final=%q blocked=%q errors=%v", out.Final, out.Blocked, out.Errors)
	}
	tools := extensiontest.ToolMessages(llm.Rounds()[1])
	if len(tools) != 1 || !strings.Contains(tools[0].Content, "[email redacted]") ||
		strings.Contains(tools[0].Content, "alice@example.com") {
		t.Fatalf("the address reached the model: %+v", tools)
	}
	if llm.Calls() != 3 {
		t.Fatalf("expected the plugin lint to force a third turn, got %d calls", llm.Calls())
	}
}

func allContent(round []domain.Message) string {
	var b strings.Builder
	for _, m := range round {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// logSink collects what the extension logs so a test can assert on forwarded
// stderr without racing the goroutine that forwards it.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newLogSink() *logSink { return &logSink{} }

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *logSink) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(s, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (s *logSink) waitFor(t *testing.T, want string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), want) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
