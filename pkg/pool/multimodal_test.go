package pool

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// The bug this file exists for: the pool built every message as a plain
// content string and dropped msg.Parts, so a configured install — which is
// every install that goes through WithConfig — attached an image to a run and
// sent a request with no image in it. Nothing said so; the model answered the
// text, plausibly.
func TestMessagePartsReachTheWire(t *testing.T) {
	png := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(png, []byte("not really a png, but bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := buildPoolGenerateWithToolsRequest("m", []domain.Message{{
		Role:    "user",
		Content: "What is in this picture?",
		Parts:   []domain.MessagePart{domain.ImageLocalPathPart(png)},
	}}, nil, nil)

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, `"type":"image_url"`) {
		t.Fatalf("the request carries no image block:\n%s", body)
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("the image was not encoded as a data URI:\n%s", body)
	}
	if !strings.Contains(body, "What is in this picture?") {
		t.Fatal("the text went missing when parts were added")
	}
}

// A message with no parts stays a plain string. A content array where a
// string would do is a difference some servers notice, and every message in
// an ordinary run has no parts.
func TestPlainMessagesStayPlain(t *testing.T) {
	req := buildPoolGenerateWithToolsRequest("m", []domain.Message{{Role: "user", Content: "hello"}}, nil, nil)
	msgs := req["messages"].([]map[string]interface{})
	if _, ok := msgs[0]["content"].(string); !ok {
		t.Fatalf("content = %T, want a plain string", msgs[0]["content"])
	}
}

func TestPartForWireShapes(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "clip.mp3")
	doc := filepath.Join(dir, "report.pdf")
	for _, p := range []string{audio, doc} {
		if err := os.WriteFile(p, []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Audio: OpenAI's input_audio block, format taken from the extension.
	block, ok := partForWire(domain.AudioLocalPathPart(audio))
	if !ok || block["type"] != "input_audio" {
		t.Fatalf("audio block = %+v", block)
	}
	if got := block["input_audio"].(map[string]interface{})["format"]; got != "mp3" {
		t.Errorf("format = %v, want mp3 from the extension", got)
	}

	// File: a data URI plus the filename some providers require.
	block, ok = partForWire(domain.FileLocalPathPart(doc))
	if !ok || block["type"] != "file" {
		t.Fatalf("file block = %+v", block)
	}
	file := block["file"].(map[string]interface{})
	if !strings.HasPrefix(file["file_data"].(string), "data:application/pdf;base64,") {
		t.Errorf("file_data = %v", file["file_data"])
	}
	if file["filename"] != "report.pdf" {
		t.Errorf("filename = %v", file["filename"])
	}

	// A part it cannot render is skipped, not sent as something else: an
	// unreadable file is a missing attachment, and a missing attachment
	// described in the wrong format is a confused turn.
	if _, ok := partForWire(domain.ImageLocalPathPart(filepath.Join(dir, "nope.png"))); ok {
		t.Error("an unreadable image was rendered anyway")
	}
	if _, ok := partForWire(domain.MessagePart{Type: "video"}); ok {
		t.Error("an unknown part type was rendered")
	}
}

// A base64 image or an http URL the caller already built is passed through
// rather than wrapped in a second data URI.
func TestImageURLPassthrough(t *testing.T) {
	for _, in := range []string{"data:image/png;base64,AAAA", "https://example.com/a.png"} {
		got, ok := imageURLForWire(&domain.MessageImage{Base64: in})
		if !ok || got != in {
			t.Errorf("imageURLForWire(%q) = %q, %v", in, got, ok)
		}
	}
	got, ok := imageURLForWire(&domain.MessageImage{Base64: "AAAA", MIMEType: "image/jpeg"})
	if !ok || got != "data:image/jpeg;base64,AAAA" {
		t.Errorf("raw base64 = %q", got)
	}
}

// The output side, in the shape a gateway actually returns — confirmed
// against a live image model: message.images[].image_url.url is a data URI.
func TestOutputImagesAreParsed(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("jpegbytes"))
	images := []wireOutputImage{{Type: "image_url"}}
	images[0].ImageURL.URL = "data:image/jpeg;base64," + payload

	parts := outputPartsFromMessage(images)
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if parts[0].Type != domain.MessagePartTypeImage || parts[0].Image == nil {
		t.Fatalf("part = %+v", parts[0])
	}
	if parts[0].Image.Base64 != payload {
		t.Errorf("payload = %q, want the base64 without the data: prefix", parts[0].Image.Base64)
	}
	if parts[0].Image.MIMEType != "image/jpeg" {
		t.Errorf("mime = %q", parts[0].Image.MIMEType)
	}

	if outputPartsFromMessage(nil) != nil {
		t.Error("no images should produce no parts")
	}
	// A plain URL keeps something the caller can still fetch.
	plain := []wireOutputImage{{Type: "image_url"}}
	plain[0].ImageURL.URL = "https://example.com/a.png"
	if got := outputPartsFromMessage(plain); len(got) != 1 || got[0].Image.Base64 != "https://example.com/a.png" {
		t.Errorf("plain url = %+v", got)
	}
}
