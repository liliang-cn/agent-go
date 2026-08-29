package pool

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// cacheControlOn reports whether an entry carries the marker, whether it sits
// on the entry itself (a tool) or inside its single content block (a message).
func cacheControlOn(entry map[string]interface{}) bool {
	if _, ok := entry["cache_control"]; ok {
		return true
	}
	blocks, ok := entry["content"].([]interface{})
	if !ok || len(blocks) == 0 {
		return false
	}
	block, ok := blocks[0].(map[string]interface{})
	if !ok {
		return false
	}
	_, ok = block["cache_control"]
	return ok
}

func msg(role, content string) map[string]interface{} {
	return map[string]interface{}{"role": role, "content": content}
}

func TestMarkPromptCacheBreakpointsPrefixSitsOnTheLastTool(t *testing.T) {
	t.Parallel()
	messages := []map[string]interface{}{
		msg("system", "you are an agent"),
		msg("user", "do the thing"),
		msg("assistant", ""), // a tool-call turn: no text to mark
		msg("tool", "the tool said something"),
	}
	tools := []map[string]interface{}{
		{"type": "function", "function": map[string]interface{}{"name": "a"}},
		{"type": "function", "function": map[string]interface{}{"name": "b"}},
	}

	if n := markPromptCacheBreakpoints(messages, tools); n != 2 {
		t.Fatalf("marked %d breakpoints, want 2", n)
	}
	if cacheControlOn(tools[0]) {
		t.Error("only the last tool should carry the prefix breakpoint")
	}
	if !cacheControlOn(tools[1]) {
		t.Error("the last tool should carry the prefix breakpoint")
	}
	// With tools present the system message is covered by the tool mark and
	// must stay a plain string, exactly as an uncached request would send it.
	if _, stillPlain := messages[0]["content"].(string); !stillPlain {
		t.Error("the system message should be left alone when a tool carries the prefix")
	}
	if !cacheControlOn(messages[3]) {
		t.Error("the tail breakpoint should land on the last message with text")
	}
	if cacheControlOn(messages[2]) {
		t.Error("a tool-call turn with no text cannot carry a breakpoint")
	}
}

func TestMarkPromptCacheBreakpointsFallsBackToSystemWithoutTools(t *testing.T) {
	t.Parallel()
	messages := []map[string]interface{}{
		msg("system", "you are an agent"),
		msg("system", "and here are your guides"),
		msg("user", "do the thing"),
	}

	if n := markPromptCacheBreakpoints(messages, nil); n != 2 {
		t.Fatalf("marked %d breakpoints, want 2", n)
	}
	if cacheControlOn(messages[0]) {
		t.Error("only the last of the leading system messages carries the prefix")
	}
	if !cacheControlOn(messages[1]) {
		t.Error("the last leading system message should carry the prefix")
	}
	if !cacheControlOn(messages[2]) {
		t.Error("the tail breakpoint should land on the last message")
	}
}

// The prefix and the tail must never be the same mark counted twice.
func TestMarkPromptCacheBreakpointsSingleSystemMessage(t *testing.T) {
	t.Parallel()
	messages := []map[string]interface{}{msg("system", "you are an agent")}
	if n := markPromptCacheBreakpoints(messages, nil); n != 1 {
		t.Fatalf("marked %d breakpoints, want 1", n)
	}
	if !cacheControlOn(messages[0]) {
		t.Error("the only message should carry the prefix breakpoint")
	}
}

func TestMarkPromptCacheBreakpointsNeverExceedsTheCeiling(t *testing.T) {
	t.Parallel()
	messages := make([]map[string]interface{}, 0, 40)
	for i := 0; i < 40; i++ {
		messages = append(messages, msg("user", "chatter"))
	}
	tools := []map[string]interface{}{{"type": "function"}}
	if n := markPromptCacheBreakpoints(messages, tools); n > maxPromptCacheBreakpoints {
		t.Fatalf("marked %d breakpoints, want at most %d", n, maxPromptCacheBreakpoints)
	}
}

// A request with caching off must be byte-identical to what the client sent
// before this feature existed. That is the whole safety argument for adding
// markers to a shared request builder.
func TestBuildRequestUnchangedWhenPromptCacheIsOff(t *testing.T) {
	t.Parallel()
	messages := []domain.Message{
		{Role: "system", Content: "you are an agent"},
		{Role: "user", Content: "do the thing"},
	}
	tools := []domain.ToolDefinition{{Type: "function"}}

	off, err := json.Marshal(buildPoolGenerateWithToolsRequest("m", messages, tools,
		&domain.GenerationOptions{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	explicit, err := json.Marshal(buildPoolGenerateWithToolsRequest("m", messages, tools,
		&domain.GenerationOptions{PromptCache: domain.PromptCacheExplicit}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(off) == string(explicit) {
		t.Fatal("explicit mode should have changed the request")
	}
	if want := `"cache_control"`; !strings.Contains(string(explicit), want) {
		t.Errorf("explicit request should carry %s, got: %s", want, explicit)
	}
	if strings.Contains(string(off), "cache_control") {
		t.Errorf("off request must carry no markers, got: %s", off)
	}
}

func TestShouldRetryPoolWithoutPromptCache(t *testing.T) {
	t.Parallel()
	explicit := &domain.GenerationOptions{PromptCache: domain.PromptCacheExplicit}
	off := &domain.GenerationOptions{}

	cases := []struct {
		name string
		opts *domain.GenerationOptions
		err  error
		want bool
	}{
		{"off is never retried", off, errors.New("unknown field cache_control"), false},
		{"no error is never retried", explicit, nil, false},
		{"an unrelated failure is not ours", explicit, errors.New("502 bad gateway"), false},
		{"a rejected marker is", explicit, errors.New("unsupported parameter: cache_control"), true},
		{"a rejected block shape is too", explicit,
			errors.New("invalid type for 'messages[0].content': expected string"), true},
		{"a rejected tool shape is too", explicit,
			errors.New("unknown field 'cache_control' in tools[0]"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRetryPoolWithoutPromptCache(tc.opts, tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The fallback must actually clear the mode, or the retry sends the same
// rejected request a second time.
func TestApplyPoolRetryFallbacksStripsPromptCache(t *testing.T) {
	t.Parallel()
	opts := &domain.GenerationOptions{PromptCache: domain.PromptCacheExplicit}
	got := applyPoolRetryFallbacks(opts, errors.New("unsupported parameter: cache_control"))
	if got == nil {
		t.Fatal("expected a retry with the marker stripped")
	}
	if got.PromptCache != domain.PromptCacheOff {
		t.Errorf("PromptCache = %q, want off", got.PromptCache)
	}
	if opts.PromptCache != domain.PromptCacheExplicit {
		t.Error("the caller's options must not be mutated")
	}
}
