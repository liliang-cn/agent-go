package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
	"github.com/liliang-cn/agent-go/v2/pkg/worktree"
)

// WorktreeSpec configures git-worktree isolation for a sub-agent. When attached
// to a SubAgentConfig (via WithSubAgentWorktree), the sub-agent runs inside a
// freshly-created worktree of RepoDir and its filesystem tools are rooted at the
// worktree checkout, so writes land there instead of in the parent repository.
type WorktreeSpec struct {
	// RepoDir is the repository the worktree is created from. Defaults to the
	// current working directory when empty.
	RepoDir string
	// Options are passed through to worktree.Create (ref, branch, detach, path).
	Options []worktree.Option
	// KeepOnDirty, when true, leaves the worktree on disk if it still has
	// uncommitted changes when the sub-agent finishes — so a human can inspect
	// the produced work. A clean worktree is always removed. When false, the
	// worktree is force-removed regardless.
	KeepOnDirty bool
}

// WithSubAgentWorktree runs the sub-agent inside an isolated git worktree of
// repoDir. The sub-agent's fs_* / bash / shell_* tools are rooted at the
// worktree path, so anything it writes lands in the throwaway checkout rather
// than the parent repo. On completion the worktree is removed if clean; a dirty
// worktree is kept by default (set KeepOnDirty=false via a manual WorktreeSpec
// to force removal).
//
//	sa := agent.NewSubAgent(cfg,
//	    agent.WithSubAgentService(parent),
//	    agent.WithSubAgentWorktree(repoDir, worktree.WithDetach()))
//	res, err := sa.Run(ctx)
//	// sa.WorktreePath() reports where the work happened.
func WithSubAgentWorktree(repoDir string, opts ...worktree.Option) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Worktree = &WorktreeSpec{
			RepoDir:     repoDir,
			Options:     opts,
			KeepOnDirty: true,
		}
	}
}

// WorktreePath returns the path of the worktree this sub-agent ran in, or "" if
// it did not use one (or has not started yet).
func (sa *SubAgent) WorktreePath() string {
	sa.mu.RLock()
	defer sa.mu.RUnlock()
	return sa.worktreePath
}

// worktreeRuntime holds the live state for a sub-agent's worktree isolation.
type worktreeRuntime struct {
	wt           *worktree.Worktree
	sandbox      sandbox.Sandbox
	childService *Service
	parentSvc    *Service
	keepOnDirty  bool
}

// setupWorktree creates the worktree, builds a worktree-rooted sandbox + child
// service, and points the sub-agent's config.Service at the child so all tool
// execution (and the fs/bash tools in particular) operate inside the worktree.
func (sa *SubAgent) setupWorktree(ctx context.Context) (*worktreeRuntime, error) {
	spec := sa.config.Worktree
	if spec == nil {
		return nil, nil
	}
	parent := sa.config.Service
	if parent == nil {
		return nil, fmt.Errorf("worktree isolation requires a parent Service (WithSubAgentService)")
	}

	wt, err := worktree.Create(ctx, applyWorktreeOptions(spec))
	if err != nil {
		return nil, err
	}

	// Root a local sandbox at the worktree checkout. We do NOT let the sandbox
	// own/remove the directory — the worktree owns it and is removed via git.
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(wt.Path))
	if err != nil {
		_ = wt.Remove(context.WithoutCancel(ctx), true)
		return nil, fmt.Errorf("root sandbox at worktree: %w", err)
	}

	// Build a child service that reuses the parent's model brain but exposes the
	// worktree-rooted sandbox tools. Tool closures bind to the sandbox at
	// registration time (RegisterSandboxTools), so a fresh service is the clean
	// way to re-root fs/bash without mutating the parent.
	child, err := New(sa.config.Agent.Name()).
		WithLLM(parent.LLM).
		WithConfig(loadChildConfig()).
		WithSandbox(sb).
		Build()
	if err != nil {
		_ = wt.Remove(context.WithoutCancel(ctx), true)
		return nil, fmt.Errorf("build worktree child service: %w", err)
	}

	// Carry over the parent's output lints so verifiers still apply to the
	// isolated run.
	copyOutputLints(parent, child, sa.config.Agent.Name())

	sa.mu.Lock()
	sa.config.Service = child
	sa.worktreePath = wt.Path
	sa.mu.Unlock()

	sa.emitProgress(fmt.Sprintf("Worktree ready at %s (branch=%s)", wt.Path, wt.Branch))

	return &worktreeRuntime{
		wt:           wt,
		sandbox:      sb,
		childService: child,
		parentSvc:    parent,
		keepOnDirty:  spec.KeepOnDirty,
	}, nil
}

// teardownWorktree restores the parent service and removes the worktree
// according to the KeepOnDirty policy. Best-effort; errors are surfaced as
// progress events only.
func (sa *SubAgent) teardownWorktree(ctx context.Context) {
	rt := sa.activeWorktree
	if rt == nil {
		return
	}
	// Restore the parent service reference.
	sa.mu.Lock()
	if rt.parentSvc != nil {
		sa.config.Service = rt.parentSvc
	}
	sa.mu.Unlock()

	if rt.sandbox != nil {
		_ = rt.sandbox.Close()
	}

	if rt.wt == nil {
		return
	}
	// NOTE: teardown runs in Run's defer chain, potentially AFTER the
	// progress/event channels have been closed, so we must not call
	// emitProgress here — log via slog instead.
	if rt.keepOnDirty {
		removed, err := rt.wt.RemoveIfClean(ctx)
		switch {
		case err != nil:
			slog.Default().Warn("worktree cleanup error", "err", err, "path", rt.wt.Path)
		case removed:
			slog.Default().Info("worktree was clean and removed", "path", rt.wt.Path)
		default:
			slog.Default().Info("worktree kept for inspection (dirty)", "path", rt.wt.Path)
		}
		return
	}
	if err := rt.wt.Remove(ctx, true); err != nil {
		slog.Default().Warn("worktree force-remove error", "err", err, "path", rt.wt.Path)
	} else {
		slog.Default().Info("worktree force-removed", "path", rt.wt.Path)
	}
}

func applyWorktreeOptions(spec *WorktreeSpec) worktree.Options {
	opts := worktree.Options{RepoDir: spec.RepoDir}
	for _, o := range spec.Options {
		o(&opts)
	}
	return opts
}

// loadChildConfig loads the default config for the worktree child service. Any
// load error falls back to a zero config (Build will load its own default).
func loadChildConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	return cfg
}

// copyOutputLints re-registers the parent's lints (global + agent-scoped) on the
// child service so verifiers continue to apply inside the worktree run. Lints
// are identified by name; the registry is idempotent by name so duplicates are
// harmless.
func copyOutputLints(parent, child *Service, agentName string) {
	if parent == nil || child == nil {
		return
	}
	src := parent.OutputLints()
	dst := child.OutputLints()
	if src == nil || dst == nil {
		return
	}
	// The registry exposes only names, not the lint objects. We re-wire by
	// wrapping a passthrough that delegates to the parent's Run for the agent —
	// but since lints are value types, the simplest faithful copy is to re-run
	// the parent registry from the child via a bridge lint.
	names := src.Names(agentName)
	if len(names) == 0 {
		return
	}
	dst.RegisterForAgent(agentName, LintFunc{
		NameValue: "worktree_parent_lints",
		Fn: func(text string, lc LintContext) (bool, string) {
			if v := src.Run(text, lc); v != nil {
				return false, fmt.Sprintf("[%s] %s", v.LintName, v.Reason)
			}
			return true, ""
		},
	})
}
