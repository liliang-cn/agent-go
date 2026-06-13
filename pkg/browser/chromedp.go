package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// defaultTimeout bounds individual operations when the caller's context has no
// deadline of its own.
const defaultTimeout = 30 * time.Second

// options holds ChromedpBrowser construction settings.
type options struct {
	headless    bool
	execPath    string
	userDataDir string
	timeout     time.Duration
}

// Option configures a ChromedpBrowser.
type Option func(*options)

// WithHeadless toggles headless mode (default true).
func WithHeadless(on bool) Option {
	return func(o *options) { o.headless = on }
}

// WithExecPath sets an explicit Chrome/Chromium executable path. When empty,
// chromedp auto-detects an installed browser.
func WithExecPath(path string) Option {
	return func(o *options) { o.execPath = path }
}

// WithUserDataDir sets a persistent Chrome user-data directory (for cookies,
// logins, etc). When empty, Chrome uses a fresh temporary profile.
func WithUserDataDir(dir string) Option {
	return func(o *options) { o.userDataDir = dir }
}

// WithTimeout sets the default per-operation timeout used when the caller's
// context carries no deadline.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// ChromedpBrowser is a stateful Browser backed by a long-lived chromedp
// session. The allocator and root browser context live for the lifetime of the
// instance; per-call work runs on the currently active tab context.
type ChromedpBrowser struct {
	opts options

	allocCtx    context.Context
	allocCancel context.CancelFunc
	browserCtx  context.Context
	browserCxl  context.CancelFunc

	mu       sync.Mutex
	active   context.Context // currently active tab context
	refs     map[string]string
	console  []ConsoleMsg
	tabCxls  []context.CancelFunc // cancel funcs for tabs we opened (index-aligned best effort)
	closed   bool
	listened map[context.Context]bool
}

// compile-time interface check.
var _ Browser = (*ChromedpBrowser)(nil)

