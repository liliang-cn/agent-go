package pool

import (
	"net/http"
	"net/url"
)

// NewAnthropicTestClient is a client for api.anthropic.com whose requests go
// to base instead, through the same Messages-API translation, for tests
// outside this package.
func NewAnthropicTestClient(base, key, model string) (*Client, error) {
	c, err := NewClient("anthropic", "https://api.anthropic.com/v1", key, model)
	if err != nil {
		return nil, err
	}
	local, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	c.http.Transport = anthropicTransport{base: wireTransport{base: redirect{to: local}}}
	return c, nil
}

var _ http.RoundTripper = redirect{}
