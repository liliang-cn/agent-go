// Package main demonstrates the PII redaction guardrail layer.
//
// It is fully offline and deterministic: the mock LLM simply ECHOES back the
// user-role text it was handed, so the program can print exactly what the
// model saw. That proves PII was stripped BEFORE it left the process — the
// key "don't leak to the cloud" property — while the local session keeps the
// original text intact.
//
//	go run ./examples/pii
//
// What it shows:
//   - agent.New(...).WithPIIRedaction() strips a fake 中国身份证 / 手机号 /
//     email / bank card before the LLM call (RedactPartial by default)
//   - the local session still holds the untouched original
//   - WithPIIMode(RedactBlock) refuses the run instead of forwarding PII
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// echoLLM returns, as its final answer, the concatenated user/assistant text it
// was asked to complete. Because the runtime applies input guardrails to a copy
// of the messages before calling the provider, whatever this mock sees is
// exactly what a real cloud provider would have seen.
type echoLLM struct{}

func (l *echoLLM) seen(msgs []domain.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		if t := strings.TrimSpace(m.Content); t != "" {
			b.WriteString(t)
			b.WriteString(" ")
		}
	}
	return "the model received: " + strings.TrimSpace(b.String())
}

func (l *echoLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *echoLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *echoLLM) GenerateWithTools(_ context.Context, msgs []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: l.seen(msgs)}, nil
}
func (l *echoLLM) StreamWithTools(_ context.Context, msgs []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(&domain.GenerationResult{Content: l.seen(msgs)})
}
func (l *echoLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *echoLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

func newConfig() *config.Config {
	home, err := os.MkdirTemp("", "pii-example-*")
	if err != nil {
		panic(err)
	}
	cfg := &config.Config{
		Home: home,
		RAG:  config.RAGConfig{Enabled: false},
		Memory: config.MemoryConfig{
			StoreType:  "file",
			MemoryPath: filepath.Join(home, "data", "memories"),
		},
	}
	cfg.ApplyHomeLayout()
	return cfg
}

func terminal(ch <-chan *agent.Event) *agent.Event {
	var last *agent.Event
	for ev := range ch {
		if ev.Type == agent.EventTypeComplete || ev.Type == agent.EventTypeBlocked {
			last = ev
		}
	}
	return last
}

func main() {
	// A prompt packed with fake PII: 中国身份证(18位) + 手机号 + email + 银行卡.
	prompt := "登记信息：身份证 110101199003074610，手机 13812345678，" +
		"邮箱 alice@example.com，银行卡 4111111111111111。请帮我核对。"

	fmt.Println("=== 1. RedactPartial (default): PII stripped before the LLM ===")
	fmt.Printf("original prompt (stays local):\n  %s\n\n", prompt)

	svc, err := agent.New("pii-demo").
		WithPTC(false).
		WithConfig(newConfig()).
		WithLLM(&echoLLM{}).
		WithPIIRedaction(). // input + output, RedactPartial, all kinds
		Build()
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	ch, err := svc.RunStream(context.Background(), prompt)
	if err != nil {
		panic(err)
	}
	final := terminal(ch)
	if final == nil {
		fmt.Println("no terminal event")
		os.Exit(1)
	}
	fmt.Printf("what the model actually saw (redacted before send):\n  %s\n\n", final.Content)

	// Prove the local session kept the ORIGINAL text.
	sess, err := svc.GetSession(svc.CurrentSessionID())
	if err != nil {
		panic(err)
	}
	localHasOriginal := false
	for _, m := range sess.GetMessages() {
		if strings.Contains(m.Content, "alice@example.com") &&
			strings.Contains(m.Content, "110101199003074610") {
			localHasOriginal = true
		}
	}
	fmt.Printf("local session still holds the original PII: %v\n", localHasOriginal)

	leaked := strings.Contains(final.Content, "alice@example.com") ||
		strings.Contains(final.Content, "13812345678") ||
		strings.Contains(final.Content, "110101199003074610") ||
		strings.Contains(final.Content, "4111111111111111")
	fmt.Printf("any raw PII leaked to the model: %v\n", leaked)
	if leaked {
		fmt.Println("FAIL: PII reached the model")
		os.Exit(1)
	}

	// --- 2. RedactBlock: refuse the run instead of forwarding PII ----------
	fmt.Println("\n=== 2. RedactBlock: refuse rather than forward PII ===")
	blockSvc, err := agent.New("pii-block").
		WithPTC(false).
		WithConfig(newConfig()).
		WithLLM(&echoLLM{}).
		WithPIIRedaction(agent.WithPIIMode(agent.RedactBlock)).
		Build()
	if err != nil {
		panic(err)
	}
	defer blockSvc.Close()

	bch, err := blockSvc.RunStream(context.Background(), prompt)
	if err != nil {
		panic(err)
	}
	bterm := terminal(bch)
	if bterm == nil {
		fmt.Println("no terminal event")
		os.Exit(1)
	}
	fmt.Printf("terminal event type: %s\n", bterm.Type)
	fmt.Printf("refusal reason: %s\n", strings.TrimSpace(bterm.Content))
	if bterm.Type != agent.EventTypeBlocked {
		fmt.Println("FAIL: expected the run to be blocked")
		os.Exit(1)
	}

	fmt.Println("\nOK: PII stripped before the LLM (partial), and RedactBlock refused the run.")
}