// NewChromedp constructs a ChromedpBrowser and starts its browser session. It
// returns an error if Chrome/Chromium cannot be launched, so callers can fall
// back gracefully.
func NewChromedp(opts ...Option) (*ChromedpBrowser, error) {
	o := options{headless: true, timeout: defaultTimeout}
	for _, fn := range opts {
		fn(&o)
	}

	allocOpts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if o.headless {
		allocOpts = append(allocOpts, chromedp.Headless)
	} else {
		// Drop the headless flag that DefaultExecAllocatorOptions includes.
		allocOpts = append(allocOpts, chromedp.Flag("headless", false))
	}
	allocOpts = append(allocOpts,
		chromedp.NoSandbox,
		chromedp.DisableGPU,
	)
	if o.execPath != "" {
		allocOpts = append(allocOpts, chromedp.ExecPath(o.execPath))
	}
	if o.userDataDir != "" {
		allocOpts = append(allocOpts, chromedp.UserDataDir(o.userDataDir))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	browserCtx, browserCxl := chromedp.NewContext(allocCtx)

	b := &ChromedpBrowser{
		opts:        o,
		allocCtx:    allocCtx,
		allocCancel: allocCancel,
		browserCtx:  browserCtx,
		browserCxl:  browserCxl,
		active:      browserCtx,
		refs:        make(map[string]string),
		listened:    make(map[context.Context]bool),
	}

	// Force the browser to actually start so launch failures surface here.
	// NOTE: the very first Run binds the tab/target to this exact context, so
	// it must be browserCtx itself — wrapping it in a cancellable child and
	// cancelling that child would tear the tab down. We bound the startup with
	// a watchdog goroutine instead of a child context.
	if err := b.runStart(browserCtx); err != nil {
		allocCancel()
		return nil, fmt.Errorf("browser: failed to start chrome: %w", err)
	}

	b.attachConsole(browserCtx)
	return b, nil
}

// runStart performs the first Run that binds a tab/target to tabCtx. It must
// not cancel tabCtx on timeout (that would destroy the tab), so it uses a
// watchdog: the Run executes on tabCtx directly and a timer bounds how long we
// wait for it to report readiness.
func (b *ChromedpBrowser) runStart(tabCtx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(tabCtx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(b.opts.timeout):
		return fmt.Errorf("timed out after %s waiting for chrome to start", b.opts.timeout)
	}
}

// attachConsole wires runtime console capture onto a tab context once.
func (b *ChromedpBrowser) attachConsole(ctx context.Context) {
	b.mu.Lock()
	if b.listened[ctx] {
		b.mu.Unlock()
		return
	}
	b.listened[ctx] = true
	b.mu.Unlock()

	chromedp.ListenTarget(ctx, func(ev any) {
		e, ok := ev.(*runtime.EventConsoleAPICalled)
		if !ok {
			return
		}
		var parts []string
		for _, arg := range e.Args {
			parts = append(parts, remoteObjectString(arg))
		}
		b.mu.Lock()
		b.console = append(b.console, ConsoleMsg{Level: string(e.Type), Text: strings.Join(parts, " ")})
		b.mu.Unlock()
	})
}

// remoteObjectString renders a console arg as best it can.
func remoteObjectString(o *runtime.RemoteObject) string {
	if o == nil {
		return ""
	}
	if len(o.Value) > 0 {
		var s string
		if err := json.Unmarshal([]byte(o.Value), &s); err == nil {
			return s
		}
		return string(o.Value)
	}
	if o.Description != "" {
		return o.Description
	}
	return string(o.Type)
}

// opCtx derives a bounded context for a single operation from the active tab.
func (b *ChromedpBrowser) opCtx(parent context.Context) (context.Context, context.CancelFunc) {
	b.mu.Lock()
	active := b.active
	to := b.opts.timeout
	b.mu.Unlock()

	// Bind cancellation: the chromedp action must run on the tab context, but
	// honor the caller's cancellation/deadline too.
	if _, hasDeadline := parent.Deadline(); !hasDeadline {
		return context.WithTimeout(active, to)
	}
	// Caller has its own deadline; still cap with our default to avoid hangs.
	return context.WithTimeout(active, to)
}

// resolve turns a ref (e1, e2, …) into a CSS selector, or returns sel unchanged
// when it is not a known ref.
func (b *ChromedpBrowser) resolve(sel string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if css, ok := b.refs[sel]; ok {
		return css
	}
	return sel
}

func (b *ChromedpBrowser) run(parent context.Context, actions ...chromedp.Action) error {
	ctx, cancel := b.opCtx(parent)
	defer cancel()
	return chromedp.Run(ctx, actions...)
}

// Navigate loads url and returns the resulting page state.
func (b *ChromedpBrowser) Navigate(ctx context.Context, url string) (PageState, error) {
	var ps PageState
	err := b.run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body"),
		chromedp.Location(&ps.URL),
		chromedp.Title(&ps.Title),
	)
	if err != nil {
		return PageState{}, fmt.Errorf("browser: navigate %q: %w", url, err)
	}
	return ps, nil
}

// Back navigates one entry back in history.
func (b *ChromedpBrowser) Back(ctx context.Context) error {
	return b.run(ctx, chromedp.NavigateBack())
}

// Forward navigates one entry forward in history.
func (b *ChromedpBrowser) Forward(ctx context.Context) error {
	return b.run(ctx, chromedp.NavigateForward())
}

// Click clicks the element identified by ref (selector or snapshot ref).
func (b *ChromedpBrowser) Click(ctx context.Context, ref string) error {
	sel := b.resolve(ref)
	if err := b.run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		return fmt.Errorf("browser: click %q: %w", ref, err)
	}
	return nil
}

// Type focuses the element identified by ref and types text into it.
func (b *ChromedpBrowser) Type(ctx context.Context, ref, text string) error {
	sel := b.resolve(ref)
	if err := b.run(ctx,
		chromedp.WaitVisible(sel, chromedp.ByQuery),
		chromedp.Focus(sel, chromedp.ByQuery),
		chromedp.SendKeys(sel, text, chromedp.ByQuery),
	); err != nil {
		return fmt.Errorf("browser: type into %q: %w", ref, err)
	}
	return nil
}

// Fill sets multiple fields at once.
func (b *ChromedpBrowser) Fill(ctx context.Context, fields map[string]string) error {
	for ref, text := range fields {
		sel := b.resolve(ref)
		if err := b.run(ctx, chromedp.SetValue(sel, text, chromedp.ByQuery)); err != nil {
			return fmt.Errorf("browser: fill %q: %w", ref, err)
		}
	}
	return nil
}

