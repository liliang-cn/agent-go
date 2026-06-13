package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// dockerWorkspace is the path inside the container where the working directory
// lives. All sandbox paths are interpreted relative to this directory.
const dockerWorkspace = "/workspace"

// DockerSandbox runs commands and file operations inside a Docker container by
// shelling out to the `docker` CLI. It carries no docker SDK dependency.
type DockerSandbox struct {
	dockerBin   string
	image       string
	containerID string
	network     string
	cpus        string
	memory      string
	env         map[string]string
}

// DockerOption configures a DockerSandbox.
type DockerOption func(*dockerConfig)

type dockerConfig struct {
	image   string
	network string
	cpus    string
	memory  string
	env     map[string]string
}

// WithImage sets the container image (default "ubuntu:22.04").
func WithImage(image string) DockerOption {
	return func(c *dockerConfig) { c.image = image }
}

// WithNetwork sets the docker network policy: "none" (default) or "bridge".
func WithNetwork(policy string) DockerOption {
	return func(c *dockerConfig) { c.network = policy }
}

// WithCPUs sets the --cpus quota (e.g. "1.5").
func WithCPUs(cpus string) DockerOption {
	return func(c *dockerConfig) { c.cpus = cpus }
}

// WithMemory sets the --memory quota (e.g. "512m").
func WithMemory(mem string) DockerOption {
	return func(c *dockerConfig) { c.memory = mem }
}

// WithDockerEnv adds environment variables applied to every exec.
func WithDockerEnv(env map[string]string) DockerOption {
	return func(c *dockerConfig) {
		if c.env == nil {
			c.env = map[string]string{}
		}
		for k, v := range env {
			c.env[k] = v
		}
	}
}

// NewDocker creates and starts a container-backed sandbox. It returns an error
// if the `docker` binary is not available so callers can fall back to NewLocal.
func NewDocker(opts ...DockerOption) (*DockerSandbox, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("docker binary not found: %w", err)
	}

	cfg := &dockerConfig{
		image:   "ubuntu:22.04",
		network: "none",
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if strings.TrimSpace(cfg.network) == "" {
		cfg.network = "none"
	}

	sb := &DockerSandbox{
		dockerBin: bin,
		image:     cfg.image,
		network:   cfg.network,
		cpus:      cfg.cpus,
		memory:    cfg.memory,
		env:       cfg.env,
	}

	name := "agentgo-sbx-" + uuid.NewString()[:12]
	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"--network", cfg.network,
		"-w", dockerWorkspace,
	}
	if strings.TrimSpace(cfg.cpus) != "" {
		args = append(args, "--cpus", cfg.cpus)
	}
	if strings.TrimSpace(cfg.memory) != "" {
		args = append(args, "--memory", cfg.memory)
	}
	// Keep the container alive; create the workspace dir up front.
	args = append(args, cfg.image, "sh", "-c",
		fmt.Sprintf("mkdir -p %s && tail -f /dev/null", dockerWorkspace))

	var out, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	sb.containerID = strings.TrimSpace(out.String())
	if sb.containerID == "" {
		return nil, errors.New("docker run returned empty container id")
	}
	return sb, nil
}

// Workspace returns the in-container workspace path.
func (s *DockerSandbox) Workspace() string { return dockerWorkspace }

// containerPath maps a workspace-relative path to an absolute in-container path,
// rejecting traversal that would escape the workspace.
func (s *DockerSandbox) containerPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return dockerWorkspace, nil
	}
	p = strings.TrimPrefix(toSlash(p), "/")
	clean := path.Clean(dockerWorkspace + "/" + p)
	if clean != dockerWorkspace && !strings.HasPrefix(clean, dockerWorkspace+"/") {
		return "", fmt.Errorf("path %q escapes the workspace", p)
	}
	return clean, nil
}

// toSlash normalizes separators; docker paths are always slash-based.
func toSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }

