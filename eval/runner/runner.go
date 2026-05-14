package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

// LiveBuilder constructs a real-LLM Service for live-mode runs. Callers
// (e.g. the agentgo CLI) supply this to bind the runner to whichever
// provider is configured locally. The runner will pass each scenario's
// agent name and lint registration set; everything else is up to the
// builder. The returned service is closed by the runner.
type LiveBuilder func(scenarioName, agentName string, lints []string, home string) (*agent.Service, error)

// RunOptions controls Run/RunAll behavior. All fields optional.
type RunOptions struct {
	// Live is invoked when a scenario has Mode == "live". If nil, live-mode
	// scenarios fail with a clear error message.
	Live LiveBuilder
	// MaxConcurrency caps how many scenarios run in parallel inside RunAll.
	// Defaults to 1 (serial). Live runs typically should stay serial to
	// avoid hammering an LLM provider.
	MaxConcurrency int
	// HomeFactory returns a temp scratch directory for a single scenario
	// run. Defaults to os.MkdirTemp under os.TempDir.
	HomeFactory func(scenarioName string) (path string, cleanup func(), err error)
}

// RunResult is the structured outcome of running a single scenario,
// possibly across multiple repetitions.
type RunResult struct {
	Scenario       string         `json:"scenario"`
	Mode           Mode           `json:"mode"`
	Runs           int            `json:"runs"`
	Pass           bool           `json:"pass"`
	PassCount      int            `json:"pass_count"`
	FailCount      int            `json:"fail_count"`
	Reasons        []string       `json:"reasons,omitempty"` // one per failing run
	AvgLLMCalls    float64        `json:"avg_llm_calls"`
	AvgDurationMs  float64        `json:"avg_duration_ms"`
	LintViolations map[string]int `json:"lint_violations,omitempty"` // summed across runs
	Status         string         `json:"status"`                    // status of the most recent run
	FinalText      string         `json:"final_text,omitempty"`      // last successful run's final text (or last attempt)
}

// Run executes a scenario for sc.Runs iterations and returns an aggregated
// RunResult. A run is considered passed when every iteration passed.
func Run(ctx context.Context, sc *Scenario, opts RunOptions) (*RunResult, error) {
	if sc == nil {
		return nil, fmt.Errorf("scenario is nil")
	}
	iterations := sc.Runs
	if iterations < 1 {
		iterations = 1
	}

	out := &RunResult{
		Scenario:       sc.Name,
		Mode:           sc.Mode,
		Runs:           iterations,
		LintViolations: make(map[string]int),
	}

	totalLLMCalls := 0
	totalDuration := time.Duration(0)
	for i := 0; i < iterations; i++ {
		single, err := runOnce(ctx, sc, opts)
		if err != nil {
			return nil, err
		}
		totalLLMCalls += single.LLMCalls
		totalDuration += single.duration
		for k, v := range single.LintViolations {
			out.LintViolations[k] += v
		}
		out.Status = single.Status
		out.FinalText = single.FinalText
		if single.Pass {
			out.PassCount++
		} else {
			out.FailCount++
			if single.Reason != "" {
				out.Reasons = append(out.Reasons, fmt.Sprintf("run %d/%d: %s", i+1, iterations, single.Reason))
			}
		}
	}

	out.AvgLLMCalls = float64(totalLLMCalls) / float64(iterations)
	out.AvgDurationMs = float64(totalDuration.Milliseconds()) / float64(iterations)
	out.Pass = out.FailCount == 0
	return out, nil
}

