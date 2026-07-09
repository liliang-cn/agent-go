package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
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
}
