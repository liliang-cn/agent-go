package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

// maxWorkspaceSnapshotBytes caps how large a workspace snapshot we embed in a
// checkpoint. Beyond this the snapshot is skipped (the run still checkpoints
// its message history) to keep the checkpoint DB from bloating on tasks that
// produce huge artifacts.
const maxWorkspaceSnapshotBytes = 32 << 20 // 32 MiB

// ArchiveEntry describes one file inside a workspace snapshot archive.
type ArchiveEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir"`
}

// snapshotWorkspaceBytes asks the sandbox for a gzip-tar snapshot of its
// workspace and returns the bytes, or nil (no error) when there is no sandbox
// or the snapshot exceeds maxWorkspaceSnapshotBytes. Best-effort: a snapshot
// failure must never break checkpointing, so it returns nil on any error.
func snapshotWorkspaceBytes(ctx context.Context, sb sandbox.Sandbox) []byte {
	if sb == nil {
		return nil
	}
	path, err := sb.Snapshot(ctx)
	if err != nil || strings.TrimSpace(path) == "" {
		return nil
	}
	defer os.Remove(path)
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxWorkspaceSnapshotBytes {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

// restoreWorkspaceBytes writes the archive to a temp file and restores it into
// the sandbox workspace.
func restoreWorkspaceBytes(ctx context.Context, sb sandbox.Sandbox, data []byte) error {
	if sb == nil || len(data) == 0 {
		return nil
	}
	tmp, err := os.CreateTemp("", "agentgo-ws-restore-*.tgz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return sb.Restore(ctx, tmp.Name())
}

// ListArchive parses a gzip-tar workspace archive (as stored in a checkpoint's
// Workspace field) and returns its file entries. Format-agnostic standard
// tar.gz, so it works regardless of which sandbox backend produced it.
func ListArchive(data []byte) ([]ArchiveEntry, error) {
	if len(data) == 0 {
		return nil, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out []ArchiveEntry
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if name == "" || name == "." {
			continue
		}
		out = append(out, ArchiveEntry{
			Path: name,
			Size: hdr.Size,
			Dir:  hdr.FileInfo().IsDir(),
		})
	}
	return out, nil
}

// ExtractArchive writes every file in a gzip-tar workspace archive into destDir,
// rejecting path traversal. Used by `task artifacts --extract`.
func ExtractArchive(data []byte, destDir string) error {
	if len(data) == 0 {
		return nil
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		clean := filepath.Clean(strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./"))
		if clean == "" || clean == "." {
			continue
		}
		target := filepath.Join(absDest, clean)
		if rel, err := filepath.Rel(absDest, target); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("refusing path traversal in archive: %s", hdr.Name)
		}
		if hdr.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // size is bounded by maxWorkspaceSnapshotBytes at write time
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}
