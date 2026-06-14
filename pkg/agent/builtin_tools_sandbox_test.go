package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

func newSandboxTestService(t *testing.T) (*Service, sandbox.Sandbox) {
	t.Helper()
	sb, err := sandbox.NewLocal()
	if err != nil {
		t.Fatalf("new local sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	svc := &Service{toolRegistry: NewToolRegistry()}
	RegisterSandboxTools(svc, sb)
	return svc, sb
}

func callTool(t *testing.T, svc *Service, name string, args map[string]interface{}) map[string]interface{} {
	t.Helper()
	res, err := svc.toolRegistry.Call(context.Background(), name, args)
	if err != nil {
		t.Fatalf("call %s returned error: %v", name, err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("call %s did not return a map: %T", name, res)
	}
	return m
}

func mustOK(t *testing.T, name string, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	if ok, _ := m["ok"].(bool); !ok {
		t.Fatalf("%s expected ok:true, got %+v", name, m)
	}
	data, _ := m["data"].(map[string]interface{})
	return data
}

func TestRegisterSandboxToolsRegistersExpectedNames(t *testing.T) {
	svc, _ := newSandboxTestService(t)
	want := []string{
		"fs_read", "fs_write", "fs_edit", "fs_multi_edit", "fs_list",
		"fs_glob", "fs_grep", "fs_move", "fs_remove", "fs_mkdir",
		"bash", "shell_start", "shell_send", "shell_read",
		"shell_interrupt", "shell_stop",
	}
	for _, name := range want {
		if !svc.toolRegistry.Has(name) {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}

func TestSandboxFsWriteReadEdit(t *testing.T) {
	svc, _ := newSandboxTestService(t)
	ctx := context.Background()

	mustOK(t, "fs_write", callTool(t, svc, "fs_write", map[string]interface{}{
		"path": "note.txt", "content": "alpha\nbeta\ngamma\n",
	}))

	data := mustOK(t, "fs_read", callTool(t, svc, "fs_read", map[string]interface{}{"path": "note.txt"}))
	content, _ := data["content"].(string)
	if !strings.Contains(content, "     1\talpha") {
		t.Fatalf("expected line-numbered content, got %q", content)
	}

	// offset/limit
	data = mustOK(t, "fs_read", callTool(t, svc, "fs_read", map[string]interface{}{
		"path": "note.txt", "offset": 1, "limit": 1,
	}))
	content, _ = data["content"].(string)
	if !strings.Contains(content, "beta") || strings.Contains(content, "alpha") || strings.Contains(content, "gamma") {
		t.Fatalf("offset/limit not honored: %q", content)
	}

	// unique edit succeeds
	mustOK(t, "fs_edit", callTool(t, svc, "fs_edit", map[string]interface{}{
		"path": "note.txt", "old_string": "beta", "new_string": "BETA",
	}))
	raw, _ := svc.toolRegistry.Call(ctx, "fs_read", map[string]interface{}{"path": "note.txt"})
	if got, _ := raw.(map[string]interface{}); !strings.Contains(got["data"].(map[string]interface{})["content"].(string), "BETA") {
		t.Fatalf("edit did not apply: %+v", raw)
	}

	// missing old_string fails with ok:false
	res := callTool(t, svc, "fs_edit", map[string]interface{}{
		"path": "note.txt", "old_string": "nope", "new_string": "x",
	})
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("expected fs_edit to fail for missing old_string, got %+v", res)
	}
}

func TestSandboxFsEditNonUniqueFails(t *testing.T) {
	svc, _ := newSandboxTestService(t)
	mustOK(t, "fs_write", callTool(t, svc, "fs_write", map[string]interface{}{
		"path": "dup.txt", "content": "x x x",
	}))
	res := callTool(t, svc, "fs_edit", map[string]interface{}{
		"path": "dup.txt", "old_string": "x", "new_string": "y",
	})
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("expected non-unique edit to fail, got %+v", res)
	}
	if msg, _ := res["error"].(string); !strings.Contains(msg, "not unique") {
		t.Fatalf("expected uniqueness error, got %q", msg)
	}
}

func TestSandboxFsMultiEditAtomic(t *testing.T) {
	svc, _ := newSandboxTestService(t)
	mustOK(t, "fs_write", callTool(t, svc, "fs_write", map[string]interface{}{
		"path": "m.txt", "content": "one two three",
	}))

	// One of the edits has a missing old_string -> whole op must fail, no write.
	res := callTool(t, svc, "fs_multi_edit", map[string]interface{}{
		"path": "m.txt",
		"edits": []interface{}{
			map[string]interface{}{"old_string": "one", "new_string": "1"},
			map[string]interface{}{"old_string": "MISSING", "new_string": "x"},
		},
	})
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("expected multi_edit to fail on missing old_string, got %+v", res)
	}
	data := mustOK(t, "fs_read", callTool(t, svc, "fs_read", map[string]interface{}{"path": "m.txt"}))
	if strings.Contains(data["content"].(string), "1 two three") {
		t.Fatalf("multi_edit should not have written partial changes")
	}

	// All present -> applied sequentially.
	mustOK(t, "fs_multi_edit", callTool(t, svc, "fs_multi_edit", map[string]interface{}{
		"path": "m.txt",
		"edits": []interface{}{
			map[string]interface{}{"old_string": "one", "new_string": "1"},
			map[string]interface{}{"old_string": "three", "new_string": "3"},
		},
	}))
	data = mustOK(t, "fs_read", callTool(t, svc, "fs_read", map[string]interface{}{"path": "m.txt"}))
	if !strings.Contains(data["content"].(string), "1 two 3") {
		t.Fatalf("multi_edit did not apply both: %q", data["content"])
	}
}

func TestSandboxBash(t *testing.T) {
	svc, _ := newSandboxTestService(t)
	data := mustOK(t, "bash", callTool(t, svc, "bash", map[string]interface{}{
		"command": "echo hello-sandbox",
	}))
	if out, _ := data["stdout"].(string); !strings.Contains(out, "hello-sandbox") {
		t.Fatalf("bash stdout missing: %+v", data)
	}
	if code, ok := data["exit_code"]; !ok {
		t.Fatalf("bash result missing exit_code: %+v", data)
	} else if int(code.(int)) != 0 {
		t.Fatalf("expected exit_code 0, got %v", code)
	}
}

func TestSandboxBashNonZeroExitReportsFailure(t *testing.T) {
	svc, _ := newSandboxTestService(t)
	// A failing script must report ok:false (so the model's toolOk guard fires
	// and it doesn't silently consume empty stdout), while still surfacing
	// stdout/stderr/exit_code in data.
	m := callTool(t, svc, "bash", map[string]interface{}{
		"command": "echo oops >&2; exit 7",
	})
	if ok, _ := m["ok"].(bool); ok {
		t.Fatalf("expected ok:false for non-zero exit, got %+v", m)
	}
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "exited 7") {
		t.Fatalf("expected error mentioning exit code, got %q", errMsg)
	}
	data, _ := m["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data with stdout/stderr/exit_code, got %+v", m)
	}
	if code, _ := data["exit_code"].(int); code != 7 {
		t.Fatalf("expected exit_code 7 in data, got %v", data["exit_code"])
	}
	if se, _ := data["stderr"].(string); !strings.Contains(se, "oops") {
		t.Fatalf("expected stderr preserved, got %+v", data)
	}
}

