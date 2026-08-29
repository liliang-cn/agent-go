package pool

import "testing"

func TestParsePoolUsage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                                    string
		body                                    string
		wantNil                                 bool
		prompt, completion, cached, cacheWrites int
	}{
		{
			name:    "no usage object at all is unknown, not zero",
			body:    `{"choices":[{"message":{"content":"hi"}}]}`,
			wantNil: true,
		},
		{
			name:    "an all-zero usage object is also unknown",
			body:    `{"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
			wantNil: true,
		},
		{
			name:       "openai reports the hit count nested",
			body:       `{"usage":{"prompt_tokens":1200,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":1024}}}`,
			prompt:     1200,
			completion: 40,
			cached:     1024,
		},
		{
			name:       "deepseek reports it flat",
			body:       `{"usage":{"prompt_tokens":1200,"completion_tokens":40,"prompt_cache_hit_tokens":896,"prompt_cache_miss_tokens":304}}`,
			prompt:     1200,
			completion: 40,
			cached:     896,
		},
		{
			name:        "anthropic-style reads and writes are both kept",
			body:        `{"usage":{"prompt_tokens":900,"completion_tokens":20,"cache_read_input_tokens":700,"cache_creation_input_tokens":180}}`,
			prompt:      900,
			completion:  20,
			cached:      700,
			cacheWrites: 180,
		},
		{
			name: "a gateway reporting the same hit count twice does not double it",
			body: `{"usage":{"prompt_tokens":1000,"completion_tokens":10,
			         "prompt_cache_hit_tokens":768,"prompt_tokens_details":{"cached_tokens":768}}}`,
			prompt:     1000,
			completion: 10,
			cached:     768,
		},
		{
			name:       "a body that is not JSON at all reports unknown",
			body:       `not json`,
			wantNil:    true,
			prompt:     0,
			completion: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePoolUsage([]byte(tc.body))
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil usage, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected usage, got nil")
			}
			if got.PromptTokens != tc.prompt {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, tc.prompt)
			}
			if got.CompletionTokens != tc.completion {
				t.Errorf("CompletionTokens = %d, want %d", got.CompletionTokens, tc.completion)
			}
			if got.CachedPromptTokens != tc.cached {
				t.Errorf("CachedPromptTokens = %d, want %d", got.CachedPromptTokens, tc.cached)
			}
			if got.CacheWriteTokens != tc.cacheWrites {
				t.Errorf("CacheWriteTokens = %d, want %d", got.CacheWriteTokens, tc.cacheWrites)
			}
		})
	}
}