// Select chooses value in a <select> identified by ref.
func (b *ChromedpBrowser) Select(ctx context.Context, ref, value string) error {
	sel := b.resolve(ref)
	if err := b.run(ctx, chromedp.SetValue(sel, value, chromedp.ByQuery)); err != nil {
		return fmt.Errorf("browser: select %q on %q: %w", value, ref, err)
	}
	return nil
}

// Hover moves the pointer over the element identified by ref.
func (b *ChromedpBrowser) Hover(ctx context.Context, ref string) error {
	sel := b.resolve(ref)
	if err := b.run(ctx, chromedp.ScrollIntoView(sel, chromedp.ByQuery)); err != nil {
		return fmt.Errorf("browser: hover %q: %w", ref, err)
	}
	return nil
}

// Scroll scrolls the window by (dx, dy) pixels.
func (b *ChromedpBrowser) Scroll(ctx context.Context, dx, dy int) error {
	js := fmt.Sprintf("window.scrollBy(%d, %d)", dx, dy)
	return b.run(ctx, chromedp.Evaluate(js, nil))
}

// WaitFor blocks until cond is satisfied or its timeout elapses.
func (b *ChromedpBrowser) WaitFor(ctx context.Context, cond WaitCond) error {
	to := cond.Timeout
	if to <= 0 {
		to = b.opts.timeout
	}

	b.mu.Lock()
	active := b.active
	b.mu.Unlock()
	wctx, cancel := context.WithTimeout(active, to)
	defer cancel()

	switch {
	case cond.Selector != "":
		if err := chromedp.Run(wctx, chromedp.WaitVisible(cond.Selector, chromedp.ByQuery)); err != nil {
			return fmt.Errorf("browser: wait for selector %q: %w", cond.Selector, err)
		}
	case cond.Text != "":
		return b.pollForText(wctx, cond.Text)
	case cond.NetworkIdle:
		// Approximate network idle with a short settle delay plus body ready.
		if err := chromedp.Run(wctx, chromedp.WaitReady("body")); err != nil {
			return fmt.Errorf("browser: wait network idle: %w", err)
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-wctx.Done():
			return wctx.Err()
		}
	default:
		if err := chromedp.Run(wctx, chromedp.WaitReady("body")); err != nil {
			return fmt.Errorf("browser: wait ready: %w", err)
		}
	}
	return nil
}

// pollForText repeatedly checks document text until substr appears or ctx ends.
func (b *ChromedpBrowser) pollForText(ctx context.Context, substr string) error {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		var body string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &body))
		if strings.Contains(body, substr) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("browser: text %q not found before timeout: %w", substr, ctx.Err())
		case <-ticker.C:
		}
	}
}

// ReadText returns the innerText of selector (defaults to "body").
func (b *ChromedpBrowser) ReadText(ctx context.Context, selector string) (string, error) {
	if selector == "" {
		selector = "body"
	}
	selector = b.resolve(selector)
	var text string
	if err := b.run(ctx, chromedp.Text(selector, &text, chromedp.ByQuery, chromedp.NodeReady)); err != nil {
		return "", fmt.Errorf("browser: read text %q: %w", selector, err)
	}
	return text, nil
}

// snapshotJS walks the DOM, tags interactable elements with a data-agentref
// attribute, and returns their role/name/selector. Kept pragmatic: it covers
// links, buttons, inputs, textareas, selects, and elements with click/role.
const snapshotJS = `
(function() {
  function cssPath(el) {
    if (el.id) return '#' + CSS.escape(el.id);
    var parts = [];
    while (el && el.nodeType === 1 && parts.length < 6) {
      var sel = el.nodeName.toLowerCase();
      if (el.parentNode) {
        var sib = el.parentNode.children;
        var idx = 0, n = 0;
        for (var i = 0; i < sib.length; i++) {
          if (sib[i].nodeName === el.nodeName) { n++; if (sib[i] === el) idx = n; }
        }
        if (n > 1) sel += ':nth-of-type(' + idx + ')';
      }
      parts.unshift(sel);
      el = el.parentElement;
    }
    return parts.join(' > ');
  }
  function role(el) {
    var r = el.getAttribute('role');
    if (r) return r;
    var tag = el.nodeName.toLowerCase();
    if (tag === 'a') return 'link';
    if (tag === 'button') return 'button';
    if (tag === 'select') return 'combobox';
    if (tag === 'textarea') return 'textbox';
    if (tag === 'input') {
      var t = (el.getAttribute('type') || 'text').toLowerCase();
      if (t === 'checkbox') return 'checkbox';
      if (t === 'radio') return 'radio';
      if (t === 'submit' || t === 'button') return 'button';
      return 'textbox';
    }
    return tag;
  }
  function name(el) {
    var n = el.getAttribute('aria-label') || el.getAttribute('placeholder') ||
            el.getAttribute('name') || el.getAttribute('value') || '';
    if (!n) n = (el.innerText || el.textContent || '').trim();
    return n.replace(/\s+/g, ' ').slice(0, 120);
  }
  var sels = 'a[href], button, input, textarea, select, [role], [onclick]';
  var nodes = Array.prototype.slice.call(document.querySelectorAll(sels));
  var out = [];
  var i = 1;
  nodes.forEach(function(el) {
    var style = window.getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden') return;
    var rect = el.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) return;
    var ref = 'e' + i;
    el.setAttribute('data-agentref', ref);
    out.push({ ref: ref, role: role(el), name: name(el), selector: '[data-agentref="' + ref + '"]' });
    i++;
  });
  return out;
})()
`

