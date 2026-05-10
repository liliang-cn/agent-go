# Testing An AgentGo-Powered App

Write fast unit tests against a stub `domain.Generator`; reach for the
behavioral eval harness when you want to test agent behavior end to
end, deterministically, without spending real LLM tokens in CI.

## Stubbing the LLM

`domain.Generator` is the interface every provider implements; use a
stub in unit tests:

```go
type fakeLLM struct{ reply string; calls int }

func (f *fakeLLM) Generate(ctx context.Context, p string, o *domain.GenerationOptions) (string, error) {
    f.calls++
    return f.reply, nil
}
func (f *fakeLLM) Stream(ctx context.Context, p string, o *domain.GenerationOptions, cb func(string)) error {
    return nil
}
func (f *fakeLLM) GenerateWithTools(ctx context.Context, msgs []domain.Message, tools []domain.ToolDefinition, o *domain.GenerationOptions) (*domain.GenerationResult, error) {
    f.calls++
    return &domain.GenerationResult{Content: f.reply}, nil
}
func (f *fakeLLM) StreamWithTools(ctx context.Context, msgs []domain.Message, tools []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
    f.calls++
    return cb(&domain.GenerationResult{Content: f.reply})
}
func (f *fakeLLM) GenerateStructured(ctx context.Context, p string, schema interface{}, o *domain.GenerationOptions) (*domain.StructuredResult, error) {
    return &domain.StructuredResult{Data: map[string]interface{}{}, Raw: "{}", Valid: true}, nil
}
func (f *fakeLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
    return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}
```

`eval/runner/mock_llm.go` already implements this — reuse it in your
own tests if you want a scripted-replies LLM.

## Isolated home directory per test

```go
home := t.TempDir()
cfg := &config.Config{
    Home: home,
    RAG:  config.RAGConfig{Enabled: false},
    Memory: config.MemoryConfig{
        StoreType:  "file",
        MemoryPath: filepath.Join(home, "data", "memories"),
    },
}
cfg.ApplyHomeLayout()

svc, err := agent.New("under-test").
    WithPTC(false).
    WithConfig(cfg).
    WithLLM(&fakeLLM{reply: "hi"}).
    Build()
require.NoError(t, err)
defer svc.Close()
```

`t.TempDir()` guarantees the SQLite DB and file-memory paths are
unique per test — concurrent tests don't collide.

## Behavioral eval as integration tests

Drop a YAML scenario into `eval/scenarios/` and `make eval` runs it:

```yaml
# eval/scenarios/myapp_dispatch_routes_to_responder.yaml
name: myapp_dispatch_routes_to_responder
description: when user asks a factual question, dispatcher routes to Responder.
agent: Dispatcher
llm_replies:
  - "The capital of France is Paris."
input: "what is the capital of France?"
expect:
  status: completed
  final_text_match: "Paris"
  llm_calls: 1
```

Run via library:

```go
import evalrunner "github.com/liliang-cn/agent-go/v2/eval/runner"

func TestEvalSuite(t *testing.T) {
    results, err := evalrunner.RunAll(context.Background(), "eval/scenarios", evalrunner.RunOptions{})
    require.NoError(t, err)
    for _, r := range results {
        if !r.Pass {
            t.Errorf("scenario %s failed: %v", r.Scenario, r.Reasons)
        }
    }
}
```

## Race tests for concurrency-sensitive code

```bash
go test -race ./internal/yourpkg -count=1
```

Always race-test code that writes to TeamManager-managed state
(custom tools that mutate shared maps, observers, etc.).

## Common smoke commands

```bash
# Pkg unit tests
go test ./...

# Behavioral eval (deterministic, mock-LLM)
make eval

# Behavioral eval against the real configured provider
make eval-live

# Try the binary against your config
go run ./cmd/yourcli ...
```

## What to assert

For agent runs, prefer asserting on **shape, not exact text**:

- The expected tool was called (look at `evt.Type == EventTypeToolCall` events).
- The final response status (`completed` / `blocked` / `error`).
- Final text matches a regex pattern (`re:^Found \\d+ items`).
- Lint X did or didn't fire (count via `EventTypeError` content).
- Persisted state can be read from a fresh `TeamManager` (process-restart correctness).
- Background tasks reach a terminal state.

Avoid asserting on the model's exact wording — it changes per
provider, per temperature, per retry.
