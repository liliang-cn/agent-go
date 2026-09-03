package cliagents

import (
	"context"
	"sort"

	"github.com/liliang-cn/agentcli/cliagent"
)

// cursor-agent is the one of the four that cliagent has no provider for, and
// it needs about fifteen lines rather than a fourth parser: Cursor's
// `--output-format stream-json` is Claude Code's dialect, frame for frame —
// a `system` init, `assistant` frames whose message.content holds text and
// tool_use blocks, a `result` frame carrying is_error and session_id. So the
// session here is a claude session with its BuildCommand replaced, and every
// hard-won thing claude.go knows about that stream — that the model's answer
// is the assistant-role message and not the ten lifecycle frames around it,
// that is_error is a verdict the exit code does not carry — applies unchanged.
//
// Only the argv differs, and it differs in every flag: cursor-agent spells
// print `-p/--print` but has no `--verbose`, spells bypass `--force`, and
// spells "this directory is fine, do not ask" `--trust`.

type cursorProvider struct {
	name   string
	binary string
	parser cliagent.Provider
}

// newCursor returns a Provider for cursor-agent under the given name.
func newCursor(name, binary string) cliagent.Provider {
	return &cursorProvider{
		name:   name,
		binary: binary,
		parser: cliagent.NewClaude(cliagent.WithName(name)),
	}
}

func (p *cursorProvider) Name() string { return p.name }

func (p *cursorProvider) Capabilities() cliagent.Capabilities {
	// No Plugins and no MCP: cursor-agent has its own notion of both, this
	// package does not pass either, and claiming them would invite a caller to
	// send plugin dirs that BuildCommand silently drops.
	return cliagent.Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *cursorProvider) NewSession() cliagent.Session {
	return &cursorSession{Session: p.parser.NewSession(), binary: p.binary}
}

// cursorSession embeds the claude session so ParseChunk, Finalize and
// SessionID come along, and overrides only the command.
type cursorSession struct {
	cliagent.Session
	binary string
}

func (s *cursorSession) BuildCommand(_ context.Context, req cliagent.Request) (cliagent.CommandSpec, error) {
	// There is no --append-system-prompt here, so policy text goes where codex
	// and gemini put it: in front of the prompt.
	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.Prompt
	}

	argv := []string{s.binary, "--print", "--output-format", "stream-json"}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--resume", req.ResumeSessionID)
	}
	if req.PermissionMode == cliagent.PermissionBypass {
		argv = append(argv, "--force")
	}
	if !req.Sandbox {
		// The headless posture, same as cliagent's --skip-trust /
		// --skip-git-repo-check: in a scratch directory the CLI has never seen,
		// the trust prompt is a prompt nobody is there to answer.
		argv = append(argv, "--trust", "--sandbox", "disabled")
	}
	argv = append(argv, req.ExtraArgs...)
	// The prompt is a positional argument and stays last, with no `--` in
	// front of it: `cursor-agent -p "<prompt>"` is the documented invocation
	// and the only one that has been seen to work.
	argv = append(argv, prompt)

	return cliagent.CommandSpec{Argv: argv, Env: cursorEnv(req), WorkDir: req.WorkspacePath}, nil
}

// cursorEnv renders Request.Env as the "KEY=VALUE" overrides pty.Run expects.
// cliagent keeps its own mergeEnv unexported, and there is no base env to
// merge here, so this is the whole of it.
func cursorEnv(req cliagent.Request) []string {
	if len(req.Env) == 0 {
		return nil
	}
	out := make([]string, 0, len(req.Env))
	for k, v := range req.Env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