// Snapshot returns a compact tree plus the interactable element list.
func (b *ChromedpBrowser) Snapshot(ctx context.Context) (Snapshot, error) {
	var raw []Element
	if err := b.run(ctx, chromedp.Evaluate(snapshotJS, &raw)); err != nil {
		return Snapshot{}, fmt.Errorf("browser: snapshot: %w", err)
	}

	b.mu.Lock()
	b.refs = make(map[string]string, len(raw))
	for _, e := range raw {
		b.refs[e.Ref] = e.Selector
	}
	b.mu.Unlock()

	var sb strings.Builder
	for _, e := range raw {
		name := e.Name
		if name == "" {
			name = "(no name)"
		}
		fmt.Fprintf(&sb, "- [%s] %s: %q\n", e.Ref, e.Role, name)
	}
	return Snapshot{Tree: sb.String(), Elements: raw}, nil
}

// Screenshot captures the current page as PNG bytes. It captures the visible
// viewport (chromedp.CaptureScreenshot encodes PNG; the full-page variant only
// emits JPEG, which would break the documented PNG contract).
func (b *ChromedpBrowser) Screenshot(ctx context.Context) ([]byte, error) {
	var buf []byte
	if err := b.run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		return nil, fmt.Errorf("browser: screenshot: %w", err)
	}
	return buf, nil
}

// Evaluate runs arbitrary JavaScript and returns the result.
func (b *ChromedpBrowser) Evaluate(ctx context.Context, js string) (any, error) {
	var res any
	if err := b.run(ctx, chromedp.Evaluate(js, &res)); err != nil {
		return nil, fmt.Errorf("browser: evaluate: %w", err)
	}
	return res, nil
}

// Console returns console messages captured since the session started.
func (b *ChromedpBrowser) Console(ctx context.Context) ([]ConsoleMsg, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ConsoleMsg, len(b.console))
	copy(out, b.console)
	return out, nil
}

// Download fetches url and writes the bytes to destPath, reusing the browser
// session's cookies where possible.
func (b *ChromedpBrowser) Download(ctx context.Context, url, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("browser: download mkdir: %w", err)
	}

	// Best effort: pull cookies from the live session and replay them with a
	// plain HTTP client so large downloads don't have to go through CDP.
	cookieHeader, _ := b.cookieHeaderFor(ctx, url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("browser: download request: %w", err)
	}
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AgentGo-Browser)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("browser: download fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("browser: download %q: status %d", url, resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("browser: download create: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("browser: download write: %w", err)
	}
	return nil
}

// cookieHeaderFor collects matching cookies from the live session for url.
func (b *ChromedpBrowser) cookieHeaderFor(ctx context.Context, url string) (string, error) {
	var cookies []*network.Cookie
	cctx, cancel := b.opCtx(ctx)
	defer cancel()
	err := chromedp.Run(cctx, chromedp.ActionFunc(func(ctx context.Context) error {
		p := network.GetCookies()
		p.URLs = []string{url}
		c, err := p.Do(ctx)
		if err != nil {
			return err
		}
		cookies = c
		return nil
	}))
	if err != nil {
		return "", err
	}
	var parts []string
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; "), nil
}

// Tabs returns a handle for managing tabs.
func (b *ChromedpBrowser) Tabs(ctx context.Context) (TabOps, error) {
	return &chromedpTabs{b: b}, nil
}

