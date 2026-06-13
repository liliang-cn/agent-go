package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/browser"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

func TestRegisterDeliverableToolsAndScan(t *testing.T) {
	sb, err := sandbox.NewLocal()
	if err != nil {
		t.Fatalf("new local sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	ctx := context.Background()

	// Lay out a few files + a nested dir + the snapshot tarball (which must be skipped).
	if err := sb.WriteFile(ctx, "report.md", []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(ctx, "data.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(ctx, "sub/shot.png", []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(ctx, "snapshot.tar.gz", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ScanDeliverables(ctx, sb)
	if err != nil {
		t.Fatalf("ScanDeliverables: %v", err)
	}
	byType := map[string]Deliverable{}
	for _, d := range got {
		byType[d.Type] = d
		if d.Path == "snapshot.tar.gz" {
			t.Fatalf("snapshot tarball should be skipped")
		}
	}
	for _, typ := range []string{"md", "json", "png"} {
		if _, ok := byType[typ]; !ok {
			t.Fatalf("expected a %q deliverable, got %+v", typ, got)
		}
	}

	svc := &Service{toolRegistry: NewToolRegistry()}
	RegisterDeliverableTools(svc, sb)
	if !svc.toolRegistry.Has("list_deliverables") {
		t.Fatal("expected list_deliverables to be registered")
	}
	data := mustOK(t, "list_deliverables", callTool(t, svc, "list_deliverables", map[string]interface{}{}))
	if items, _ := data["deliverables"].([]map[string]interface{}); len(items) != 3 {
		t.Fatalf("expected 3 deliverables (tarball skipped), got %+v", data)
	}
}

// fakeBrowser is a no-op browser.Browser used to assert tool registration without
// requiring a live Chrome.
type fakeBrowser struct{}

func (fakeBrowser) Navigate(ctx context.Context, url string) (browser.PageState, error) {
	return browser.PageState{URL: url}, nil
}
func (fakeBrowser) Back(ctx context.Context) error                      { return nil }
func (fakeBrowser) Forward(ctx context.Context) error                   { return nil }
func (fakeBrowser) Click(ctx context.Context, ref string) error         { return nil }
func (fakeBrowser) Type(ctx context.Context, ref, text string) error    { return nil }
func (fakeBrowser) Fill(ctx context.Context, f map[string]string) error { return nil }
func (fakeBrowser) Select(ctx context.Context, ref, value string) error { return nil }
func (fakeBrowser) Hover(ctx context.Context, ref string) error         { return nil }
func (fakeBrowser) Scroll(ctx context.Context, dx, dy int) error        { return nil }
func (fakeBrowser) WaitFor(ctx context.Context, c browser.WaitCond) error {
	return nil
}
func (fakeBrowser) ReadText(ctx context.Context, selector string) (string, error) { return "", nil }
func (fakeBrowser) Snapshot(ctx context.Context) (browser.Snapshot, error) {
	return browser.Snapshot{}, nil
}
func (fakeBrowser) Screenshot(ctx context.Context) ([]byte, error) { return []byte("png"), nil }
func (fakeBrowser) Evaluate(ctx context.Context, js string) (any, error) {
	return nil, nil
}
func (fakeBrowser) Console(ctx context.Context) ([]browser.ConsoleMsg, error) { return nil, nil }
func (fakeBrowser) Tabs(ctx context.Context) (browser.TabOps, error)          { return nil, nil }
func (fakeBrowser) Download(ctx context.Context, url, destPath string) error  { return nil }
func (fakeBrowser) Close() error                                              { return nil }

func TestRegisterBrowserToolsRegistersExpectedNames(t *testing.T) {
	svc := &Service{toolRegistry: NewToolRegistry()}
	RegisterBrowserTools(svc, fakeBrowser{}, nil)
	want := []string{
		"browser_navigate", "browser_back", "browser_read", "browser_snapshot",
		"browser_click", "browser_type", "browser_fill", "browser_select",
		"browser_scroll", "browser_wait", "browser_screenshot",
		"browser_evaluate", "browser_console", "browser_download",
	}
	for _, name := range want {
		if !svc.toolRegistry.Has(name) {
			t.Errorf("expected %q to be registered", name)
		}
	}

	// browser_download without a sandbox returns ok:false with a clear message.
	res := callTool(t, svc, "browser_download", map[string]interface{}{"url": "http://example.com/x"})
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("expected browser_download to fail without a sandbox, got %+v", res)
	}

	// browser_screenshot returns base64 PNG.
	data := mustOK(t, "browser_screenshot", callTool(t, svc, "browser_screenshot", map[string]interface{}{}))
	if mime, _ := data["mime"].(string); mime != "image/png" {
		t.Fatalf("expected image/png mime, got %+v", data)
	}
	if b64, _ := data["image_base64"].(string); b64 == "" {
		t.Fatalf("expected non-empty image_base64, got %+v", data)
	}
}
