package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

// What the workspace looks like, handed to a segment that did not build it.
//
// A task that runs for hours produces files, and on a coding task the files
// *are* the state — far more of it than any plan note could carry. But a
// segment starts a fresh session, so it opens with no idea any of them exist.
// It either asks (spending rounds rediscovering its own output) or, worse,
// assumes an empty workspace and starts the work again.
//
// So the same run-start hand-off that carries the plan carries an inventory of
// the workspace. It is deliberately an inventory and not content: paths, sizes
// and modification times are enough for the model to know what to open, and
// cheap enough to sit in a prompt that must stay byte-stable for a whole
// segment.

const (
	// workspaceHandoffMaxEntries caps the inventory. Past a couple of hundred
	// files a listing stops orienting anyone and starts being a wall the model
	// reads on every turn; what it needs is enough to know where to look.
	workspaceHandoffMaxEntries = 200

	// workspaceHandoffMaxDepth bounds the walk. Deep trees are usually
	// dependencies rather than work product, and the point is orientation.
	workspaceHandoffMaxDepth = 4
)

// workspaceHandoffSkip are directories never worth listing: they are large,
// generated, and never what the agent needs to be reminded of.
var workspaceHandoffSkip = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".cache": true, "target": true, ".agentgo": true,
}

// workspaceSummaryForRun renders an inventory of the sandbox workspace, or ""
// when there is no sandbox or nothing in it.
//
// Best-effort throughout: a workspace that cannot be walked is a hand-off
// without an inventory, never a run that fails to start.
func (s *Service) workspaceSummaryForRun(ctx context.Context) string {
	if s == nil {
		return ""
	}
	sb := s.Sandbox()
	if sb == nil {
		return ""
	}
	entries := walkWorkspace(ctx, sb, "", 0)
	if len(entries) == 0 {
		return ""
	}
	truncated := false
	if len(entries) > workspaceHandoffMaxEntries {
		entries = entries[:workspaceHandoffMaxEntries]
		truncated = true
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	var b strings.Builder
	b.WriteString("These files are already in the workspace. They are the work so far — read before rewriting.\n\n")
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(&b, "  %s/\n", e.Path)
			continue
		}
		fmt.Fprintf(&b, "  %-52s %8d bytes  %s\n", e.Path, e.Size, e.ModTime.Format(time.RFC3339))
	}
	if truncated {
		fmt.Fprintf(&b, "  … more than %d entries; use fs_list and fs_glob for the rest\n",
			workspaceHandoffMaxEntries)
	}
	return strings.TrimRight(b.String(), "\n")
}

// walkWorkspace lists a directory and its children breadth-first, bounded by
// depth and by the entry cap.
func walkWorkspace(ctx context.Context, sb sandbox.Sandbox, dir string, depth int) []sandbox.FileInfo {
	if depth > workspaceHandoffMaxDepth {
		return nil
	}
	listed, err := sb.List(ctx, dir)
	if err != nil {
		return nil
	}
	out := make([]sandbox.FileInfo, 0, len(listed))
	for _, e := range listed {
		if strings.HasPrefix(e.Name, ".") && e.Name != "." {
			// Dotfiles are usually configuration the agent wrote deliberately,
			// so keep them — but never walk into a dot directory.
			if e.IsDir {
				continue
			}
		}
		if e.IsDir && workspaceHandoffSkip[e.Name] {
			continue
		}
		out = append(out, e)
		if len(out) > workspaceHandoffMaxEntries {
			return out
		}
		if e.IsDir {
			out = append(out, walkWorkspace(ctx, sb, e.Path, depth+1)...)
			if len(out) > workspaceHandoffMaxEntries {
				return out
			}
		}
	}
	return out
}
