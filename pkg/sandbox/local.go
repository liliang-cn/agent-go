package sandbox

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LocalSandbox runs commands and file operations directly on the host, with all
// file paths jailed under a workspace root. It has no external dependencies
// beyond creack/pty (for shell sessions).
type LocalSandbox struct {
	root    string
	ownRoot bool // true when we created the root and should remove it on Close
	env     map[string]string
}

// LocalOption configures a LocalSandbox.
type LocalOption func(*localConfig)

type localConfig struct {
	root string
	env  map[string]string
}

// WithWorkspace sets the workspace root directory. The directory is created if
// it does not exist. When unset, a temp directory is created and removed on Close.
func WithWorkspace(dir string) LocalOption {
	return func(c *localConfig) { c.root = dir }
}

// WithEnv adds environment variables applied to every Exec/Shell invocation.
func WithEnv(env map[string]string) LocalOption {
	return func(c *localConfig) {
		if c.env == nil {
			c.env = map[string]string{}
		}
		for k, v := range env {
			c.env[k] = v
		}
	}
}

// NewLocal constructs a LocalSandbox. With no options the workspace is a fresh
// temp directory removed on Close.
func NewLocal(opts ...LocalOption) (*LocalSandbox, error) {
	cfg := &localConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	ownRoot := false
	root := strings.TrimSpace(cfg.root)
	if root == "" {
		dir, err := os.MkdirTemp("", "agentgo-sbx-*")
		if err != nil {
			return nil, fmt.Errorf("create workspace: %w", err)
		}
		root = dir
		ownRoot = true
	} else {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, fmt.Errorf("create workspace: %w", err)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	// Resolve symlinks so prefix checks in resolveInWorkspace are reliable
	// (e.g. macOS /tmp -> /private/tmp).
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return &LocalSandbox{root: abs, ownRoot: ownRoot, env: cfg.env}, nil
}

// Workspace returns the absolute workspace root.
func (s *LocalSandbox) Workspace() string { return s.root }

// resolveInWorkspace converts a workspace-relative (or absolute-within-root)
// path into an absolute host path, rejecting any path that escapes the root.
func (s *LocalSandbox) resolveInWorkspace(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return s.root, nil
	}
	// Treat an absolute input as relative to the workspace root rather than the
	// host filesystem: strip the leading separator so /foo means <root>/foo.
	if filepath.IsAbs(p) {
		// Allow an absolute path that already points inside the workspace.
		clean := filepath.Clean(p)
		if rel, err := filepath.Rel(s.root, clean); err == nil && isInside(rel) {
			return clean, nil
		}
		p = strings.TrimPrefix(p, string(os.PathSeparator))
		p = strings.TrimPrefix(p, "/")
	}
	clean := filepath.Clean(filepath.Join(s.root, p))
	rel, err := filepath.Rel(s.root, clean)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", p, err)
	}
	if !isInside(rel) {
		return "", fmt.Errorf("path %q escapes the workspace", p)
	}
	return clean, nil
}

