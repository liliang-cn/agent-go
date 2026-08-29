package agent

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

// Built-in filesystem / shell tools backed by a sandbox.Sandbox. These give an
// agent the "hands" to read, write, edit, and search files plus run commands —
// all jailed to the sandbox workspace. Registration mirrors RegisterFetchURLTool:
// opt-in, guarded against double registration, structured {ok,data,error} returns.

// toolArgBool extracts a boolean tool argument, tolerating string forms.
func toolArgBool(args map[string]interface{}, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return false
}

// sandboxShellManager keeps live sandbox.Session handles for the shell_* tools,
// keyed by session id. Mirrors the operator_sessions.go singleton+mutex style.
type sandboxShellManager struct {
	mu       sync.RWMutex
	sessions map[string]sandbox.Session
}

var globalSandboxShells = &sandboxShellManager{
	sessions: make(map[string]sandbox.Session),
}

func (m *sandboxShellManager) add(s sandbox.Session) {
	m.mu.Lock()
	m.sessions[s.ID()] = s
	m.mu.Unlock()
}

func (m *sandboxShellManager) get(id string) (sandbox.Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return nil, fmt.Errorf("shell session %s not found", id)
	}
	return s, nil
}

func (m *sandboxShellManager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, strings.TrimSpace(id))
	m.mu.Unlock()
}

func toolOK(data interface{}) map[string]interface{} {
	return map[string]interface{}{"ok": true, "data": data}
}

func toolErr(msg string) map[string]interface{} {
	return map[string]interface{}{"ok": false, "error": msg}
}

// withLineNumbers renders text Claude-Code style: "   1\tfoo". offset is 0-based,
// limit caps the number of lines returned (0 = all).
func withLineNumbers(content string, offset, limit int) string {
	lines := strings.Split(content, "\n")
	// Drop a trailing empty element produced by a final newline so we don't
	// number a phantom blank line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	end := len(lines)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	var b strings.Builder
	for i := offset; i < end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	return b.String()
}