// Close cancels the browser session and frees resources.
func (b *ChromedpBrowser) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cxls := b.tabCxls
	b.tabCxls = nil
	b.mu.Unlock()

	for _, c := range cxls {
		if c != nil {
			c()
		}
	}
	if b.browserCxl != nil {
		b.browserCxl()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	return nil
}

// chromedpTabs implements TabOps over chromedp target contexts.
type chromedpTabs struct {
	b *ChromedpBrowser
}

// List returns the currently open page tabs.
func (t *chromedpTabs) List() ([]TabInfo, error) {
	infos, err := chromedp.Targets(t.b.browserCtx)
	if err != nil {
		return nil, fmt.Errorf("browser: list tabs: %w", err)
	}
	var out []TabInfo
	idx := 0
	for _, info := range infos {
		if info.Type != "page" {
			continue
		}
		out = append(out, TabInfo{Index: idx, URL: info.URL, Title: info.Title})
		idx++
	}
	return out, nil
}

// New opens a new tab, optionally navigating to url, and switches to it.
func (t *chromedpTabs) New(url string) (TabInfo, error) {
	newCtx, cancel := chromedp.NewContext(t.b.browserCtx)
	if err := t.b.runStart(newCtx); err != nil {
		cancel()
		return TabInfo{}, fmt.Errorf("browser: new tab: %w", err)
	}
	t.b.attachConsole(newCtx)

	var ps PageState
	if url != "" {
		navCtx, nc := context.WithTimeout(newCtx, t.b.opts.timeout)
		err := chromedp.Run(navCtx,
			chromedp.Navigate(url),
			chromedp.WaitReady("body"),
			chromedp.Location(&ps.URL),
			chromedp.Title(&ps.Title),
		)
		nc()
		if err != nil {
			cancel()
			return TabInfo{}, fmt.Errorf("browser: new tab navigate: %w", err)
		}
	}

	t.b.mu.Lock()
	t.b.tabCxls = append(t.b.tabCxls, cancel)
	idx := len(t.b.tabCxls) - 1
	t.b.active = newCtx
	t.b.refs = make(map[string]string)
	t.b.mu.Unlock()

	return TabInfo{Index: idx, URL: ps.URL, Title: ps.Title}, nil
}

// Switch makes the tab at idx the active one for subsequent operations.
func (t *chromedpTabs) Switch(idx int) error {
	infos, err := chromedp.Targets(t.b.browserCtx)
	if err != nil {
		return fmt.Errorf("browser: switch tab: %w", err)
	}
	var pages []*target.Info
	for _, info := range infos {
		if info.Type == "page" {
			pages = append(pages, info)
		}
	}
	if idx < 0 || idx >= len(pages) {
		return fmt.Errorf("browser: switch tab: index %d out of range (%d tabs)", idx, len(pages))
	}
	tctx, cancel := chromedp.NewContext(t.b.browserCtx, chromedp.WithTargetID(pages[idx].TargetID))
	if err := t.b.runStart(tctx); err != nil {
		cancel()
		return fmt.Errorf("browser: switch tab attach: %w", err)
	}
	t.b.attachConsole(tctx)
	t.b.mu.Lock()
	t.b.tabCxls = append(t.b.tabCxls, cancel)
	t.b.active = tctx
	t.b.refs = make(map[string]string)
	t.b.mu.Unlock()
	return nil
}

// Close closes the tab at idx.
func (t *chromedpTabs) Close(idx int) error {
	infos, err := chromedp.Targets(t.b.browserCtx)
	if err != nil {
		return fmt.Errorf("browser: close tab: %w", err)
	}
	var pages []*target.Info
	for _, info := range infos {
		if info.Type == "page" {
			pages = append(pages, info)
		}
	}
	if idx < 0 || idx >= len(pages) {
		return fmt.Errorf("browser: close tab: index %d out of range (%d tabs)", idx, len(pages))
	}
	tctx, cancel := chromedp.NewContext(t.b.browserCtx, chromedp.WithTargetID(pages[idx].TargetID))
	defer cancel()
	if err := t.b.runStart(tctx); err != nil {
		return fmt.Errorf("browser: close tab attach: %w", err)
	}
	if err := chromedp.Cancel(tctx); err != nil {
		return fmt.Errorf("browser: close tab: %w", err)
	}
	return nil
}
