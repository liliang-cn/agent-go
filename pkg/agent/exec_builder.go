package agent

import (
	"context"

	"github.com/liliang-cn/agent-go/v2/pkg/browser"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

// AutonomyProfile configures long-horizon autonomous execution. Zero value =
// framework defaults (max ~20 tool rounds, lint retry budget 2, no scratchpad).
type AutonomyProfile struct {
	// MaxRounds is the default per-run tool-round budget used when a run does
	// not set RunConfig.MaxTurns (via WithMaxTurns). Autonomous tasks often
	// need dozens of rounds; the framework default is 20. 0 = leave default.
	MaxRounds int

	// LintRetryBudget overrides how many times a single turn may be rejected by
	// an output lint and re-prompted before the task is blocked. Framework
	// default is 2. 0 = leave default.
	LintRetryBudget int

	// Scratchpad, when true, registers the scratchpad_* tools so the agent can
	// maintain a persistent todo/notes list across a long run.
	Scratchpad bool
}

// WithSandbox attaches an execution sandbox (pkg/sandbox) and registers the
// fs_* / bash / shell_* tools on the service. The caller owns the sandbox
// lifecycle (call sb.Close() when done).
//
//	sb, _ := sandbox.NewLocal()
//	defer sb.Close()
//	svc, _ := agent.New("worker").WithSandbox(sb).Build()
func (b *Builder) WithSandbox(sb sandbox.Sandbox) *Builder {
	b.sandbox = sb
	return b
}

// WithBrowser attaches a browser (pkg/browser) and registers the browser_*
// tools. If a sandbox is also configured, browser_download writes into the
// sandbox workspace. The caller owns the browser lifecycle (br.Close()).
func (b *Builder) WithBrowser(br browser.Browser) *Builder {
	b.browser = br
	return b
}

// WithVision enables multimodal image feedback: tool results that carry image
// data (browser_screenshot, image fs_read) are surfaced to the model as image
// content parts when the model supports vision. Safe to enable on text-only
// models (it is a no-op there).
func (b *Builder) WithVision(on bool) *Builder {
	b.enableVision = on
	return b
}

// WithDeliverables registers the list_deliverables tool, which scans the
// sandbox workspace for produced artifacts. Requires WithSandbox.
func (b *Builder) WithDeliverables(on bool) *Builder {
	b.enableDeliver = on
	return b
}

// WithAutonomy configures long-horizon execution (round budget, lint retry
// budget, scratchpad). See AutonomyProfile.
func (b *Builder) WithAutonomy(p AutonomyProfile) *Builder {
	b.autonomy = p
	return b
}

// Sandbox returns the configured execution sandbox, or nil if none.
func (s *Service) Sandbox() sandbox.Sandbox { return s.execSandbox }

// Browser returns the configured browser, or nil if none.
func (s *Service) Browser() browser.Browser { return s.execBrowser }

// VisionEnabled reports whether multimodal image feedback is active.
func (s *Service) VisionEnabled() bool { return s.visionEnabled }

// Deliverables scans the configured sandbox workspace for produced artifacts.
// Returns an empty slice (no error) when no sandbox is configured.
func (s *Service) Deliverables(ctx context.Context) ([]Deliverable, error) {
	if s.execSandbox == nil {
		return nil, nil
	}
	return ScanDeliverables(ctx, s.execSandbox)
}
