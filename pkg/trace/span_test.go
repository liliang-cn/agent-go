package trace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTraceparentRoundTrips(t *testing.T) {
	in := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx := WithTraceparent(context.Background(), in)
	sc, ok := SpanFromContext(ctx)
	if !ok || sc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || sc.SpanID != "00f067aa0ba902b7" {
		t.Fatalf("parsed %+v", sc)
	}
	child, csc := StartSpan(ctx)
	if csc.TraceID != sc.TraceID || csc.ParentSpanID != sc.SpanID || csc.SpanID == sc.SpanID || !csc.Valid() {
		t.Fatalf("child %+v", csc)
	}
	h := http.Header{}
	h.Set(TracestateHeader, "vendor=1")
	back := ExtractHTTP(context.Background(), func() http.Header { InjectHTTP(child, h); return h }())
	bsc, _ := SpanFromContext(back)
	if bsc.TraceID != sc.TraceID || bsc.SpanID != csc.SpanID {
		t.Fatalf("round trip %+v", bsc)
	}
	for _, bad := range []string{"", "junk", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"} {
		if _, err := ParseTraceparent(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, root := StartSpan(context.Background()); root.ParentSpanID != "" || !root.Valid() {
		t.Fatalf("root %+v", root)
	}
}

func TestTracestatePassesThrough(t *testing.T) {
	ctx := Extract(context.Background(), headerMap(map[string]string{
		TraceparentHeader: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		TracestateHeader:  "cortex=abc",
	}).get)
	ctx, _ = StartSpan(ctx)
	got := map[string]string{}
	Inject(ctx, func(k, v string) { got[k] = v })
	if got[TracestateHeader] != "cortex=abc" || got[TraceparentHeader] == "" {
		t.Fatalf("injected %v", got)
	}
}

type headerMap map[string]string

func (m headerMap) get(k string) string { return m[k] }

// A server that answers with its own traceparent is linked to the call.
func TestHTTPTransportSendsAndCollectsTraceparent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(TraceparentHeader)
		w.Header().Set(TraceparentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-1111111111111111-01")
	}))
	defer srv.Close()
	ctx, sc := StartSpan(context.Background())
	ctx, links := WithLinks(ctx)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := (&http.Client{Transport: HTTPTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != sc.Traceparent() {
		t.Fatalf("sent %q, want %q", got, sc.Traceparent())
	}
	if l := links.List(); len(l) != 1 || l[0] != "00-4bf92f3577b34da6a3ce929d0e0e4736-1111111111111111-01" {
		t.Fatalf("links %v", l)
	}
}
