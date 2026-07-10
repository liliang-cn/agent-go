package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

// writeSampleDocx creates a minimal valid .docx with the given paragraphs.
func writeSampleDocx(t *testing.T, path string, paragraphs ...string) {
	t.Helper()
	var body bytes.Buffer
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t>`)
		body.WriteString(p)
		body.WriteString(`</w:t></w:r></w:p>`)
	}
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body.String() + `</w:body></w:document>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterFileToolsReadDocument(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "report.docx")
	writeSampleDocx(t, docPath, "Quarterly report", "All targets met")

	svc, err := New("tester").Build()
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	RegisterFileTools(svc)

	if !svc.toolRegistry.Has("read_document") {
		t.Fatalf("read_document tool not registered")
	}

	res, err := svc.toolRegistry.Call(context.Background(), "read_document", map[string]interface{}{"path": docPath})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("result is not a map: %T", res)
	}
	if ok, _ := m["ok"].(bool); !ok {
		t.Fatalf("expected ok:true, got %v", m)
	}
	if m["format"] != "docx" {
		t.Errorf("format = %v, want docx", m["format"])
	}
	text, _ := m["text"].(string)
	if !bytes.Contains([]byte(text), []byte("Quarterly report")) {
		t.Errorf("text missing expected content: %q", text)
	}
	if _, ok := m["metadata"].(map[string]any); !ok {
		t.Errorf("metadata missing or wrong type: %v", m["metadata"])
	}
	if _, ok := m["truncated"].(bool); !ok {
		t.Errorf("truncated field missing")
	}
}

func TestReadDocumentMissingPath(t *testing.T) {
	svc, err := New("tester").Build()
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	RegisterFileTools(svc)

	res, err := svc.toolRegistry.Call(context.Background(), "read_document", map[string]interface{}{})
	if err != nil {
		t.Fatalf("handler returned error instead of structured failure: %v", err)
	}
	m := res.(map[string]interface{})
	if okv, _ := m["ok"].(bool); okv {
		t.Fatalf("expected ok:false for missing path, got %v", m)
	}
	if m["error"] == nil {
		t.Errorf("expected error message")
	}
}

func TestWithFileToolsBuilder(t *testing.T) {
	svc, err := New("tester").WithFileTools().Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !svc.toolRegistry.Has("read_document") {
		t.Fatalf("WithFileTools() did not register read_document")
	}
	if !svc.toolRegistry.Has("export_document") {
		t.Fatalf("WithFileTools() did not register export_document")
	}
}

func TestReadDocumentPerPage(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "r.docx")
	writeSampleDocx(t, docPath, "line one", "line two")

	svc, err := New("tester").Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	RegisterFileTools(svc)

	// per_page true always yields a pages_text field (empty array for docx).
	res := ReadDocumentWith(context.Background(), svc, ReadDocumentOpts{Path: docPath, PerPage: true})
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("expected ok, got %v", res)
	}
	if _, has := res["pages_text"]; !has {
		t.Errorf("per_page true should include pages_text")
	}

	// per_page false omits pages_text.
	res2 := ReadDocument(context.Background(), svc, docPath, 0)
	if _, has := res2["pages_text"]; has {
		t.Errorf("pages_text must be absent when per_page is false")
	}
}

func TestExportDocumentHostMarkdown(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "src.docx")
	writeSampleDocx(t, docPath, "Alpha content", "Beta content")
	outPath := filepath.Join(dir, "out.md")

	svc, err := New("tester").Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	RegisterFileTools(svc)

	res, err := svc.toolRegistry.Call(context.Background(), "export_document", map[string]interface{}{
		"path":     docPath,
		"out_path": outPath,
		"format":   "md",
	})
	if err != nil {
		t.Fatalf("export handler: %v", err)
	}
	m := res.(map[string]interface{})
	if ok, _ := m["ok"].(bool); !ok {
		t.Fatalf("expected ok:true, got %v", m)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if !strings.Contains(string(data), "## Page 1") {
		t.Errorf("md output missing page header: %q", data)
	}
	if !strings.Contains(string(data), "Alpha content") {
		t.Errorf("md output missing extracted text: %q", data)
	}
}

func TestExportDocumentBadFormat(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "src.docx")
	writeSampleDocx(t, docPath, "x")
	svc, _ := New("tester").Build()
	RegisterFileTools(svc)
	res := ExportDocument(context.Background(), svc, ExportDocumentOpts{
		Path: docPath, OutPath: filepath.Join(dir, "o.pdf"), Format: "pdf",
	})
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("expected ok:false for bad format, got %v", res)
	}
}

func TestReadDocumentSandboxWorkspacePath(t *testing.T) {
	sb, err := sandbox.NewLocal()
	if err != nil {
		t.Fatalf("new local sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	ctx := context.Background()

	// Write a docx into the workspace directly on disk so we can read it back
	// through the workspace-resolution (streaming) path.
	docPath := filepath.Join(sb.Workspace(), "in.docx")
	writeSampleDocx(t, docPath, "Sandbox doc body")

	svc, err := New("tester").WithSandbox(sb).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	RegisterFileTools(svc)

	res := ReadDocument(ctx, svc, "in.docx", 0)
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("expected ok:true, got %v", res)
	}
	if text, _ := res["text"].(string); !strings.Contains(text, "Sandbox doc body") {
		t.Errorf("text missing content: %q", text)
	}

	// Escape attempts must not resolve to a host path under the workspace;
	// they fall through to the sandbox ReadFile path, which rejects the escape.
	esc := ReadDocument(ctx, svc, "../../etc/hosts", 0)
	if ok, _ := esc["ok"].(bool); ok {
		t.Errorf("expected escape to fail, got %v", esc)
	}

	// export_document writes back into the workspace.
	outRes := ExportDocument(ctx, svc, ExportDocumentOpts{Path: "in.docx", OutPath: "out.md"})
	if ok, _ := outRes["ok"].(bool); !ok {
		t.Fatalf("export in sandbox failed: %v", outRes)
	}
	written, err := sb.ReadFile(ctx, "out.md")
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(written), "Sandbox doc body") {
		t.Errorf("exported file missing content: %q", written)
	}
}
