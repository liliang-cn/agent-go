package providers

import (
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3/option"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// wireMiddleware reports each HTTP attempt of a model call to the call's Wire
// (trace.WireOf): the SDK's own retries too, which it makes inside one call
// and which were otherwise only a call that took longer. A request without a
// Wire goes through untouched.
func wireMiddleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	w := trace.WireOf(req.Context())
	if w == nil {
		return next(req)
	}
	if n := req.Header.Get("X-Stainless-Retry-Count"); n != "" && n != "0" {
		w.Retry(fmt.Sprintf("the SDK tries again (retry %s)", n), 0)
	}
	what := req.Method + " " + req.URL.Path
	if req.ContentLength > 0 {
		what += fmt.Sprintf(", %.1f KB", float64(req.ContentLength)/1024)
	}
	w.Request(what)
	resp, err := next(req)
	if err != nil {
		w.Failed(err)
		return resp, err
	}
	detail := ""
	if resp.StatusCode >= 400 {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			detail = "Retry-After " + ra
		}
	}
	w.Response(resp.StatusCode, detail)
	if resp.StatusCode < 400 {
		resp.Body = w.Body(resp.Body)
	}
	return resp, nil
}