// RegisterSandboxTools registers the built-in filesystem and shell tools on a
// service, backed by the given sandbox. No-op if svc or sb is nil.
//
//	svc, _ := agent.New("assistant").Build()
//	sb, _ := sandbox.NewLocal()
//	agent.RegisterSandboxTools(svc, sb)
func RegisterSandboxTools(svc *Service, sb sandbox.Sandbox) {
	if svc == nil || sb == nil {
		return
	}
	has := func(name string) bool {
		return svc.toolRegistry != nil && svc.toolRegistry.Has(name)
	}

	roMeta := ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}
	destMeta := ToolMetadata{Destructive: true, InterruptBehavior: InterruptBehaviorBlock}

	// --- fs_read ---
	if !has("fs_read") {
		svc.AddToolWithMetadata(
			"fs_read",
			"Read a file in the workspace and return its text with line numbers (e.g. \"   1\\tfoo\"). Optional offset (first line to read, 0-based) and limit (max lines to read).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]interface{}{"type": "string", "description": "Path relative to the workspace root"},
					"offset": map[string]interface{}{"type": "integer", "description": "Start line (0-based), default 0"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Max lines to read; 0 means all"},
				},
				"required": []string{"path"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				path := toolArgString(args, "path")
				if path == "" {
					return toolErr("path required"), nil
				}
				data, err := sb.ReadFile(ctx, path)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				out := withLineNumbers(string(data), toolArgInt(args, "offset"), toolArgInt(args, "limit"))
				return toolOK(map[string]interface{}{"path": path, "content": out}), nil
			},
			roMeta,
		)
	}

	// --- fs_write ---
	if !has("fs_write") {
		svc.AddToolWithMetadata(
			"fs_write",
			"Write content to a workspace file (overwrites the whole file; parent directories are created).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string", "description": "Path relative to the workspace root"},
					"content": map[string]interface{}{"type": "string", "description": "Full content to write"},
				},
				"required": []string{"path", "content"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				path := toolArgString(args, "path")
				if path == "" {
					return toolErr("path required"), nil
				}
				content := ""
				if v, ok := args["content"].(string); ok {
					content = v
				}
				if err := sb.WriteFile(ctx, path, []byte(content), fs.FileMode(0o644)); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"path": path, "bytes": len(content)}), nil
			},
			destMeta,
		)
	}

	// --- fs_edit ---
	if !has("fs_edit") {
		svc.AddToolWithMetadata(
			"fs_edit",
			"Replace the one occurrence of old_string with new_string in a file. If old_string is missing or appears more than once, returns ok:false with an explanation; add surrounding context to make it unique.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":       map[string]interface{}{"type": "string", "description": "Path relative to the workspace root"},
					"old_string": map[string]interface{}{"type": "string", "description": "Exact text to replace (must be unique in the file)"},
					"new_string": map[string]interface{}{"type": "string", "description": "Replacement text"},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				path := toolArgString(args, "path")
				oldStr, _ := args["old_string"].(string)
				newStr, _ := args["new_string"].(string)
				if path == "" {
					return toolErr("path required"), nil
				}
				if oldStr == "" {
					return toolErr("old_string required"), nil
				}
				data, err := sb.ReadFile(ctx, path)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				content := string(data)
				n := strings.Count(content, oldStr)
				if n == 0 {
					return toolErr("old_string not found in file"), nil
				}
				if n > 1 {
					return toolErr(fmt.Sprintf("old_string is not unique (found %d times); add more surrounding context", n)), nil
				}
				updated := strings.Replace(content, oldStr, newStr, 1)
				if err := sb.WriteFile(ctx, path, []byte(updated), fs.FileMode(0o644)); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"path": path, "replaced": 1}), nil
			},
			destMeta,
		)
	}

	// --- fs_multi_edit ---
	if !has("fs_multi_edit") {
		svc.AddToolWithMetadata(
			"fs_multi_edit",
			"Apply several {old_string,new_string} replacements to one file atomically and in order: read once, replace in sequence, write once. If any old_string is missing the whole edit fails and nothing is written.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Path relative to the workspace root"},
					"edits": map[string]interface{}{
						"type":        "array",
						"description": "Replacements, applied in order",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"old_string": map[string]interface{}{"type": "string"},
								"new_string": map[string]interface{}{"type": "string"},
							},
							"required": []string{"old_string", "new_string"},
						},
					},
				},
				"required": []string{"path", "edits"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				path := toolArgString(args, "path")
				if path == "" {
					return toolErr("path required"), nil
				}
				rawEdits, ok := args["edits"].([]interface{})
				if !ok || len(rawEdits) == 0 {
					return toolErr("edits must be a non-empty array of {old_string,new_string}"), nil
				}
				data, err := sb.ReadFile(ctx, path)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				content := string(data)
				for i, re := range rawEdits {
					m, ok := re.(map[string]interface{})
					if !ok {
						return toolErr(fmt.Sprintf("edit %d is not an object", i)), nil
					}
					oldStr, _ := m["old_string"].(string)
					newStr, _ := m["new_string"].(string)
					if oldStr == "" {
						return toolErr(fmt.Sprintf("edit %d: old_string required", i)), nil
					}
					if !strings.Contains(content, oldStr) {
						return toolErr(fmt.Sprintf("edit %d: old_string not found (no changes written)", i)), nil
					}
					content = strings.Replace(content, oldStr, newStr, 1)
				}
				if err := sb.WriteFile(ctx, path, []byte(content), fs.FileMode(0o644)); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"path": path, "edits": len(rawEdits)}), nil
			},
			destMeta,
		)
	}

	// --- fs_list ---
	if !has("fs_list") {
		svc.AddToolWithMetadata(
			"fs_list",
			"List the direct entries (files and directories) of a workspace directory.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Directory path relative to the workspace; defaults to the root"},
				},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				path := toolArgString(args, "path")
				if path == "" {
					path = "."
				}
				infos, err := sb.List(ctx, path)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				entries := make([]map[string]interface{}, 0, len(infos))
				for _, fi := range infos {
					entries = append(entries, map[string]interface{}{
						"name":   fi.Name,
						"path":   fi.Path,
						"size":   fi.Size,
						"is_dir": fi.IsDir,
					})
				}
				return toolOK(map[string]interface{}{"path": path, "entries": entries}), nil
			},
			roMeta,
		)
	}

	// --- fs_glob ---
	if !has("fs_glob") {
		svc.AddToolWithMetadata(
			"fs_glob",
			"Match workspace files with a shell glob pattern (e.g. *.go, docs/*.md) and return the relative paths.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{"type": "string", "description": "Glob pattern"},
				},
				"required": []string{"pattern"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				pattern := toolArgString(args, "pattern")
				if pattern == "" {
					return toolErr("pattern required"), nil
				}
				matches, err := sb.Glob(ctx, pattern)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"pattern": pattern, "matches": matches}), nil
			},
			roMeta,
		)
	}

	// --- fs_grep ---
	if !has("fs_grep") {
		svc.AddToolWithMetadata(
			"fs_grep",
			"Search workspace file contents with a regular expression and return the matching {path,line,text}. Optional glob to restrict the files, ignore_case, max_hits.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern":     map[string]interface{}{"type": "string", "description": "Regular expression"},
					"glob":        map[string]interface{}{"type": "string", "description": "Glob restricting which files are searched, e.g. *.go"},
					"ignore_case": map[string]interface{}{"type": "boolean", "description": "Match case-insensitively"},
					"max_hits":    map[string]interface{}{"type": "integer", "description": "Max number of hits; 0 means unlimited"},
				},
				"required": []string{"pattern"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				pattern := toolArgString(args, "pattern")
				if pattern == "" {
					return toolErr("pattern required"), nil
				}
				hits, err := sb.Grep(ctx, pattern, sandbox.GrepOpts{
					Glob:       toolArgString(args, "glob"),
					IgnoreCase: toolArgBool(args, "ignore_case"),
					MaxHits:    toolArgInt(args, "max_hits"),
				})
				if err != nil {
					return toolErr(err.Error()), nil
				}
				out := make([]map[string]interface{}, 0, len(hits))
				for _, h := range hits {
					out = append(out, map[string]interface{}{"path": h.Path, "line": h.Line, "text": h.Text})
				}
				return toolOK(map[string]interface{}{"pattern": pattern, "hits": out}), nil
			},
			roMeta,
		)
	}

	// --- fs_move ---
	if !has("fs_move") {
		svc.AddToolWithMetadata(
			"fs_move",
			"Move or rename a file or directory inside the workspace.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"src": map[string]interface{}{"type": "string", "description": "Source path"},
					"dst": map[string]interface{}{"type": "string", "description": "Destination path"},
				},
				"required": []string{"src", "dst"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				src := toolArgString(args, "src")
				dst := toolArgString(args, "dst")
				if src == "" || dst == "" {
					return toolErr("src and dst required"), nil
				}
				if err := sb.Move(ctx, src, dst); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"src": src, "dst": dst}), nil
			},
			destMeta,
		)
	}

	// --- fs_remove ---
	if !has("fs_remove") {
		svc.AddToolWithMetadata(
			"fs_remove",
			"Delete a file or directory in the workspace. Deleting a non-empty directory requires recursive:true.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":      map[string]interface{}{"type": "string", "description": "Path to delete"},
					"recursive": map[string]interface{}{"type": "boolean", "description": "Delete directories recursively"},
				},
				"required": []string{"path"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				path := toolArgString(args, "path")
				if path == "" {
					return toolErr("path required"), nil
				}
				if err := sb.Remove(ctx, path, toolArgBool(args, "recursive")); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"path": path}), nil
			},
			destMeta,
		)
	}

	// --- fs_mkdir ---
	if !has("fs_mkdir") {
		svc.AddToolWithMetadata(
			"fs_mkdir",
			"Create a directory in the workspace, including any missing parents.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Directory path to create"},
				},
				"required": []string{"path"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				path := toolArgString(args, "path")
				if path == "" {
					return toolErr("path required"), nil
				}
				if err := sb.Mkdir(ctx, path); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"path": path}), nil
			},
			destMeta,
		)
	}

	// --- bash ---
	if !has("bash") {
		svc.AddToolWithMetadata(
			"bash",
			"Run one shell command in the sandbox workspace (via sh -c) and return stdout/stderr/exit_code. Optional timeout_seconds (default 120).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command":         map[string]interface{}{"type": "string", "description": "Shell command to run"},
					"timeout_seconds": map[string]interface{}{"type": "integer", "description": "Timeout in seconds, default 120"},
				},
				"required": []string{"command"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				command := toolArgString(args, "command")
				if command == "" {
					return toolErr("command required"), nil
				}
				timeout := toolArgInt(args, "timeout_seconds")
				if timeout <= 0 {
					timeout = 120
				}
				res, err := sb.Exec(ctx, sandbox.ExecRequest{
					Command: "sh",
					Args:    []string{"-c", command},
					Timeout: time.Duration(timeout) * time.Second,
				})
				if err != nil {
					return toolErr(err.Error()), nil
				}
				data := map[string]interface{}{
					"stdout":    res.Stdout,
					"stderr":    res.Stderr,
					"exit_code": res.ExitCode,
				}
				if res.Err != "" {
					data["err"] = res.Err
				}
				// A non-zero exit is a failure: report ok:false so the model's
				// toolOk() guard fires. Without this a crashed script (empty
				// stdout) silently flows into an empty file downstream. stdout/
				// stderr stay in data so the model can still inspect them.
				if res.ExitCode != 0 {
					return map[string]interface{}{
						"ok":    false,
						"error": fmt.Sprintf("command exited %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr)),
						"data":  data,
					}, nil
				}
				return toolOK(data), nil
			},
			destMeta,
		)
	}

	// --- shell_start ---
	if !has("shell_start") {
		svc.AddToolWithMetadata(
			"shell_start",
			"Start a persistent shell session (PTY) and return its session_id. Interact with it via shell_send/shell_read. Optional command, default sh.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{"type": "string", "description": "Shell to start, default sh"},
				},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				command := toolArgString(args, "command")
				if command == "" {
					command = "sh"
				}
				sess, err := sb.Shell(ctx, sandbox.ShellOpts{Command: command})
				if err != nil {
					return toolErr(err.Error()), nil
				}
				globalSandboxShells.add(sess)
				return toolOK(map[string]interface{}{"session_id": sess.ID()}), nil
			},
			destMeta,
		)
	}

	// --- shell_send ---
	if !has("shell_send") {
		svc.AddToolWithMetadata(
			"shell_send",
			"Send one line of input to a persistent shell session (a newline is appended).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "Session id returned by shell_start"},
					"input":      map[string]interface{}{"type": "string", "description": "Input to send"},
				},
				"required": []string{"session_id", "input"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				sess, err := globalSandboxShells.get(toolArgString(args, "session_id"))
				if err != nil {
					return toolErr(err.Error()), nil
				}
				input, _ := args["input"].(string)
				if err := sess.Send(input); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"session_id": sess.ID(), "sent": true}), nil
			},
			destMeta,
		)
	}

	// --- shell_read ---
	if !has("shell_read") {
		svc.AddToolWithMetadata(
			"shell_read",
			"Read the most recent output (the tail) of a persistent shell session. Optional tail_chars caps how many characters come back.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "Session id"},
					"tail_chars": map[string]interface{}{"type": "integer", "description": "How many trailing characters to return, default 4000"},
				},
				"required": []string{"session_id"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				sess, err := globalSandboxShells.get(toolArgString(args, "session_id"))
				if err != nil {
					return toolErr(err.Error()), nil
				}
				tail := toolArgInt(args, "tail_chars")
				if tail <= 0 {
					tail = 4000
				}
				return toolOK(map[string]interface{}{
					"session_id": sess.ID(),
					"output":     sess.Read(tail),
					"done":       sess.Done(),
				}), nil
			},
			roMeta,
		)
	}

	// --- shell_interrupt ---
	if !has("shell_interrupt") {
		svc.AddToolWithMetadata(
			"shell_interrupt",
			"Send an interrupt signal (the equivalent of Ctrl-C) to a persistent shell session.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "Session id"},
				},
				"required": []string{"session_id"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				sess, err := globalSandboxShells.get(toolArgString(args, "session_id"))
				if err != nil {
					return toolErr(err.Error()), nil
				}
				if err := sess.Interrupt(); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"session_id": sess.ID(), "interrupted": true}), nil
			},
			destMeta,
		)
	}

	// --- shell_stop ---
	if !has("shell_stop") {
		svc.AddToolWithMetadata(
			"shell_stop",
			"Terminate a persistent shell session. force:true uses SIGKILL, otherwise SIGINT.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "Session id"},
					"force":      map[string]interface{}{"type": "boolean", "description": "Force SIGKILL"},
				},
				"required": []string{"session_id"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				id := toolArgString(args, "session_id")
				sess, err := globalSandboxShells.get(id)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				if err := sess.Stop(toolArgBool(args, "force")); err != nil {
					return toolErr(err.Error()), nil
				}
				globalSandboxShells.remove(id)
				return toolOK(map[string]interface{}{"session_id": id, "stopped": true}), nil
			},
			destMeta,
		)
	}
}
