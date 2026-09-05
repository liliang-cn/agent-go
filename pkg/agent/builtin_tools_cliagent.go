package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agentexec"
	agentexecpty "github.com/liliang-cn/agentexec/pty"
)

// Handing a task to another agent CLI.
//
// The most capable agent runtimes most people have are already installed on
// their machine — claude, codex, gemini, cursor-agent — and each is a whole
// agent with its own tools, its own sandbox posture and its own bill. These
// two tools let an agent-go agent give one of them a job and get the answer
// back, with the sub-agent's output streaming into the parent run's event
// channel and its tokens accounted apart from the parent's.
//
// They are not registered by default, for the same reason the background task
// tools are not: a delegated call is a whole agent run on somebody else's
// subscription, and an agent that can start one without its author deciding so
// can spend money in a loop. RegisterCLIAgentTools is the decision.
//
// Two things about this are worth knowing before relying on it:
//
//   - Being listed means the binary exists. It does not mean it is logged in.
//     On the machine this was written on all four are installed and one works;
//     the rest fail at the first turn with an expired login. There is no cheap
//     probe for that, so the failure is reported rather than predicted.
//   - `claude` with a revoked OAuth token writes "Failed to authenticate" as
//     an assistant message, sets is_error on its result frame, and exits zero.
//     agentexec.Result.Failed is that verdict, and this tool treats it as a
//     failure regardless of the exit code. A caller that read only the summary
//     and the exit status would hand the model an authentication error as if
//     it were the answer, and the model would hand it to the user.

// CLIAgentConfig configures the cli_agent_* tools.
type CLIAgentConfig struct {
	// Agents allow-lists by name; empty means every discovered one.
	Agents []string
	// Binaries overrides a name's path, and whitelists names Discover does not know.
	Binaries map[string]string
	// AllowedRoots bounds cwd. Empty means only the service's workspace.
	AllowedRoots []string
	// DefaultTimeout / MaxTimeout bound one run. Zero takes sensible defaults
	// (10 min / 60 min).
	DefaultTimeout, MaxTimeout time.Duration
}

const (
	defaultCLIAgentTimeout = 10 * time.Minute
	maxCLIAgentTimeout     = 60 * time.Minute
)

// CLIAgentRunResult is what OnSubAgentEnd carries as its `result` for a
// cli_agent_run call: everything an observer needs to bill the run to the
// right agent without re-reading the tool's own JSON. It is a plain struct on
// purpose — an observer that wants to account for delegated spend should be
// able to type-switch on it, not parse a map.
type CLIAgentRunResult struct {
	Agent     string  `json:"agent"`
	SessionID string  `json:"session_id,omitempty"`
	Summary   string  `json:"summary,omitempty"`
	Failed    bool    `json:"failed"`
	ExitCode  int     `json:"exit_code"`
	Duration  int64   `json:"duration_ms"`
	Input     int64   `json:"input_tokens"`
	Output    int64   `json:"output_tokens"`
	Cache     int64   `json:"cache_tokens"`
	CostUSD   float64 `json:"cost_usd"`
	Model     string  `json:"model,omitempty"`
}

// cliAgentRunner holds everything the two tools resolved once at registration:
// which agents exist, how to drive each, and where they are allowed to run.
type cliAgentRunner struct {
	svc            *Service
	agents         []agentexec.Installed
	registry       *agentexec.Registry
	roots          []string
	defaultCwd     string
	defaultTimeout time.Duration
	maxTimeout     time.Duration
}