// RunAll loads every scenario from dir and runs each one. Scenarios run
// serially in deterministic order to keep results comparable and avoid
// rate-limiting live providers.
func RunAll(ctx context.Context, dir string, opts RunOptions) ([]*RunResult, error) {
	scenarios, err := LoadScenariosFromDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]*RunResult, 0, len(scenarios))
	for _, sc := range scenarios {
		res, err := Run(ctx, sc, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// MarshalResults converts a slice of RunResults into a stable JSON
// document with summary fields, suitable for writing to
// eval/results/<ts>.json.
func MarshalResults(results []*RunResult, profile string) ([]byte, error) {
	pass, fail := 0, 0
	for _, r := range results {
		if r.Pass {
			pass++
		} else {
			fail++
		}
	}
	out := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"profile":   profile,
		"summary": map[string]int{
			"total": len(results),
			"pass":  pass,
			"fail":  fail,
		},
		"results": results,
	}
	return json.MarshalIndent(out, "", "  ")
}

// --- internals ---------------------------------------------------------

// singleRun is the per-iteration outcome (not exported).
type singleRun struct {
	Pass           bool
	Reason         string
	Status         string
	FinalText      string
	LLMCalls       int
	LintViolations map[string]int
	duration       time.Duration
}

func runOnce(ctx context.Context, sc *Scenario, opts RunOptions) (*singleRun, error) {
	start := time.Now()

	homeFactory := opts.HomeFactory
	if homeFactory == nil {
		homeFactory = defaultHomeFactory
	}
	home, cleanup, err := homeFactory(sc.Name)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	res := &singleRun{
		LintViolations: make(map[string]int),
	}

	var (
		svc      *agent.Service
		mock     *MockLLM
		buildErr error
	)

	switch sc.Mode {
	case ModeMock, "":
		mock = NewMockLLM(sc.LLMReplies)
		svc, buildErr = buildMockService(sc, home, mock)
	case ModeLive:
		if opts.Live == nil {
			return nil, fmt.Errorf("scenario %s: live mode requires RunOptions.Live builder", sc.Name)
		}
		svc, buildErr = opts.Live(sc.Name, scenarioAgentName(sc), sc.RegisterLints, home)
	default:
		return nil, fmt.Errorf("scenario %s: unknown mode %q", sc.Name, sc.Mode)
	}
	if buildErr != nil {
		return nil, fmt.Errorf("scenario %s: build service: %w", sc.Name, buildErr)
	}
	defer svc.Close()

	if sc.Mode == ModeMock || sc.Mode == "" {
		// Mock mode wires lints from the scenario spec; live mode lets the
		// caller's LiveBuilder decide (typically RegisterDefaultOutputLints
		// and/or whatever TeamManager auto-wires).
		if err := registerLints(svc, sc.RegisterLints); err != nil {
			return nil, fmt.Errorf("scenario %s: %w", sc.Name, err)
		}
	}

	events, err := svc.RunStream(ctx, sc.Input)
	if err != nil {
		return nil, fmt.Errorf("scenario %s: RunStream failed: %w", sc.Name, err)
	}

	final, blocked, sawError := collectEvents(events, res.LintViolations)
	if mock != nil {
		res.LLMCalls = mock.CallCount()
	}
	res.duration = time.Since(start)

	switch {
	case final != "":
		res.Status = "completed"
		res.FinalText = final
	case blocked != "":
		res.Status = "blocked"
		res.FinalText = blocked
	case sawError:
		res.Status = "error"
	default:
		res.Status = "none"
	}

	if reason := assertExpectations(sc, res); reason != "" {
		res.Pass = false
		res.Reason = reason
		return res, nil
	}
	res.Pass = true
	return res, nil
}

func buildMockService(sc *Scenario, home string, mock *MockLLM) (*agent.Service, error) {
	cfg := buildEvalConfig(home)
	return agent.New(scenarioAgentName(sc)).
		WithPTC(false).
		WithConfig(cfg).
		WithLLM(mock).
		Build()
}

// buildEvalConfig assembles the minimal *config.Config the runner needs to
// keep the eval Service self-contained: a temp home, RAG off, file-backed
// memory inside the home so each scenario gets a clean slate. It mirrors
// the pattern used by pkg/agent's own integration tests.
func buildEvalConfig(home string) *config.Config {
	cfg := &config.Config{
		Home: home,
		RAG: config.RAGConfig{
			Enabled: false,
		},
		Memory: config.MemoryConfig{
			StoreType:  "file",
			MemoryPath: filepath.Join(home, "data", "memories"),
		},
	}
	cfg.ApplyHomeLayout()
	return cfg
}

// scenarioAgentName returns the entry agent name as it should be set on
// the underlying Service. Defaults to "Responder" when the scenario does
// not pin one. The name is passed verbatim to agent.New(...) so that
// agent-scoped lints (e.g. dispatcher_no_bounce_back, registered against
// "Dispatcher") match correctly.
func scenarioAgentName(sc *Scenario) string {
	name := strings.TrimSpace(sc.Agent)
	if name == "" {
		return "Responder"
	}
	return name
}

func registerLints(svc *agent.Service, names []string) error {
	for _, name := range names {
		switch strings.TrimSpace(name) {
		case "":
			continue
		case "default":
			agent.RegisterDefaultOutputLints(svc)
		case "dispatcher_no_bounce_back":
			svc.RegisterOutputLint(agent.DispatcherNoBounceBack(), agent.BuiltInDispatcherAgentName)
		case "archivist_no_relative_time":
			svc.RegisterOutputLint(agent.ArchivistNoRelativeTime(), "Archivist")
		case "no_planning_only_finish":
			svc.RegisterOutputLint(agent.NoPlanningOnlyFinish())
		default:
			return fmt.Errorf("unknown lint %q", name)
		}
	}
	return nil
}

// lintRejectMessage parses the EventTypeError content emitted by
// runtime.runFinalLints to extract the lint name. The format is fixed:
//
//	"output lint <name> rejected response: <reason>"
var lintRejectMessage = regexp.MustCompile(`^output lint ([a-zA-Z0-9_]+) (?:rejected response|repeatedly rejected the response):`)

func collectEvents(events <-chan *agent.Event, lintCounts map[string]int) (final string, blocked string, sawError bool) {
	for evt := range events {
		switch evt.Type {
		case agent.EventTypeComplete:
			final = evt.Content
		case agent.EventTypeBlocked:
			blocked = evt.Content
		case agent.EventTypeError:
			sawError = true
			if m := lintRejectMessage.FindStringSubmatch(evt.Content); len(m) == 2 {
				lintCounts[m[1]]++
			}
		}
	}
	return
}

func assertExpectations(sc *Scenario, res *singleRun) string {
	want := sc.Expect

	if want.Status != res.Status {
		return fmt.Sprintf("status mismatch: want %q got %q (final=%q)", want.Status, res.Status, res.FinalText)
	}

	if want.LLMCalls > 0 && want.LLMCalls != res.LLMCalls {
		return fmt.Sprintf("llm_calls mismatch: want %d got %d", want.LLMCalls, res.LLMCalls)
	}

	if msg := matchTextConstraint(res.FinalText, want.FinalTextMatch, true); msg != "" {
		return msg
	}
	if msg := matchTextConstraint(res.FinalText, want.FinalTextMustNotMatch, false); msg != "" {
		return msg
	}

	// Strict mode: when LintViolations is set, every entry must match
	// exactly and unlisted lints must not fire.
	if len(want.LintViolations) > 0 {
		expected := make(map[string]int)
		for _, v := range want.LintViolations {
			expected[v.Lint] = v.Count
		}
		for name, w := range expected {
			if got := res.LintViolations[name]; got != w {
				return fmt.Sprintf("lint %s violation count mismatch: want %d got %d", name, w, got)
			}
		}
		for name, got := range res.LintViolations {
			if _, listed := expected[name]; !listed && got > 0 {
				return fmt.Sprintf("unexpected lint %s fired %d times (not declared in expect.lint_violations)", name, got)
			}
		}
		return ""
	}

	// Loose mode: max_lint_violations sets ceilings only.
	for name, max := range want.MaxLintViolations {
		if got := res.LintViolations[name]; got > max {
			return fmt.Sprintf("lint %s fired %d times, exceeds max_lint_violations cap %d", name, got, max)
		}
	}
	return ""
}

func matchTextConstraint(text, pattern string, mustMatch bool) string {
	if pattern == "" {
		return ""
	}
	matched := false
	if strings.HasPrefix(pattern, "re:") {
		pat := strings.TrimPrefix(pattern, "re:")
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Sprintf("invalid regex pattern %q: %v", pat, err)
		}
		matched = re.MatchString(text)
	} else {
		matched = strings.Contains(text, pattern)
	}
	switch {
	case mustMatch && !matched:
		return fmt.Sprintf("final text does not contain %q (got %q)", pattern, text)
	case !mustMatch && matched:
		return fmt.Sprintf("final text matches forbidden pattern %q (got %q)", pattern, text)
	}
	return ""
}

// --- helpers exposed via runner_io.go ----------------------------------

func defaultHomeFactory(scenarioName string) (string, func(), error) {
	return makeTempHome(scenarioName)
}

// makeTempHome and friends live in runner_io.go.
