// Package sandbox provides isolated execution environments for AgentGo.
//
// A Sandbox is the "hands and body" of an autonomous agent: it can run
// commands, drive persistent shell sessions, and manipulate files — all jailed
// to a workspace directory. Two backends ship in this package:
//
//   - LocalSandbox: zero-dependency, runs on the host with all paths jailed
//     under a workspace root.
//   - DockerSandbox: shells out to the `docker` CLI for strong isolation
//     (no docker SDK dependency).
//
// Both backends are concurrency-safe and self-contained — a bare AgentGo
// install is unaffected unless a Sandbox is explicitly wired in.
package sandbox

import (
	"context"
	"io/fs"
	"time"
)

// Sandbox is an isolated execution environment scoped to a workspace directory.
// All file paths passed to its methods are interpreted relative to (and jailed
// under) the workspace; attempts to escape the workspace are rejected.
type Sandbox interface {
	// Exec runs a single command to completion and returns its captured output.
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
	// Shell starts a persistent PTY-backed session the caller can drive
	// interactively via the returned Session.
	Shell(ctx context.Context, opts ShellOpts) (Session, error)

	// WriteFile writes data to path (creating parent dirs as needed).
	WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error
	// ReadFile returns the contents of path.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// Stat returns metadata for path.
	Stat(ctx context.Context, path string) (FileInfo, error)
	// List returns the immediate children of the directory at path.
	List(ctx context.Context, path string) ([]FileInfo, error)
	// Remove deletes path; recursive removes a non-empty directory.
	Remove(ctx context.Context, path string, recursive bool) error
	// Mkdir creates a directory (and any missing parents) at path.
	Mkdir(ctx context.Context, path string) error
	// Move renames/relocates src to dst, both inside the workspace.
	Move(ctx context.Context, src, dst string) error
	// Glob returns workspace-relative paths matching the shell pattern.
	Glob(ctx context.Context, pattern string) ([]string, error)
	// Grep searches file contents for a regular expression.
	Grep(ctx context.Context, pattern string, opts GrepOpts) ([]GrepHit, error)

	// Workspace returns the absolute path of the workspace root on the host.
	Workspace() string
	// Snapshot writes a tar.gz of the workspace to a temp file and returns its path.
	Snapshot(ctx context.Context) (string, error)
	// Restore extracts a snapshot (produced by Snapshot) back into the workspace.
	Restore(ctx context.Context, snapshot string) error

	// Close releases any resources held by the sandbox.
	Close() error
}

// ExecRequest describes a one-shot command to run inside the sandbox.
type ExecRequest struct {
	Command string            // executable name or path
	Args    []string          // command arguments
	Stdin   string            // optional stdin written to the process
	Env     map[string]string // extra environment variables (merged on top)
	Timeout time.Duration     // max wall-clock duration; 0 means no explicit timeout
	Workdir string            // workspace-relative working directory; empty = workspace root
}

// ExecResult is the captured outcome of an ExecRequest.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      string // non-empty if the command failed to run (vs. exiting non-zero)
}

// ShellOpts configures a persistent shell session.
type ShellOpts struct {
	Command string            // shell/command to launch; defaults to a login shell
	Args    []string          // arguments for Command
	Env     map[string]string // extra environment variables
}

// Session is a live, PTY-backed interactive process.
type Session interface {
	// Send writes input to the session's stdin (a trailing newline is added).
	Send(input string) error
	// Read returns the tail of the captured output (last tailChars runes).
	Read(tailChars int) string
	// Interrupt sends an interrupt signal (Ctrl-C equivalent).
	Interrupt() error
	// Stop terminates the session; force uses SIGKILL instead of SIGINT.
	Stop(force bool) error
	// ID returns the session's unique identifier.
	ID() string
	// Done reports whether the underlying process has exited.
	Done() bool
}

// FileInfo describes a file or directory inside the workspace.
type FileInfo struct {
	Name    string      // base name
	Path    string      // workspace-relative path
	Size    int64       // size in bytes
	IsDir   bool        // true for directories
	Mode    fs.FileMode // file mode bits
	ModTime time.Time   // last modification time
}

// GrepOpts tunes a Grep search.
type GrepOpts struct {
	Glob       string // optional file glob to restrict the search (e.g. "*.go")
	IgnoreCase bool   // case-insensitive match
	MaxHits    int    // cap on total hits returned; 0 means unlimited
}

// GrepHit is a single matching line.
type GrepHit struct {
	Path string // workspace-relative path
	Line int    // 1-based line number
	Text string // the matching line (trailing newline stripped)
}