// RegisterCLIAgentTools registers cli_agent_list and cli_agent_run on a
// service. It returns an error rather than doing nothing when there is no
// directory the delegated CLI would be allowed to run in: a tool registered
// with nowhere to run is a tool that fails on every call, and the author is
// better told at build time.
func RegisterCLIAgentTools(svc *Service, cfg CLIAgentConfig) error {
	if svc == nil {
		return errors.New("cli agent tools: nil service")
	}

	roots, err := resolveCLIAgentRoots(svc, cfg.AllowedRoots)
	if err != nil {
		return err
	}

	agents := agentexec.Discover(cfg.Binaries)
	if len(cfg.Agents) > 0 {
		allowed := make([]agentexec.Installed, 0, len(agents))
		for _, a := range agents {
			if slices.Contains(cfg.Agents, a.Name) {
				allowed = append(allowed, a)
			}
		}
		agents = allowed
	}

	r := &cliAgentRunner{
		svc:            svc,
		agents:         agents,
		registry:       agentexec.RegistryFrom(agents, agentexec.WithMCPConfig(".agentgo-cli-agent-mcp.json", true)),
		roots:          roots,
		defaultCwd:     roots[0],
		defaultTimeout: cfg.DefaultTimeout,
		maxTimeout:     cfg.MaxTimeout,
	}
	if r.defaultTimeout <= 0 {
		r.defaultTimeout = defaultCLIAgentTimeout
	}
	if r.maxTimeout <= 0 {
		r.maxTimeout = maxCLIAgentTimeout
	}
	if r.defaultTimeout > r.maxTimeout {
		r.defaultTimeout = r.maxTimeout
	}

	has := func(name string) bool {
		return svc.toolRegistry != nil && svc.toolRegistry.Has(name)
	}

	if !has("cli_agent_list") {
		svc.AddToolWithMetadata(
			"cli_agent_list",
			"List the other agent CLIs installed on this machine that you can hand a task to. "+
				"An agent appears here because its binary is installed, NOT because it is logged "+
				"in and working — the only way to find that out is to run it and read what comes "+
				"back. Use it before cli_agent_run to see which names exist.",
			map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
				listed := make([]map[string]interface{}, 0, len(r.agents))
				for _, a := range r.agents {
					entry := map[string]interface{}{
						"name":      a.Name,
						"binary":    a.Binary,
						"streaming": a.Streaming,
						"resume":    a.Resume,
					}
					if a.Version != "" {
						entry["version"] = a.Version
					}
					listed = append(listed, entry)
				}
				return toolOK(map[string]interface{}{
					"agents": listed,
					"count":  len(listed),
					"note":   "installed, not necessarily logged in; a run may still fail on authentication",
				}), nil
			},
			ToolMetadata{ReadOnly: true},
		)
	}

	if !has("cli_agent_run") {
		svc.AddToolWithMetadata(
			"cli_agent_run",
			"Hand one self-contained task to another agent CLI on this machine and wait for its "+
				"answer. The other agent runs its own tool loop with permission prompts bypassed: "+
				"it can read and write files under cwd and run commands, and it costs money on "+
				"whichever account that CLI is logged into. It cannot see this conversation, so "+
				"state the whole task in the prompt. Check `failed` in the result, not just "+
				"`ok`: these CLIs report an authentication failure as an ordinary-looking answer "+
				"and still exit successfully.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent": map[string]interface{}{
						"type":        "string",
						"description": "Which CLI to run, by name from cli_agent_list (e.g. claude, codex, gemini, cursor-agent).",
					},
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "The complete task, stated in full. The other agent starts with no context but this.",
					},
					"cwd": map[string]interface{}{
						"type":        "string",
						"description": "Directory to run in. Must be inside an allowed root; defaults to the workspace.",
					},
					"model": map[string]interface{}{
						"type":        "string",
						"description": "Model for that CLI to use. Omit to let it pick its own default.",
					},
					"resume_session_id": map[string]interface{}{
						"type":        "string",
						"description": "A session_id from an earlier cli_agent_run, to continue that conversation instead of starting fresh.",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "How long to wait before giving up and killing it.",
					},
				},
				"required": []string{"agent", "prompt"},
			},
			r.run,
			// Destructive: it spends money and writes files under cwd, so a
			// host's approval gate has to see it. Blocking on interrupt would
			// be wrong — a delegated run that the user stopped should die with
			// the parent, and pty.Run already signals its whole process group.
			ToolMetadata{Destructive: true},
		)
	}

	return nil
}

// resolveCLIAgentRoots turns configured roots into absolute, symlink-resolved
// directories.
//
// EvalSymlinks is the whole point of doing this here rather than at call time:
// on macOS a workspace under /tmp is really under /private/tmp, so a cwd check
// that compares the two textually rejects the service's own workspace. Both
// sides get resolved, once, and compared afterwards.
func resolveCLIAgentRoots(svc *Service, configured []string) ([]string, error) {
	raw := configured
	if len(raw) == 0 {
		if ws := strings.TrimSpace(svc.workspaceRoot()); ws != "" {
			raw = []string{ws}
		}
	}
	if len(raw) == 0 {
		return nil, errors.New("cli agent tools: no allowed roots — the service has no sandbox workspace, so set CLIAgentConfig.AllowedRoots")
	}

	roots := make([]string, 0, len(raw))
	for _, root := range raw {
		resolved, err := resolveExistingDir(root)
		if err != nil {
			return nil, fmt.Errorf("cli agent tools: allowed root %q: %w", root, err)
		}
		if !slices.Contains(roots, resolved) {
			roots = append(roots, resolved)
		}
	}
	return roots, nil
}

func resolveExistingDir(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return resolved, nil
}

// resolveCwd bounds where a delegated agent may run. The agent it starts has
// its approval prompts turned off, so this check is the only thing standing
// between "summarise this repo" and "summarise, and while you are there, edit,
// somewhere else entirely".
func (r *cliAgentRunner) resolveCwd(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return r.defaultCwd, nil
	}
	resolved, err := resolveExistingDir(raw)
	if err != nil {
		return "", fmt.Errorf("cwd %q cannot be used: %v", raw, err)
	}
	for _, root := range r.roots {
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("cwd %q is outside the allowed roots (%s)", raw, strings.Join(r.roots, ", "))
}