func TestSandboxFsListGlobGrep(t *testing.T) {
	svc, _ := newSandboxTestService(t)
	mustOK(t, "fs_write", callTool(t, svc, "fs_write", map[string]interface{}{"path": "a.txt", "content": "needle here\nother"}))
	mustOK(t, "fs_write", callTool(t, svc, "fs_write", map[string]interface{}{"path": "b.txt", "content": "no match"}))

	data := mustOK(t, "fs_list", callTool(t, svc, "fs_list", map[string]interface{}{}))
	if entries, _ := data["entries"].([]map[string]interface{}); len(entries) < 2 {
		t.Fatalf("expected at least 2 list entries, got %+v", data)
	}

	data = mustOK(t, "fs_glob", callTool(t, svc, "fs_glob", map[string]interface{}{"pattern": "*.txt"}))
	if matches, _ := data["matches"].([]string); len(matches) < 2 {
		t.Fatalf("expected glob to match 2 files, got %+v", data)
	}

	data = mustOK(t, "fs_grep", callTool(t, svc, "fs_grep", map[string]interface{}{"pattern": "needle"}))
	hits, _ := data["hits"].([]map[string]interface{})
	if len(hits) != 1 {
		t.Fatalf("expected 1 grep hit, got %+v", data)
	}
}

func TestSandboxShellSession(t *testing.T) {
	svc, _ := newSandboxTestService(t)
	data := mustOK(t, "shell_start", callTool(t, svc, "shell_start", map[string]interface{}{}))
	sid, _ := data["session_id"].(string)
	if sid == "" {
		t.Fatal("shell_start returned empty session_id")
	}

	mustOK(t, "shell_send", callTool(t, svc, "shell_send", map[string]interface{}{
		"session_id": sid, "input": "echo shell-marker",
	}))

	// Give the PTY a moment to echo.
	found := false
	for i := 0; i < 50; i++ {
		data = mustOK(t, "shell_read", callTool(t, svc, "shell_read", map[string]interface{}{"session_id": sid}))
		if strings.Contains(data["output"].(string), "shell-marker") {
			found = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("did not observe shell-marker in session output: %+v", data)
	}

	mustOK(t, "shell_stop", callTool(t, svc, "shell_stop", map[string]interface{}{"session_id": sid, "force": true}))

	// session removed -> shell_read now errors via ok:false
	res := callTool(t, svc, "shell_read", map[string]interface{}{"session_id": sid})
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("expected shell_read to fail after stop, got %+v", res)
	}
}
