package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeSamplePNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for x := 0; x < 20; x++ {
		img.Set(x, 0, color.RGBA{R: 255, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func TestOCRImageTool(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "scan.png")
	writeSamplePNG(t, imgPath)

	var gotModel string
	var gotImages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req ollamaGenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotModel = req.Model
		gotImages = len(req.Images)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{Response: "HELLO-OCR"})
	}))
	defer srv.Close()

	svc, err := New("tester").Build()
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	RegisterOCRTool(svc, WithOCREndpoint(srv.URL))

	if !svc.toolRegistry.Has("ocr_image") {
		t.Fatalf("ocr_image tool not registered")
	}

	res, err := svc.toolRegistry.Call(context.Background(), "ocr_image", map[string]interface{}{"path": imgPath})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	m := res.(map[string]interface{})
	if ok, _ := m["ok"].(bool); !ok {
		t.Fatalf("expected ok:true, got %v", m)
	}
	if m["text"] != "HELLO-OCR" {
		t.Errorf("text = %v, want HELLO-OCR", m["text"])
	}
	if m["model"] != defaultOCRModel {
		t.Errorf("model = %v, want %s", m["model"], defaultOCRModel)
	}
	if gotModel != defaultOCRModel {
		t.Errorf("server saw model %q, want %s", gotModel, defaultOCRModel)
	}
	if gotImages != 1 {
		t.Errorf("server saw %d images, want 1", gotImages)
	}
}

func TestOCRImageMissingPath(t *testing.T) {
	svc, err := New("tester").Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	RegisterOCRTool(svc)
	res := OCRImage(context.Background(), svc, "", "")
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("expected ok:false for empty path, got %v", res)
	}
}

func TestWithOCRBuilder(t *testing.T) {
	svc, err := New("tester").WithOCR(WithOCRModel("glm-ocr")).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !svc.toolRegistry.Has("ocr_image") {
		t.Fatalf("WithOCR() did not register ocr_image")
	}
}
