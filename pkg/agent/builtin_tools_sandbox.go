package agent

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
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
			"读取工作区内某个文件的内容，返回带行号(如 \"   1\\tfoo\")的文本。可选 offset(从第几行起,0基)与 limit(最多读多少行)。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]interface{}{"type": "string", "description": "工作区相对路径"},
					"offset": map[string]interface{}{"type": "integer", "description": "起始行(0基),默认0"},
					"limit":  map[string]interface{}{"type": "integer", "description": "最多读取行数,0表示全部"},
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
			"把内容写入工作区文件(整文件覆盖,父目录自动创建)。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string", "description": "工作区相对路径"},
					"content": map[string]interface{}{"type": "string", "description": "要写入的完整内容"},
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
			"在文件中把唯一出现的 old_string 替换为 new_string。若 old_string 不存在或出现多次,返回 ok:false 并说明,请提供更多上下文使其唯一。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":       map[string]interface{}{"type": "string", "description": "工作区相对路径"},
					"old_string": map[string]interface{}{"type": "string", "description": "要被替换的精确文本(必须在文件中唯一)"},
					"new_string": map[string]interface{}{"type": "string", "description": "替换后的文本"},
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
			"对同一文件按顺序原子地应用多处 {old_string,new_string} 替换:一次读入、依次替换、一次写回。任一 old_string 缺失则整体失败、不写入。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "工作区相对路径"},
					"edits": map[string]interface{}{
						"type":        "array",
						"description": "替换列表,按顺序应用",
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
			"列出工作区某个目录下的直接子项(文件与目录)。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "工作区相对目录路径,缺省为根"},
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
			"用 shell 通配模式(如 *.go、docs/*.md)匹配工作区文件,返回相对路径列表。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{"type": "string", "description": "通配模式"},
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
			"在工作区文件内容中按正则搜索,返回命中的 {path,line,text}。可选 glob 限定文件、ignore_case、max_hits。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern":     map[string]interface{}{"type": "string", "description": "正则表达式"},
					"glob":        map[string]interface{}{"type": "string", "description": "限定搜索的文件通配,如 *.go"},
					"ignore_case": map[string]interface{}{"type": "boolean", "description": "忽略大小写"},
					"max_hits":    map[string]interface{}{"type": "integer", "description": "最大命中数,0为不限"},
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
			"在工作区内移动/重命名文件或目录。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"src": map[string]interface{}{"type": "string", "description": "源路径"},
					"dst": map[string]interface{}{"type": "string", "description": "目标路径"},
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
			"删除工作区内的文件或目录。删除非空目录需 recursive:true。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":      map[string]interface{}{"type": "string", "description": "要删除的路径"},
					"recursive": map[string]interface{}{"type": "boolean", "description": "递归删除目录"},
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
			"在工作区内创建目录(含缺失的父目录)。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "要创建的目录路径"},
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
			"在沙箱工作区里执行一条 shell 命令(经 sh -c),返回 stdout/stderr/exit_code。可选 timeout_seconds(默认120)。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command":         map[string]interface{}{"type": "string", "description": "要执行的 shell 命令"},
					"timeout_seconds": map[string]interface{}{"type": "integer", "description": "超时秒数,默认120"},
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
				return toolOK(data), nil
			},
			destMeta,
		)
	}

	// --- shell_start ---
	if !has("shell_start") {
		svc.AddToolWithMetadata(
			"shell_start",
			"启动一个持久 shell 会话(PTY),返回 session_id。后续用 shell_send/shell_read 与之交互。可选 command,默认 sh。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{"type": "string", "description": "要启动的 shell,默认 sh"},
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
			"向持久 shell 会话发送一行输入(自动追加换行)。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "shell_start 返回的会话 id"},
					"input":      map[string]interface{}{"type": "string", "description": "要发送的输入"},
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
			"读取持久 shell 会话的最近输出(尾部)。可选 tail_chars 限定字符数。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "会话 id"},
					"tail_chars": map[string]interface{}{"type": "integer", "description": "返回的尾部字符数,默认4000"},
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
			"向持久 shell 会话发送中断信号(等同 Ctrl-C)。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "会话 id"},
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
			"终止持久 shell 会话。force:true 用 SIGKILL,否则 SIGINT。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "会话 id"},
					"force":      map[string]interface{}{"type": "boolean", "description": "强制 KILL"},
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