func (s *DockerSandbox) dockerExec(ctx context.Context, stdin string, env map[string]string, workdir string, argv ...string) (ExecResult, error) {
	args := []string{"exec", "-i"}
	if strings.TrimSpace(workdir) != "" {
		args = append(args, "-w", workdir)
	}
	for k, v := range s.env {
		args = append(args, "-e", k+"="+v)
	}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, s.containerID)
	args = append(args, argv...)

	cmd := exec.CommandContext(ctx, s.dockerBin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
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

// Exec runs a one-shot command inside the container.
func (s *DockerSandbox) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if strings.TrimSpace(req.Command) == "" {
		return ExecResult{}, errors.New("command is required")
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	workdir := dockerWorkspace
	if strings.TrimSpace(req.Workdir) != "" {
		dir, err := s.containerPath(req.Workdir)
		if err != nil {
			return ExecResult{}, err
		}
		workdir = dir
	}
	argv := append([]string{req.Command}, req.Args...)
	return s.dockerExec(ctx, req.Stdin, req.Env, workdir, argv...)
}

// Shell starts a persistent PTY-backed `docker exec` session.
func (s *DockerSandbox) Shell(ctx context.Context, opts ShellOpts) (Session, error) {
	command := strings.TrimSpace(opts.Command)
	args := opts.Args
	if command == "" {
		command, args = "sh", nil
	}
	dockerArgs := []string{"exec", "-it", "-w", dockerWorkspace}
	for k, v := range s.env {
		dockerArgs = append(dockerArgs, "-e", k+"="+v)
	}
	for k, v := range opts.Env {
		dockerArgs = append(dockerArgs, "-e", k+"="+v)
	}
	dockerArgs = append(dockerArgs, s.containerID, command)
	dockerArgs = append(dockerArgs, args...)

	cmd := exec.Command(s.dockerBin, dockerArgs...)
	return newLocalSession(cmd)
}

// WriteFile writes data into the container via `docker cp` from a temp file.
func (s *DockerSandbox) WriteFile(ctx context.Context, p string, data []byte, mode fs.FileMode) error {
	dst, err := s.containerPath(p)
	if err != nil {
		return err
	}
	// Ensure parent dir exists.
	if res, err := s.dockerExec(ctx, "", nil, "", "mkdir", "-p", path.Dir(dst)); err != nil {
		return err
	} else if res.ExitCode != 0 {
		return fmt.Errorf("mkdir parent failed: %s", strings.TrimSpace(res.Stderr))
	}

	tmp, err := os.CreateTemp("", "agentgo-dsbx-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	_ = tmp.Close()

	cp := exec.CommandContext(ctx, s.dockerBin, "cp", tmp.Name(), s.containerID+":"+dst)
	var stderr bytes.Buffer
	cp.Stderr = &stderr
	if err := cp.Run(); err != nil {
		return fmt.Errorf("docker cp: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if mode != 0 {
		_, _ = s.dockerExec(ctx, "", nil, "", "chmod", fmt.Sprintf("%o", mode.Perm()), dst)
	}
	return nil
}

// ReadFile reads file contents from the container.
func (s *DockerSandbox) ReadFile(ctx context.Context, p string) ([]byte, error) {
	src, err := s.containerPath(p)
	if err != nil {
		return nil, err
	}
	res, err := s.dockerExec(ctx, "", nil, "", "cat", src)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("read %s: %s", p, strings.TrimSpace(res.Stderr))
	}
	return []byte(res.Stdout), nil
}

// Stat returns metadata for a path using a `stat`-based probe.
func (s *DockerSandbox) Stat(ctx context.Context, p string) (FileInfo, error) {
	cp, err := s.containerPath(p)
	if err != nil {
		return FileInfo{}, err
	}
	// Format: <size> <epoch_mtime> <octal_mode> <type:f/d>
	script := fmt.Sprintf(
		`if [ -e %q ]; then if [ -d %q ]; then t=d; else t=f; fi; printf '%%s %%s %%s %%s' "$(wc -c < %q 2>/dev/null || echo 0)" "$(date -r %q +%%s 2>/dev/null || echo 0)" "0" "$t"; else echo MISSING; fi`,
		cp, cp, cp, cp,
	)
	res, err := s.dockerExec(ctx, "", nil, "", "sh", "-c", script)
	if err != nil {
		return FileInfo{}, err
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "MISSING" || out == "" {
		return FileInfo{}, fmt.Errorf("stat %s: not found", p)
	}
	fields := strings.Fields(out)
	if len(fields) < 4 {
		return FileInfo{}, fmt.Errorf("stat %s: unexpected output %q", p, out)
	}
	size, _ := strconv.ParseInt(fields[0], 10, 64)
	mtime, _ := strconv.ParseInt(fields[1], 10, 64)
	isDir := fields[3] == "d"
	return FileInfo{
		Name:    path.Base(cp),
		Path:    s.relPath(cp),
		Size:    size,
		IsDir:   isDir,
		ModTime: time.Unix(mtime, 0),
	}, nil
}

func (s *DockerSandbox) relPath(containerAbs string) string {
	rel := strings.TrimPrefix(containerAbs, dockerWorkspace+"/")
	if rel == dockerWorkspace {
		return "."
	}
	return rel
}

// List returns the immediate children of a directory.
func (s *DockerSandbox) List(ctx context.Context, p string) ([]FileInfo, error) {
	cp, err := s.containerPath(p)
	if err != nil {
		return nil, err
	}
	// Print "<type> <size> <name>" per entry. -A skips . and ..
	script := fmt.Sprintf(
		`cd %q || exit 1; for f in $(ls -A 2>/dev/null); do if [ -d "$f" ]; then printf 'd 0 %%s\n' "$f"; else printf 'f %%s %%s\n' "$(wc -c < "$f" 2>/dev/null || echo 0)" "$f"; fi; done`,
		cp,
	)
	res, err := s.dockerExec(ctx, "", nil, "", "sh", "-c", script)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("list %s: %s", p, strings.TrimSpace(res.Stderr))
	}
	var out []FileInfo
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 3)
		if len(fields) < 3 {
			continue
		}
		size, _ := strconv.ParseInt(fields[1], 10, 64)
		name := fields[2]
		rel := strings.TrimPrefix(cp+"/"+name, dockerWorkspace+"/")
		out = append(out, FileInfo{
			Name:  name,
			Path:  rel,
			Size:  size,
			IsDir: fields[0] == "d",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove deletes a path; recursive removes a non-empty directory.
func (s *DockerSandbox) Remove(ctx context.Context, p string, recursive bool) error {
	cp, err := s.containerPath(p)
	if err != nil {
		return err
	}
	if cp == dockerWorkspace {
		return errors.New("refusing to remove the workspace root")
	}
	args := []string{"rm"}
	if recursive {
		args = append(args, "-rf")
	} else {
		args = append(args, "-f")
	}
	args = append(args, cp)
	res, err := s.dockerExec(ctx, "", nil, "", args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("remove %s: %s", p, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Mkdir creates a directory and any missing parents.
func (s *DockerSandbox) Mkdir(ctx context.Context, p string) error {
	cp, err := s.containerPath(p)
	if err != nil {
		return err
	}
	res, err := s.dockerExec(ctx, "", nil, "", "mkdir", "-p", cp)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("mkdir %s: %s", p, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Move renames/relocates src to dst within the container.
func (s *DockerSandbox) Move(ctx context.Context, src, dst string) error {
	srcAbs, err := s.containerPath(src)
	if err != nil {
		return err
	}
	dstAbs, err := s.containerPath(dst)
	if err != nil {
		return err
	}
	script := fmt.Sprintf("mkdir -p %q && mv %q %q", path.Dir(dstAbs), srcAbs, dstAbs)
	res, err := s.dockerExec(ctx, "", nil, "", "sh", "-c", script)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("move: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Glob returns workspace-relative paths matching a shell pattern via `find`.
func (s *DockerSandbox) Glob(ctx context.Context, pattern string) ([]string, error) {
	pattern = toSlash(strings.TrimSpace(pattern))
	// Use find -name on the base pattern, then post-filter against the full
	// relative path so directory patterns work too.
	res, err := s.dockerExec(ctx, "", nil, "", "sh", "-c",
		fmt.Sprintf("cd %q && find . -mindepth 1", dockerWorkspace))
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		rel := strings.TrimPrefix(strings.TrimSpace(line), "./")
		if rel == "" {
			continue
		}
		ok, _ := path.Match(pattern, rel)
		if !ok {
			ok, _ = path.Match(pattern, path.Base(rel))
		}
		if ok {
			matches = append(matches, rel)
		}
	}
	sort.Strings(matches)
	return matches, nil
}

// Grep searches file contents inside the container using `grep -rn`.
func (s *DockerSandbox) Grep(ctx context.Context, pattern string, opts GrepOpts) ([]GrepHit, error) {
	// Validate the pattern locally so callers get an early, clear error.
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	args := []string{"grep", "-rnE"}
	if opts.IgnoreCase {
		args = append(args, "-i")
	}
	if strings.TrimSpace(opts.Glob) != "" {
		args = append(args, "--include="+opts.Glob)
	}
	args = append(args, "--", pattern, ".")

	res, err := s.dockerExec(ctx, "", nil, dockerWorkspace, args...)
	if err != nil {
		return nil, err
	}
	// grep exits 1 when no matches; that's not an error for us.
	var hits []GrepHit
	for _, line := range strings.Split(res.Stdout, "\n") {
		if line == "" {
			continue
		}
		// format: ./path:line:text
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		rel := strings.TrimPrefix(parts[0], "./")
		lineNo, _ := strconv.Atoi(parts[1])
		hits = append(hits, GrepHit{Path: rel, Line: lineNo, Text: parts[2]})
		if opts.MaxHits > 0 && len(hits) >= opts.MaxHits {
			break
		}
	}
	return hits, nil
}

// Snapshot copies the workspace out as a tar.gz on the host.
func (s *DockerSandbox) Snapshot(ctx context.Context) (string, error) {
	f, err := os.CreateTemp("", "agentgo-dsbx-snap-*.tar.gz")
	if err != nil {
		return "", err
	}
	_ = f.Close()
	// Stream a gzip'd tar of the workspace to the host file.
	res, err := s.dockerExec(ctx, "", nil, dockerWorkspace, "sh", "-c", "tar -cf - . 2>/dev/null | gzip")
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := os.WriteFile(f.Name(), []byte(res.Stdout), 0o644); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Restore extracts a host tar.gz snapshot back into the container workspace.
func (s *DockerSandbox) Restore(ctx context.Context, snapshot string) error {
	data, err := os.ReadFile(snapshot)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	res, err := s.dockerExec(ctx, string(data), nil, dockerWorkspace, "sh", "-c", "gzip -dc | tar -xf -")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("restore: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Close stops and removes the container (it was started with --rm).
func (s *DockerSandbox) Close() error {
	if s.containerID == "" {
		return nil
	}
	cmd := exec.Command(s.dockerBin, "rm", "-f", s.containerID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker rm: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	s.containerID = ""
	return nil
}
