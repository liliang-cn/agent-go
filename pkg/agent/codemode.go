package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/codemode"
)

// Code mode: the model builds something by writing a program instead of
// writing the thing out. Each mode becomes one tool whose argument is the
// whole program; the tool runs it in the codemode sandbox, validates what it
// built and returns a short report. The agent's ordinary loop is the revise
// loop: the model reads the report, fixes the program and calls the tool
// again, until an attempt passes or the attempt budget is spent. The best
// attempt is kept and handed to the caller as structured data (RunCode),
// never as code in the answer.
//
// This is not a second tool channel: the program cannot call tools, only the
// mode's builder API.

// CodeModeOption tunes one code mode on an agent.
type CodeModeOption func(*codeModeConfig)

type codeModeConfig struct {
	mode        *codemode.Mode
	maxAttempts int
}

// CodeModeAttempts bounds how many programs the model may submit per run.
// Default codemode.DefaultMaxAttempts.
func CodeModeAttempts(n int) CodeModeOption {
	return func(c *codeModeConfig) { c.maxAttempts = n }
}

// WithCodeMode gives the agent a code mode: a tool, named by the mode
// (scene.Scene3D() registers "build_scene"), that runs the model's program
// against the mode's builder and reports back. Use Service.RunCode to run a
// task and get the built artifact.
//
//	svc, err := agent.New("builder").
//	    WithLLM(llm).
//	    WithCodeMode(scene.Scene3D(), agent.CodeModeAttempts(5)).
//	    Build()
//	out, err := svc.RunCode(ctx, "a rack of 6×4 shelves with a box on each")
func (b *Builder) WithCodeMode(m *codemode.Mode, opts ...CodeModeOption) *Builder {
	if m == nil {
		return b
	}
	cfg := codeModeConfig{mode: m}
	for _, o := range opts {
		o(&cfg)
	}
	b.codeModes = append(b.codeModes, cfg)
	return b
}

// codeModeHost owns the code-mode tools of one Service and the attempts of
// the runs using them, keyed by session ID.
type codeModeHost struct {
	modes []codeModeConfig

	mu    sync.Mutex
	runs  map[string]*codeRun
	order []string // lazily created runs, oldest first, for eviction
}

// codeRun is one run's attempts, one session per mode.
type codeRun struct {
	mu       sync.Mutex
	sessions map[string]*codemode.Session // by tool name
	last     string                       // tool most recently submitted to
	pinned   bool                         // created by RunCode; RunCode removes it
}

// maxLazyCodeRuns bounds the attempts kept for runs that did not go through
// RunCode (a plain Run that happened to call the tool).
const maxLazyCodeRuns = 64

func (s *Service) installCodeModes(modes []codeModeConfig) {
	h := &codeModeHost{modes: modes, runs: map[string]*codeRun{}}
	s.codeModes = h
	for _, cfg := range modes {
		cfg := cfg
		tool := cfg.mode.ToolName()
		params := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"code": map[string]interface{}{
					"type":        "string",
					"description": "The whole JavaScript program. Every call replaces the previous attempt, so always send the complete program.",
				},
			},
			"required": []string{"code"},
		}
		s.AddToolWithMetadata(tool, cfg.mode.Describe(), params,
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				return h.submit(ctx, cfg, args)
			},
			// Nothing outside the sandbox changes, but attempts are numbered
			// in order, so two programs in one round must not race.
			ToolMetadata{ReadOnly: true, InterruptBehavior: InterruptBehaviorCancel, OutputLimit: 16 << 10})
	}
	s.RegisterOutputLint(codeModeAnswerLint(h))
}

func (h *codeModeHost) submit(ctx context.Context, cfg codeModeConfig, args map[string]interface{}) (interface{}, error) {
	code, _ := args["code"].(string)
	if strings.TrimSpace(code) == "" {
		return "The code argument is empty. Send the whole program as a string in `code`.", nil
	}
	run := h.runFor(sessionIDFromContext(ctx), true)
	tool := cfg.mode.ToolName()
	run.mu.Lock()
	sess := run.sessions[tool]
	if sess == nil {
		sess = codemode.NewSession(cfg.mode, cfg.maxAttempts)
		run.sessions[tool] = sess
	}
	run.last = tool
	run.mu.Unlock()

	res, err := sess.Submit(ctx, code)
	if errors.Is(err, codemode.ErrAttemptsExhausted) {
		best := sess.Best()
		state := "no attempt passed"
		if best != nil && best.OK() {
			state = fmt.Sprintf("attempt %d passed and is kept", best.Attempt)
		} else if best != nil {
			state = fmt.Sprintf("attempt %d is kept as the best so far", best.Attempt)
		}
		return fmt.Sprintf("No attempts left (%d used); this program was not run. %s. Stop and reply in one or two sentences.", sess.MaxAttempts(), state), nil
	}
	if err != nil {
		return nil, err
	}
	return sess.Report(res), nil
}

func sessionIDFromContext(ctx context.Context) string {
	if s := getCurrentSession(ctx); s != nil {
		return s.GetID()
	}
	return ""
}

// runFor finds the run for a session, creating one when create is set.
func (h *codeModeHost) runFor(sessionID string, create bool) *codeRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.runs[sessionID]; r != nil {
		return r
	}
	if !create {
		return nil
	}
	r := &codeRun{sessions: map[string]*codemode.Session{}}
	h.runs[sessionID] = r
	h.order = append(h.order, sessionID)
	for len(h.order) > maxLazyCodeRuns {
		old := h.order[0]
		h.order = h.order[1:]
		if or := h.runs[old]; or != nil && !or.pinned {
			delete(h.runs, old)
		}
	}
	return r
}