func (r *cliAgentRunner) run(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	name := toolArgString(args, "agent")
	if name == "" {
		return cliAgentFailure("", "agent is required; call cli_agent_list for the names"), nil
	}
	prompt := toolArgString(args, "prompt")
	if prompt == "" {
		return cliAgentFailure(name, "prompt is required"), nil
	}

	provider, err := r.registry.Get(name)
	if err != nil {
		return cliAgentFailure(name, fmt.Sprintf("no agent named %q is available here; call cli_agent_list", name)), nil
	}

	cwd, err := r.resolveCwd(toolArgString(args, "cwd"))
	if err != nil {
		return cliAgentFailure(name, err.Error()), nil
	}

	timeout := r.defaultTimeout
	if secs := toolArgInt(args, "timeout_seconds"); secs > 0 {
		timeout = time.Duration(secs) * time.Second
		if timeout > r.maxTimeout {
			timeout = r.maxTimeout
		}
	}

	session := provider.NewSession()
	spec, err := session.BuildCommand(ctx, agentexec.Request{
		RunID:           currentRunID(ctx),
		Prompt:          prompt,
		WorkspacePath:   cwd,
		Model:           toolArgString(args, "model"),
		ResumeSessionID: toolArgString(args, "resume_session_id"),
		// Headless posture. Sandbox false is what emits codex's
		// --skip-git-repo-check and gemini's --skip-trust; without them both
		// refuse to start in a directory that is not a trusted git repo, which
		// a scratch cwd never is.
		Sandbox: false,
		// A delegated call that can reach the operator's own MCP servers is
		// not reproducible, and booting all of them costs more than the call.
		NoMCP:          true,
		PermissionMode: agentexec.PermissionBypass,
	})
	if err != nil {
		return cliAgentFailure(name, fmt.Sprintf("could not build the %s command: %v", name, err)), nil
	}

	info := SubAgentInfo{
		ParentTaskID: currentTaskID(getCurrentSession(ctx)),
		RunID:        currentRunID(ctx),
		SubAgentID:   uuid.NewString(),
		Name:         name,
		Goal:         prompt,
		SessionID:    currentRunSessionID(ctx),
		Kind:         "cli",
		Provider:     name,
	}
	r.svc.emitObserver(func(o Observer) { o.OnSubAgentStart(ctx, info) })
	r.svc.emitProgress("tool_call", fmt.Sprintf("→ Delegating to %s: %s", name, truncateGoal(prompt, 50)), 0, "cli_agent_run")

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sink := eventSinkFromContext(ctx)
	started := time.Now()
	ptyResult, runErr := agentexecpty.Run(runCtx, agentexecpty.Command{
		Argv:    spec.Argv,
		Env:     spec.Env,
		WorkDir: spec.WorkDir,
		// Nothing is written to stdin, and that is deliberate rather than an
		// omission. Codex goes looking for more of the prompt on stdin when it
		// is handed a pipe — "Reading additional input from stdin…", then it
		// waits for a person who is never coming — and the fix for that is to
		// give it /dev/null. Under a pty there is no pipe to notice: stdin is
		// a terminal. Writing a synthetic EOF anyway makes things worse, not
		// safer, because the line discipline echoes whatever we write back
		// into the same stream we are parsing: a ^D sent at startup arrives
		// glued to the front of the first JSON frame, and the frame stops
		// being JSON. If some future CLI does sit waiting on the terminal, the
		// timeout below bounds it and says so.
	}, func(chunk []byte) {
		events, _ := session.ParseChunk(chunk)
		r.forward(sink, name, events)
	})

	result, tail, finalizeErr := session.Finalize(ctx, ptyResult.Output, ptyResult.ExitCode)
	r.forward(sink, name, tail)
	elapsed := time.Since(started)

	out := CLIAgentRunResult{
		Agent:     name,
		SessionID: session.SessionID(),
		Summary:   result.Summary,
		Failed:    result.Failed,
		ExitCode:  ptyResult.ExitCode,
		Duration:  elapsed.Milliseconds(),
		Input:     result.Usage.InputTokens,
		Output:    result.Usage.OutputTokens,
		Cache:     result.Usage.CacheTokens,
		CostUSD:   result.Usage.EstimatedCostUSD,
		Model:     result.Usage.Model,
	}

	// Four separate ways this can have gone wrong, and only the last is the
	// obvious one. The timeout has to be named as a timeout — a killed run
	// looks like a crash from the exit code alone. Failed has to outrank the
	// exit code, because the authentication case sets one and not the other.
	var failure string
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		failure = fmt.Sprintf("%s did not finish within %s and was stopped", name, timeout)
	case runErr != nil:
		failure = fmt.Sprintf("could not run %s: %v", name, runErr)
	case finalizeErr != nil:
		failure = fmt.Sprintf("could not read what %s produced: %v", name, finalizeErr)
	case result.Failed:
		failure = cliAgentFailureText(name, result.Summary, ptyResult.Output)
	case ptyResult.ExitCode != 0:
		failure = cliAgentFailureText(name, result.Summary, ptyResult.Output)
	}

	var endErr error
	if failure != "" {
		out.Failed = true
		endErr = errors.New(failure)
	}
	r.svc.emitObserver(func(o Observer) { o.OnSubAgentEnd(ctx, info, out, endErr) })
	r.svc.emitProgress("tool_result", fmt.Sprintf("✓ %s finished", name), 0, "cli_agent_run")

	payload := map[string]interface{}{
		"ok":          failure == "",
		"agent":       out.Agent,
		"session_id":  out.SessionID,
		"summary":     out.Summary,
		"failed":      out.Failed,
		"exit_code":   out.ExitCode,
		"duration_ms": out.Duration,
		"usage": map[string]interface{}{
			"input":    out.Input,
			"output":   out.Output,
			"cache":    out.Cache,
			"cost_usd": out.CostUSD,
		},
		"error": failure,
	}
	return payload, nil
}

