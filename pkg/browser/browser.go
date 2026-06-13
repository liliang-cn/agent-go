// Package browser provides a stateful, full-featured browser abstraction for
// AgentGo agents. The Browser interface is intentionally small and pragmatic:
// it exposes navigation, interaction (click/type/select/hover), an
// accessibility-ish Snapshot with stable refs the model can act on, text
// extraction, screenshots, arbitrary JS evaluation, console capture, tab
// management, and downloads.
//
// The default implementation, ChromedpBrowser, drives a real Chrome/Chromium
// instance via chromedp and holds a long-lived browser session (allocator +
// browser context) that is reused across calls, so cookies, scroll position,
// and open tabs persist for the lifetime of the Browser.
//
// All capabilities are optional and zero-dependency-by-default at the framework
// level: a bare AgentGo install is unaffected unless a Browser is wired in via
// builder options.
package browser

import (
	"context"
	"time"
)

// Browser is the stateful browser session an agent drives. Methods that take a
// "ref" accept either a CSS selector or a stable ref ("e1", "e2", …) produced
// by the most recent Snapshot.
type Browser interface {
	// Navigate loads url and returns the resulting page state.
	Navigate(ctx context.Context, url string) (PageState, error)
	// Back navigates one entry back in history.
	Back(ctx context.Context) error
	// Forward navigates one entry forward in history.
	Forward(ctx context.Context) error

	// Click clicks the element identified by ref (CSS selector or snapshot ref).
	Click(ctx context.Context, ref string) error
	// Type focuses the element identified by ref and types text into it.
	Type(ctx context.Context, ref, text string) error
	// Fill sets multiple fields at once, keyed by ref (selector or snapshot ref).
	Fill(ctx context.Context, fields map[string]string) error
	// Select chooses value in a <select> identified by ref.
	Select(ctx context.Context, ref, value string) error
	// Hover moves the pointer over the element identified by ref.
	Hover(ctx context.Context, ref string) error
	// Scroll scrolls the window by (dx, dy) pixels.
	Scroll(ctx context.Context, dx, dy int) error

	// WaitFor blocks until cond is satisfied or its timeout elapses.
	WaitFor(ctx context.Context, cond WaitCond) error
	// ReadText returns the innerText of selector (defaults to "body" when empty).
	ReadText(ctx context.Context, selector string) (string, error)
	// Snapshot returns a compact accessibility-ish tree plus a flat list of
	// interactable elements with stable refs.
	Snapshot(ctx context.Context) (Snapshot, error)
	// Screenshot captures the current page as PNG bytes.
	Screenshot(ctx context.Context) ([]byte, error)
	// Evaluate runs arbitrary JavaScript in the page and returns the result.
	Evaluate(ctx context.Context, js string) (any, error)
	// Console returns console messages captured since the session started.
	Console(ctx context.Context) ([]ConsoleMsg, error)
	// Tabs returns a handle for managing tabs.
	Tabs(ctx context.Context) (TabOps, error)
	// Download fetches url and writes the bytes to destPath, reusing the
	// session's cookies where possible.
	Download(ctx context.Context, url, destPath string) error
	// Close cancels the browser session and frees resources.
	Close() error
}

// PageState is a lightweight snapshot of where the browser currently is.
type PageState struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Element is a single interactable node discovered by Snapshot.
type Element struct {
	// Ref is a stable, snapshot-scoped identifier ("e1", "e2", …) usable in
	// Click/Type/Select/Hover/Fill in place of a CSS selector.
	Ref string `json:"ref"`
	// Role is an ARIA-ish role hint (button, link, textbox, checkbox, …).
	Role string `json:"role"`
	// Name is the accessible name / visible label of the element.
	Name string `json:"name"`
	// Selector is a CSS selector that resolves to this element.
	Selector string `json:"selector"`
}

// Snapshot is the result of Browser.Snapshot: a compact text tree for the model
// to read plus the flat element list backing the refs in that tree.
type Snapshot struct {
	// Tree is a compact, human-readable outline of interactable elements.
	Tree string `json:"tree"`
	// Elements is the flat list of interactable elements with their refs.
	Elements []Element `json:"elements"`
}

// WaitCond describes a condition for Browser.WaitFor. Conditions are checked in
// order of specificity: Selector (wait for it to be visible), then Text (poll
// the page until the substring appears), then NetworkIdle. If none are set,
// WaitFor waits for the document body to be ready.
type WaitCond struct {
	// Selector, when set, waits until this CSS selector is visible.
	Selector string `json:"selector,omitempty"`
	// Text, when set, waits until this substring appears in the page text.
	Text string `json:"text,omitempty"`
	// NetworkIdle, when true, waits for a short quiet period of no new requests.
	NetworkIdle bool `json:"network_idle,omitempty"`
	// Timeout bounds the wait; defaults to a sane value when zero.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// ConsoleMsg is a single captured browser console message.
type ConsoleMsg struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// TabInfo describes a single open tab/target.
type TabInfo struct {
	Index int    `json:"index"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// TabOps manages the browser's open tabs. The index used by Switch/Close
// matches the Index field returned by List.
type TabOps interface {
	// List returns the currently open page tabs.
	List() ([]TabInfo, error)
	// New opens a new tab, optionally navigating to url, and switches to it.
	New(url string) (TabInfo, error)
	// Switch makes the tab at idx the active one for subsequent operations.
	Switch(idx int) error
	// Close closes the tab at idx.
	Close(idx int) error
}