// isInside reports whether a filepath.Rel result stays within its base.
func isInside(rel string) bool {
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

// toRel converts an absolute host path under the workspace to a clean
// workspace-relative path using forward slashes.
func (s *LocalSandbox) toRel(abs string) string {
	rel, err := filepath.Rel(s.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// Exec runs a one-shot command with an optional timeout.
func (s *LocalSandbox) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if strings.TrimSpace(req.Command) == "" {
		return ExecResult{}, errors.New("command is required")
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	workdir := s.root
	if strings.TrimSpace(req.Workdir) != "" {
		dir, err := s.resolveInWorkspace(req.Workdir)
		if err != nil {
			return ExecResult{}, err
		}
		workdir = dir
	}

	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	cmd.Dir = workdir
	cmd.Env = s.mergeEnv(req.Env)
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureProcAttr(cmd) // unix: own process group so timeouts kill children

	runErr := cmd.Run()

	// If the context expired, make sure the whole process group is gone.
	if ctx.Err() != nil {
		killProcessGroup(cmd)
	}

	res := ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			// Non-zero exit is captured in ExitCode, not treated as a hard error.
			return res, nil
		}
		res.Err = runErr.Error()
		if res.ExitCode == 0 {
			res.ExitCode = -1
		}
		return res, runErr
	}
	return res, nil
}

// Shell starts a persistent PTY-backed session.
func (s *LocalSandbox) Shell(ctx context.Context, opts ShellOpts) (Session, error) {
	command := strings.TrimSpace(opts.Command)
	args := opts.Args
	if command == "" {
		command, args = defaultShell()
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = s.root
	cmd.Env = s.mergeEnv(opts.Env)
	// Note: do not set Setpgid here. creack/pty already calls Setsid to make the
	// child a session leader; combining that with Setpgid yields EPERM on fork.
	return newLocalSession(cmd)
}

func defaultShell() (string, []string) {
	if sh := strings.TrimSpace(os.Getenv("SHELL")); sh != "" {
		return sh, nil
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash", nil
	}
	return "sh", nil
}

func (s *LocalSandbox) mergeEnv(extra map[string]string) []string {
	env := os.Environ()
	apply := func(m map[string]string) {
		for k, v := range m {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			env = append(env, k+"="+v)
		}
	}
	apply(s.env)
	apply(extra)
	return env
}

// WriteFile writes data, creating parent directories as needed.
func (s *LocalSandbox) WriteFile(ctx context.Context, p string, data []byte, mode fs.FileMode) error {
	abs, err := s.resolveInWorkspace(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(abs, data, mode)
}

// ReadFile returns file contents.
func (s *LocalSandbox) ReadFile(ctx context.Context, p string) ([]byte, error) {
	abs, err := s.resolveInWorkspace(p)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

// Stat returns metadata for a path.
func (s *LocalSandbox) Stat(ctx context.Context, p string) (FileInfo, error) {
	abs, err := s.resolveInWorkspace(p)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return FileInfo{}, err
	}
	return s.fileInfo(abs, info), nil
}

// List returns the immediate children of a directory.
func (s *LocalSandbox) List(ctx context.Context, p string) ([]FileInfo, error) {
	abs, err := s.resolveInWorkspace(p)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, s.fileInfo(filepath.Join(abs, e.Name()), info))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *LocalSandbox) fileInfo(abs string, info fs.FileInfo) FileInfo {
	return FileInfo{
		Name:    info.Name(),
		Path:    s.toRel(abs),
		Size:    info.Size(),
		IsDir:   info.IsDir(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
	}
}

// Remove deletes a path; recursive removes a non-empty directory.
func (s *LocalSandbox) Remove(ctx context.Context, p string, recursive bool) error {
	abs, err := s.resolveInWorkspace(p)
	if err != nil {
		return err
	}
	if abs == s.root {
		return errors.New("refusing to remove the workspace root")
	}
	if recursive {
		return os.RemoveAll(abs)
	}
	return os.Remove(abs)
}

// Mkdir creates a directory and any missing parents.
func (s *LocalSandbox) Mkdir(ctx context.Context, p string) error {
	abs, err := s.resolveInWorkspace(p)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

// Move renames/relocates src to dst, both jailed within the workspace.
func (s *LocalSandbox) Move(ctx context.Context, src, dst string) error {
	srcAbs, err := s.resolveInWorkspace(src)
	if err != nil {
		return err
	}
	dstAbs, err := s.resolveInWorkspace(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	return os.Rename(srcAbs, dstAbs)
}

// Glob returns workspace-relative paths matching a shell pattern. The pattern is
// matched against workspace-relative paths; "**" is not special — use Grep's
// glob or a recursive pattern segment per directory level.
func (s *LocalSandbox) Glob(ctx context.Context, pattern string) ([]string, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	var matches []string
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if p == s.root {
			return nil
		}
		rel := s.toRel(p)
		ok, matchErr := path.Match(pattern, rel)
		if matchErr != nil {
			return matchErr
		}
		if !ok {
			// Also try matching just the base name for convenience (e.g. "*.go").
			ok, _ = path.Match(pattern, path.Base(rel))
		}
		if ok {
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// Grep searches file contents for a regular expression.
func (s *LocalSandbox) Grep(ctx context.Context, pattern string, opts GrepOpts) ([]GrepHit, error) {
	expr := pattern
	if opts.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	glob := filepath.ToSlash(strings.TrimSpace(opts.Glob))

	var hits []GrepHit
	walkErr := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		rel := s.toRel(p)
		if glob != "" {
			ok, _ := path.Match(glob, rel)
			if !ok {
				ok, _ = path.Match(glob, path.Base(rel))
			}
			if !ok {
				return nil
			}
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if re.MatchString(line) {
				hits = append(hits, GrepHit{Path: rel, Line: lineNo, Text: line})
				if opts.MaxHits > 0 && len(hits) >= opts.MaxHits {
					return io.EOF // sentinel to stop the walk early
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, io.EOF) {
		return hits, walkErr
	}
	return hits, nil
}

// Snapshot writes a tar.gz of the workspace to a temp file and returns its path.
func (s *LocalSandbox) Snapshot(ctx context.Context) (string, error) {
	f, err := os.CreateTemp("", "agentgo-sbx-snap-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create snapshot file: %w", err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	walkErr := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == s.root {
			return nil
		}
		rel := s.toRel(p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
	if walkErr != nil {
		_ = tw.Close()
		_ = gz.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("snapshot workspace: %w", walkErr)
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// Restore extracts a snapshot back into the workspace.
func (s *LocalSandbox) Restore(ctx context.Context, snapshot string) error {
	f, err := os.Open(snapshot)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read snapshot entry: %w", err)
		}
		abs, err := s.resolveInWorkspace(hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(abs, fs.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(abs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			_ = os.Chtimes(abs, time.Now(), hdr.ModTime)
		}
	}
	return nil
}

// Close removes the workspace if the sandbox created it.
func (s *LocalSandbox) Close() error {
	if s.ownRoot && s.root != "" {
		return os.RemoveAll(s.root)
	}
	return nil
}