// forward pushes the delegated agent's events into the parent run so a UI
// watching the parent sees the work happen instead of a four-minute silence.
//
// eventSinkFromContext is the seam: executeToolOrHandoff puts the runtime's
// own forwarder on the context of every tool call, which is the same route a
// sub-agent's events take. Text goes through as a partial — that is what a
// host already renders as the answer being written. Tool calls go through as
// state updates rather than tool_call events, because a host that pairs a
// tool_call with its result would be left holding one half of a pair: the
// delegated CLI's tool results are frames about its own internal loop, and
// replaying them into the parent's tool timeline would claim this agent ran
// tools it never ran.
func (r *cliAgentRunner) forward(sink func(*Event), name string, events []agentexec.Event) {
	if sink == nil || len(events) == 0 {
		return
	}
	for _, e := range events {
		switch e.Type {
		case agentexec.EventAgentMessage:
			if e.Payload["role"] != "assistant" {
				continue
			}
			text, _ := e.Payload["text"].(string)
			if strings.TrimSpace(text) == "" {
				continue
			}
			sink(&Event{Type: EventTypePartial, AgentName: name, Content: text})
		case agentexec.EventToolCall:
			tool := cliAgentToolName(e.Payload)
			sink(&Event{
				Type:      EventTypeStateUpdate,
				AgentName: name,
				ToolName:  tool,
				Content:   name + " → " + tool,
			})
		}
	}
}

// cliAgentToolName digs the tool's name out of a provider's tool-call payload.
// Claude and cursor-agent hand over the raw tool_use block ("name"); codex's
// item shape uses "name" too but not always, and gemini's frame keys it
// differently again. Unknown shapes get a placeholder rather than an empty
// string, so the forwarded event still reads as something happening.
func cliAgentToolName(payload map[string]any) string {
	for _, key := range []string{"name", "tool_name", "tool", "command"} {
		if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "tool"
}

// cliAgentFailureText builds the message a model reads when a delegated run
// failed. The CLI's own words come first when it produced any — an expired
// login, a rate limit and an ineligible account are three different problems
// and only the CLI knows which one it hit — with the tail of the raw output as
// the fallback, because a run that failed before it emitted a single JSON
// frame has nothing else to offer.
func cliAgentFailureText(name, summary string, raw []byte) string {
	if s := strings.TrimSpace(summary); s != "" {
		return fmt.Sprintf("%s reported a failure: %s", name, s)
	}
	tail := strings.TrimSpace(string(raw))
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}
	if tail == "" {
		return fmt.Sprintf("%s failed and produced no output", name)
	}
	return fmt.Sprintf("%s failed; its output ended with: %s", name, tail)
}

// cliAgentFailure is the shape returned for a call that never started — a bad
// name, a cwd outside the roots. It carries the same keys as a real run so the
// model never has to branch on which kind of failure it is looking at.
func cliAgentFailure(name, msg string) map[string]interface{} {
	return map[string]interface{}{
		"ok":          false,
		"agent":       name,
		"session_id":  "",
		"summary":     "",
		"failed":      true,
		"exit_code":   -1,
		"duration_ms": int64(0),
		"usage": map[string]interface{}{
			"input":    int64(0),
			"output":   int64(0),
			"cache":    int64(0),
			"cost_usd": float64(0),
		},
		"error": msg,
	}
}
