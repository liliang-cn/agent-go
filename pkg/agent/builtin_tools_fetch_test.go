package agent

import (
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	in := `<html><head><style>.x{color:red}</style><script>var a=1<2;</script></head>` +
		`<body><h1>Hi &amp; Bye</h1><p>line  one</p>
		<p>line two</p></body></html>`
	got := htmlToText(in)
	if strings.Contains(got, "color:red") || strings.Contains(got, "var a=") {
		t.Fatalf("script/style not stripped: %q", got)
	}
	if !strings.Contains(got, "Hi & Bye") {
		t.Fatalf("entity not unescaped / heading lost: %q", got)
	}
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Fatalf("body text lost: %q", got)
	}
	if strings.Contains(got, "<") {
		t.Fatalf("tags not stripped: %q", got)
	}
}

func TestFetchHostBlocked(t *testing.T) {
	blocked := []string{"localhost", "127.0.0.1", "::1", "10.0.0.5", "192.168.1.1", "169.254.1.1", "0.0.0.0", ""}
	for _, h := range blocked {
		if !fetchHostBlocked(h) {
			t.Fatalf("expected %q to be blocked", h)
		}
	}
	// public IP literals (no DNS) must be allowed
	for _, h := range []string{"8.8.8.8", "1.1.1.1"} {
		if fetchHostBlocked(h) {
			t.Fatalf("expected %q to be allowed", h)
		}
	}
}
