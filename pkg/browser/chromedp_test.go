package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const staticPage = `<!doctype html>
<html><head><title>Static Page</title></head>
<body>
  <h1 id="heading">Welcome Home</h1>
  <p>The quick brown fox jumps over the lazy dog.</p>
  <a href="/form">Go to form</a>
</body></html>`

const formPage = `<!doctype html>
<html><head><title>Form Page</title></head>
<body>
  <h1>Form</h1>
  <input id="name" type="text" placeholder="your name" />
  <button id="go" onclick="document.getElementById('out').innerText = 'Hello ' + document.getElementById('name').value">Greet</button>
  <div id="out"></div>
</body></html>`

func newTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(staticPage))
	})
	mux.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(formPage))
	})
	return httptest.NewServer(mux)
}

// newBrowserOrSkip constructs a headless browser, skipping the test when Chrome
// is unavailable in the environment.
func newBrowserOrSkip(t *testing.T) *ChromedpBrowser {
	t.Helper()
	b, err := NewChromedp(WithHeadless(true), WithTimeout(20*time.Second))
	if err != nil {
		t.Skipf("chrome/chromium unavailable, skipping browser test: %v", err)
	}
	return b
}

func TestNavigateAndReadText(t *testing.T) {
	b := newBrowserOrSkip(t)
	defer b.Close()
	srv := newTestServer()
	defer srv.Close()

	ctx := context.Background()
	ps, err := b.Navigate(ctx, srv.URL+"/")
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if ps.Title != "Static Page" {
		t.Errorf("title = %q, want %q", ps.Title, "Static Page")
	}

	text, err := b.ReadText(ctx, "body")
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	if !strings.Contains(text, "Welcome Home") {
		t.Errorf("body text missing heading; got: %q", text)
	}
}

func TestSnapshotReturnsElements(t *testing.T) {
	b := newBrowserOrSkip(t)
	defer b.Close()
	srv := newTestServer()
	defer srv.Close()

	ctx := context.Background()
	if _, err := b.Navigate(ctx, srv.URL+"/form"); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	snap, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Elements) == 0 {
		t.Fatalf("expected snapshot elements, got none; tree=%q", snap.Tree)
	}
	var sawInput, sawButton bool
	for _, e := range snap.Elements {
		if e.Role == "textbox" {
			sawInput = true
		}
		if e.Role == "button" {
			sawButton = true
		}
		if e.Ref == "" || e.Selector == "" {
			t.Errorf("element missing ref/selector: %+v", e)
		}
	}
	if !sawInput || !sawButton {
		t.Errorf("expected a textbox and a button in snapshot; got %+v", snap.Elements)
	}
}

func TestTypeClickAndReadUpdatedDOM(t *testing.T) {
	b := newBrowserOrSkip(t)
	defer b.Close()
	srv := newTestServer()
	defer srv.Close()

	ctx := context.Background()
	if _, err := b.Navigate(ctx, srv.URL+"/form"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := b.Type(ctx, "#name", "Ada"); err != nil {
		t.Fatalf("type: %v", err)
	}
	if err := b.Click(ctx, "#go"); err != nil {
		t.Fatalf("click: %v", err)
	}

	// The inline onclick updates #out synchronously; give a brief poll.
	if err := b.WaitFor(ctx, WaitCond{Text: "Hello Ada", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("wait for updated text: %v", err)
	}
	out, err := b.ReadText(ctx, "#out")
	if err != nil {
		t.Fatalf("read #out: %v", err)
	}
	if !strings.Contains(out, "Hello Ada") {
		t.Errorf("#out = %q, want to contain %q", out, "Hello Ada")
	}
}

func TestSnapshotRefClick(t *testing.T) {
	b := newBrowserOrSkip(t)
	defer b.Close()
	srv := newTestServer()
	defer srv.Close()

	ctx := context.Background()
	if _, err := b.Navigate(ctx, srv.URL+"/form"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	snap, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var inputRef, buttonRef string
	for _, e := range snap.Elements {
		if e.Role == "textbox" && inputRef == "" {
			inputRef = e.Ref
		}
		if e.Role == "button" && buttonRef == "" {
			buttonRef = e.Ref
		}
	}
	if inputRef == "" || buttonRef == "" {
		t.Skipf("snapshot did not yield expected refs: %+v", snap.Elements)
	}
	if err := b.Type(ctx, inputRef, "Grace"); err != nil {
		t.Fatalf("type by ref: %v", err)
	}
	if err := b.Click(ctx, buttonRef); err != nil {
		t.Fatalf("click by ref: %v", err)
	}
	if err := b.WaitFor(ctx, WaitCond{Text: "Hello Grace", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("wait for updated text: %v", err)
	}
}

func TestScreenshotReturnsPNG(t *testing.T) {
	b := newBrowserOrSkip(t)
	defer b.Close()
	srv := newTestServer()
	defer srv.Close()

	ctx := context.Background()
	if _, err := b.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	img, err := b.Screenshot(ctx)
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if len(img) == 0 {
		t.Fatal("screenshot returned no bytes")
	}
	// PNG magic number.
	if len(img) < 8 || img[0] != 0x89 || img[1] != 'P' || img[2] != 'N' || img[3] != 'G' {
		n := len(img)
		if n > 8 {
			n = 8
		}
		t.Errorf("screenshot is not PNG; first bytes: % x", img[:n])
	}
}

func TestEvaluate(t *testing.T) {
	b := newBrowserOrSkip(t)
	defer b.Close()
	srv := newTestServer()
	defer srv.Close()

	ctx := context.Background()
	if _, err := b.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	res, err := b.Evaluate(ctx, "1 + 2")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// JSON numbers decode to float64.
	if f, ok := res.(float64); !ok || f != 3 {
		t.Errorf("evaluate 1+2 = %v (%T), want 3", res, res)
	}
}
