package agent

import (
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestExtractToolImageParts_ScreenshotShape(t *testing.T) {
	result := map[string]interface{}{
		"ok": true,
		"data": map[string]interface{}{
			"image_base64": "QUJD", // "ABC"
			"mime":         "image/png",
		},
	}
	parts, cleaned := extractToolImageParts(result)
	if len(parts) != 1 {
		t.Fatalf("expected 1 image part, got %d", len(parts))
	}
	if parts[0].Type != domain.MessagePartTypeImage || parts[0].Image == nil {
		t.Fatalf("expected an image part, got %+v", parts[0])
	}
	if parts[0].Image.Base64 != "QUJD" || parts[0].Image.MIMEType != "image/png" {
		t.Fatalf("image part lost data: %+v", parts[0].Image)
	}
	// The cleaned result must no longer carry the raw base64 (keeps it out of
	// the text tool message / history).
	cleanedData := cleaned.(map[string]interface{})["data"].(map[string]interface{})
	if b64, _ := cleanedData["image_base64"].(string); strings.Contains(b64, "QUJD") {
		t.Fatalf("cleaned result still contains raw base64: %v", b64)
	}
}

func TestExtractToolImageParts_NoImagePassthrough(t *testing.T) {
	// A normal text-only result must be returned unchanged with no parts, so the
	// text-only path is unaffected.
	result := map[string]interface{}{"ok": true, "data": map[string]interface{}{"text": "hello"}}
	parts, out := extractToolImageParts(result)
	if parts != nil {
		t.Fatalf("expected no image parts, got %d", len(parts))
	}
	data := out.(map[string]interface{})["data"].(map[string]interface{})
	if data["text"] != "hello" {
		t.Fatalf("non-image result was mutated: %+v", out)
	}
}

func TestExtractToolImageParts_NonMapPassthrough(t *testing.T) {
	parts, out := extractToolImageParts("just a string")
	if parts != nil || out != "just a string" {
		t.Fatalf("string result should pass through unchanged, got parts=%v out=%v", parts, out)
	}
}
