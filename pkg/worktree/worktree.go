// Package worktree provides thin, dependency-free helpers for creating and
// disposing of git worktrees. It exists so subagents (and any other isolated
// unit of work) can operate inside a throwaway checkout of a repository instead
// of mutating the caller's working tree directly.
//
// The package shells out to the `git` CLI rather than depending on a git
// library — this keeps it zero-dependency and matches how the rest of AgentGo
// drives external tools (see pkg/sandbox). It has NO dependency on pkg/agent.
//
//	wt, err := worktree.Create(ctx, worktree.Options{}) // worktree off cwd's HEAD
//	if err != nil { ... }
//	defer wt.RemoveIfClean(ctx)
//	// ... do work inside wt.Path ...
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree describes a single git worktree created by Create.
type Worktree struct {
	// Path is the absolute filesystem path of the worktree checkout.
	Path string
	// Branch is the branch checked out in the worktree. Empty when the
	// worktree was created detached (Options.Detach) or off a bare ref.
	Branch string
	// RepoDir is the absolute path of the repository the worktree belongs to
	// (the directory git commands are run with `-C`).
	RepoDir string
}

// Options configures Create.
type Options struct {
	// RepoDir is the repository the worktree is added to. When empty, the
	// current working directory is used. It must be inside a git work tree.
	RepoDir string
	// Ref is the branch/commit/ref the worktree is based on. When empty, HEAD
	// of RepoDir is used.
	Ref string
	// Path is the directory the worktree is checked out into. When empty, a
	// fresh temp directory under os.TempDir() is created.
	Path string
	// Branch, when non-empty, creates a new branch (git worktree add -b) for
	// the worktree instead of checking out Ref directly. Mutually exclusive
	// with Detach.
	Branch string
	// Detach checks the worktree out in detached-HEAD state (no branch). Useful
	// for read-only/throwaway work. Ignored when Branch is set.
	Detach bool
}

// Option mutates Options. Provided for the functional-option call style used by
// the agent wiring (WithSubAgentWorktree(repoDir, opts...)).
type Option func(*Options)

// WithRef bases the worktree on the given branch/commit/ref.
func WithRef(ref string) Option { return func(o *Options) { o.Ref = ref } }

// WithPath checks the worktree out into the given directory.
func WithPath(path string) Option { return func(o *Options) { o.Path = path } }

// WithBranch creates a new branch for the worktree.
func WithBranch(branch string) Option { return func(o *Options) { o.Branch = branch } }

// WithDetach checks the worktree out in detached-HEAD state.
func WithDetach() Option { return func(o *Options) { o.Detach = true } }

// Available reports whether the git CLI is on PATH.
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// runGit executes `git -C dir args...` and returns trimmed stdout. On failure
// the returned error includes the combined stderr for diagnosis.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git not found on PATH: %w", err)
	}
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Create adds a new git worktree according to opts and returns a handle to it.
func Create(ctx context.Context, opts Options) (*Worktree, error) {
	repoDir := strings.TrimSpace(opts.RepoDir)
	if repoDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("worktree: resolve cwd: %w", err)
		}
		repoDir = cwd
	}
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("worktree: resolve repo dir %q: %w", repoDir, err)
	}
	repoDir = abs

	// Verify repoDir is inside a git work tree before doing anything else, so
	// we fail with a clear message rather than a cryptic git error.
	if out, err := runGit(ctx, repoDir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, fmt.Errorf("worktree: %q is not inside a git work tree: %w", repoDir, err)
	} else if strings.TrimSpace(out) != "true" {
		return nil, fmt.Errorf("worktree: %q is not inside a git work tree (rev-parse returned %q)", repoDir, out)
	}

	path := strings.TrimSpace(opts.Path)
	if path == "" {
		dir, err := os.MkdirTemp("", "agentgo-wt-*")
		if err != nil {
			return nil, fmt.Errorf("worktree: create temp dir: %w", err)
		}
		// git worktree add requires the target path NOT to exist yet, so remove
		// the freshly-created temp dir (we keep its unique name).
		if err := os.Remove(dir); err != nil {
			return nil, fmt.Errorf("worktree: clear temp dir: %w", err)
		}
		path = dir
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}

	args := []string{"worktree", "add"}
	if opts.Branch != "" {
		args = append(args, "-b", opts.Branch)
	} else if opts.Detach {
		args = append(args, "--detach")
	}
	args = append(args, path)
	if ref := strings.TrimSpace(opts.Ref); ref != "" {
		args = append(args, ref)
	}

	if _, err := runGit(ctx, repoDir, args...); err != nil {
		return nil, fmt.Errorf("worktree: add: %w", err)
	}

	wt := &Worktree{Path: path, RepoDir: repoDir, Branch: opts.Branch}
	if wt.Branch == "" && !opts.Detach {
		// Best-effort: record the branch actually checked out.
		if br, err := runGit(ctx, path, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && br != "HEAD" {
			wt.Branch = br
		}
	}
	return wt, nil
}

// Remove deletes the worktree. When force is true a dirty worktree is removed
// anyway (git worktree remove --force).
func (w *Worktree) Remove(ctx context.Context, force bool) error {
	if w == nil || strings.TrimSpace(w.Path) == "" {
		return errors.New("worktree: nil or empty worktree")
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, w.Path)
	if _, err := runGit(ctx, w.RepoDir, args...); err != nil {
		return fmt.Errorf("worktree: remove %q: %w", w.Path, err)
	}
	return nil
}

// IsClean reports whether the worktree has no uncommitted changes (a porcelain
// status with no output).
func (w *Worktree) IsClean(ctx context.Context) (bool, error) {
	if w == nil || strings.TrimSpace(w.Path) == "" {
		return false, errors.New("worktree: nil or empty worktree")
	}
	out, err := runGit(ctx, w.Path, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("worktree: status %q: %w", w.Path, err)
	}
	return strings.TrimSpace(out) == "", nil
}

// RemoveIfClean removes the worktree only when it is clean. A dirty worktree is
// left in place (so a human can inspect it) and removed=false is returned.
func (w *Worktree) RemoveIfClean(ctx context.Context) (removed bool, err error) {
	clean, err := w.IsClean(ctx)
	if err != nil {
		return false, err
	}
	if !clean {
		return false, nil
	}
	if err := w.Remove(ctx, false); err != nil {
		return false, err
	}
	return true, nil
}