func (h *codeModeHost) pin(sessionID string) *codeRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := &codeRun{sessions: map[string]*codemode.Session{}, pinned: true}
	h.runs[sessionID] = r
	return r
}

func (h *codeModeHost) release(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.runs, sessionID)
}

func (h *codeModeHost) isCodeTool(name string) bool {
	for _, m := range h.modes {
		if m.mode.ToolName() == name {
			return true
		}
	}
	return false
}

// CodeOutcome is what RunCode hands back: the built artifact, not the code.
type CodeOutcome struct {
	// OK reports whether the kept attempt passed validation.
	OK bool `json:"ok"`
	// Mode is the code mode the artifact was built in.
	Mode string `json:"mode"`
	// Artifact is the kept attempt's result as JSON (the scene, the plan...).
	// Nil when no program produced one.
	Artifact json.RawMessage `json:"artifact,omitempty"`
	// Attempts is how many programs the model submitted.
	Attempts int `json:"attempts"`
	// Best is the kept attempt, with its issues, stats, logs and render.
	Best *codemode.Result `json:"best,omitempty"`
	// History is every attempt in order, each with its program in Code.
	History []*codemode.Result `json:"-"`
	// Code is the kept attempt's program, for storing or replaying with
	// codemode.Run. It is not part of the answer.
	Code string `json:"-"`
	// Report is the kept attempt's feedback report.
	Report string `json:"report,omitempty"`
	// Answer is the model's closing sentence or two.
	Answer string `json:"answer,omitempty"`
	// Result is the underlying run.
	Result *ExecutionResult `json:"-"`
}

// ErrNoCodeMode is returned by RunCode on an agent built without WithCodeMode.
var ErrNoCodeMode = errors.New("agent: no code mode configured (use Builder.WithCodeMode)")

// RunCode runs a task on an agent with a code mode and returns the artifact
// the kept attempt built. The returned error is the run's error; a run that
// finished without a passing attempt returns an outcome with OK false (and
// the best failing attempt, if any) and a nil error.
func (s *Service) RunCode(ctx context.Context, goal string, opts ...RunOption) (*CodeOutcome, error) {
	if s.codeModes == nil {
		return nil, ErrNoCodeMode
	}
	probe := DefaultRunConfig()
	for _, o := range opts {
		o(probe)
	}
	sessionID := probe.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
		opts = append(opts, WithSessionID(sessionID))
	}
	run := s.codeModes.pin(sessionID)
	defer s.codeModes.release(sessionID)

	res, err := s.Run(ctx, goal, opts...)
	out := s.codeModes.outcome(run)
	out.Result = res
	if res != nil {
		out.Answer = res.Text()
	}
	return out, err
}

func (h *codeModeHost) outcome(run *codeRun) *CodeOutcome {
	run.mu.Lock()
	tool := run.last
	sess := run.sessions[tool]
	run.mu.Unlock()
	out := &CodeOutcome{}
	if sess == nil {
		if len(h.modes) > 0 {
			out.Mode = h.modes[0].mode.Name
		}
		return out
	}
	out.Mode = sess.Mode().Name
	out.History = sess.Attempts()
	out.Attempts = len(out.History)
	if best := sess.Best(); best != nil {
		out.OK = best.OK()
		out.Artifact = best.Artifact
		out.Best = best
		out.Code = best.Code
		out.Report = sess.Report(best)
	}
	return out
}

var codeFenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*\\n(.*?)```")

// codeModeAnswerLint keeps the program out of the answer and, for a RunCode
// run, makes the model actually run a program before it answers. The
// artifact reaches the caller as data; a reply that pastes the program
// instead is the old failure of a code channel leaking into the answer.
func codeModeAnswerLint(h *codeModeHost) OutputLint {
	return LintFunc{
		NameValue: "code_mode_answer",
		Fn: func(text string, lc LintContext) (bool, string) {
			offered := false
			for _, name := range lc.AvailableTools {
				if h.isCodeTool(name) {
					offered = true
					break
				}
			}
			if !offered {
				return true, ""
			}
			run := h.runFor(lc.SessionID, false)
			if run != nil && run.pinned {
				run.mu.Lock()
				submitted := run.last != ""
				run.mu.Unlock()
				if !submitted {
					return false, "nothing has been built yet. Write the program and run it with " + codeToolList(h) + "; reply only after it has run."
				}
			}
			if leaksProgram(text, run) {
				return false, "your reply contains the program. The built result is handed over separately; reply with one or two sentences describing what was built and how it checked out, without code."
			}
			return true, ""
		},
	}
}

func codeToolList(h *codeModeHost) string {
	names := make([]string, 0, len(h.modes))
	for _, m := range h.modes {
		names = append(names, m.mode.ToolName())
	}
	return strings.Join(names, " or ")
}

// leaksProgram reports whether the answer carries the program: a fenced
// block of three or more lines, or two or more substantial lines of any
// program this run submitted.
func leaksProgram(text string, run *codeRun) bool {
	for _, m := range codeFenceRe.FindAllStringSubmatch(text, -1) {
		if strings.Count(strings.TrimSpace(m[1]), "\n") >= 2 {
			return true
		}
	}
	if run == nil {
		return false
	}
	run.mu.Lock()
	sessions := make([]*codemode.Session, 0, len(run.sessions))
	for _, s := range run.sessions {
		sessions = append(sessions, s)
	}
	run.mu.Unlock()
	for _, s := range sessions {
		for _, a := range s.Attempts() {
			hits := 0
			for _, line := range strings.Split(a.Code, "\n") {
				line = strings.TrimSpace(line)
				if len(line) < 16 {
					continue
				}
				if strings.Contains(text, line) {
					hits++
					if hits >= 2 {
						return true
					}
				}
			}
		}
	}
	return false
}
