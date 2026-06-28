package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepo creates a fresh git repo with one commit and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "initial")
	return dir
}

func TestWorktreeLifecycle(t *testing.T) {
	if !Available() {
		t.Skip("git not available on PATH")
	}
	ctx := context.Background()
	repo := initRepo(t)

	// (c) Create → assert the worktree path exists and is a real worktree.
	wt, err := Create(ctx, Options{RepoDir: repo, Detach: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info, err := os.Stat(wt.Path); err != nil || !info.IsDir() {
		t.Fatalf("worktree path %q not a directory: %v", wt.Path, err)
	}
	// A worktree checkout carries a `.git` file pointing back at the repo.
	if _, err := os.Stat(filepath.Join(wt.Path, ".git")); err != nil {
		t.Fatalf("worktree missing .git marker: %v", err)
	}
	listOut, err := runGit(ctx, repo, "worktree", "list")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if !containsPath(listOut, wt.Path) {
		t.Fatalf("worktree %q not listed in:\n%s", wt.Path, listOut)
	}

	// Initially clean.
	if clean, err := wt.IsClean(ctx); err != nil || !clean {
		t.Fatalf("fresh worktree should be clean: clean=%v err=%v", clean, err)
	}

	// (d) Write a file inside → IsClean()==false.
	scratch := filepath.Join(wt.Path, "solution.txt")
	if err := os.WriteFile(scratch, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}
	if clean, err := wt.IsClean(ctx); err != nil || clean {
		t.Fatalf("dirty worktree should report not clean: clean=%v err=%v", clean, err)
	}

	// (e) RemoveIfClean leaves a dirty worktree in place and returns false.
	removed, err := wt.RemoveIfClean(ctx)
	if err != nil {
		t.Fatalf("RemoveIfClean (dirty): %v", err)
	}
	if removed {
		t.Fatalf("RemoveIfClean removed a dirty worktree")
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("dirty worktree was removed but should have been kept: %v", err)
	}

	// (f) Commit the change to clean it, then RemoveIfClean removes it.
	commit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wt.Path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit("config", "user.email", "test@example.com")
	commit("config", "user.name", "Test")
	commit("add", "-A")
	commit("commit", "-m", "add solution")

	if clean, err := wt.IsClean(ctx); err != nil || !clean {
		t.Fatalf("committed worktree should be clean: clean=%v err=%v", clean, err)
	}
	removed, err = wt.RemoveIfClean(ctx)
	if err != nil {
		t.Fatalf("RemoveIfClean (clean): %v", err)
	}
	if !removed {
		t.Fatalf("RemoveIfClean should have removed a clean worktree")
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("clean worktree should be gone, stat err=%v", err)
	}
}

func TestCreateRejectsNonRepo(t *testing.T) {
	if !Available() {
		t.Skip("git not available on PATH")
	}
	dir := t.TempDir() // plain dir, not a git repo
	if _, err := Create(context.Background(), Options{RepoDir: dir}); err == nil {
		t.Fatalf("expected error creating worktree outside a git repo")
	}
}

func TestCreateWithBranch(t *testing.T) {
	if !Available() {
		t.Skip("git not available on PATH")
	}
	ctx := context.Background()
	repo := initRepo(t)
	wt, err := Create(ctx, Options{RepoDir: repo, Branch: "feature/x"})
	if err != nil {
		t.Fatalf("Create with branch: %v", err)
	}
	defer wt.Remove(ctx, true)
	if wt.Branch != "feature/x" {
		t.Fatalf("expected branch feature/x, got %q", wt.Branch)
	}
}

func containsPath(haystack, needle string) bool {
	// git worktree list prints absolute paths; resolve symlinks on both sides
	// so /tmp vs /private/tmp on macOS doesn't trip the check.
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	needle = resolve(needle)
	for _, line := range splitLines(haystack) {
		fields := splitFields(line)
		if len(fields) == 0 {
			continue
		}
		if resolve(fields[0]) == needle {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
