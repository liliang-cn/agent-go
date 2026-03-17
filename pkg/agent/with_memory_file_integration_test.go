package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/pkg/config"
	"github.com/liliang-cn/agent-go/pkg/domain"
)

type fileMemoryTestLLM struct {
	navigatorID        string
	sawMemoryContext   bool
	forceStoredMemory  bool
	storedMemoryText   string
	expectedRecallText string
}

func (f *fileMemoryTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (f *fileMemoryTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (f *fileMemoryTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	var userContent string
	if len(messages) > 0 {
		userContent = messages[len(messages)-1].Content
	}
	allContent := collectMessageContent(messages)

	if strings.Contains(allContent, "Relevant Context From Memory") && strings.Contains(allContent, "Alice likes tea") {
		f.sawMemoryContext = true
		return &domain.GenerationResult{Content: "You like tea."}, nil
	}

	if f.expectedRecallText != "" && strings.Contains(allContent, "Relevant Context From Memory") && strings.Contains(allContent, f.expectedRecallText) {
		f.sawMemoryContext = true
		return &domain.GenerationResult{Content: "I remember that detail."}, nil
	}

	if strings.Contains(strings.ToLower(userContent), "remember: alice likes tea") {
		return &domain.GenerationResult{Content: "I'll remember that."}, nil
	}

	if strings.Contains(strings.ToLower(userContent), "alice prefers coffee over tea") {
		return &domain.GenerationResult{Content: "Understood."}, nil
	}

	return &domain.GenerationResult{Content: "OK"}, nil
}

func (f *fileMemoryTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (f *fileMemoryTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	switch {
	case schemaHasProperty(schema, "intent_type"):
		return structuredJSON(map[string]interface{}{
			"intent_type": "general_qa",
			"confidence":  0.9,
		}), nil
	case schemaHasProperty(schema, "should_store"):
		if f.forceStoredMemory {
			return structuredJSON(map[string]interface{}{
				"should_store": true,
				"memories": []map[string]interface{}{
					{
						"type":       "preference",
						"content":    f.storedMemoryText,
						"importance": 0.9,
					},
				},
			}), nil
		}
		return structuredJSON(map[string]interface{}{
			"should_store": false,
			"memories":     []map[string]interface{}{},
		}), nil
	case schemaHasProperty(schema, "ids"):
		if f.navigatorID == "" {
			re := regexp.MustCompile(`\[(.*?)\]`)
			if match := re.FindStringSubmatch(prompt); len(match) == 2 {
				f.navigatorID = match[1]
			}
		}
		return structuredJSON(map[string]interface{}{
			"ids":       []string{f.navigatorID},
			"reasoning": "Selected the stored preference.",
		}), nil
	default:
		return structuredJSON(map[string]interface{}{}), nil
	}
}

func (f *fileMemoryTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func structuredJSON(data interface{}) *domain.StructuredResult {
	raw, _ := json.Marshal(data)
	return &domain.StructuredResult{
		Raw:   string(raw),
		Valid: true,
		Data:  data,
	}
}

func schemaHasProperty(schema interface{}, key string) bool {
	root, ok := schema.(map[string]interface{})
	if !ok {
		return false
	}
	properties, ok := root["properties"].(map[string]interface{})
	if !ok {
		return false
	}
	_, exists := properties[key]
	return exists
}

func collectMessageContent(messages []domain.Message) string {
	var sb strings.Builder
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		sb.WriteString(message.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

func testAgentConfig(home string) *config.Config {
	cfg := &config.Config{
		Home: home,
		LLM: config.LLMConfig{
			Enabled: false,
		},
		RAG: config.RAGConfig{
			Enabled: false,
		},
		Memory: config.MemoryConfig{
			StoreType:  "file",
			MemoryPath: filepath.Join(home, "data", "memories"),
		},
	}
	cfg.ApplyHomeLayout()
	return cfg
}

func TestAgentWithMemoryStoresAndRecallsFileMemory(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &fileMemoryTestLLM{}

	svc, err := New("memory-agent").
		WithConfig(testAgentConfig(home)).
		WithLLM(llm).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	first, err := svc.Chat(ctx, "remember: Alice likes tea")
	if err != nil {
		t.Fatalf("first chat failed: %v", err)
	}
	if got := first.Text(); got != "I'll remember that." {
		t.Fatalf("unexpected first response: %q", got)
	}

	mems, total, err := svc.MemoryService().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list memories failed: %v", err)
	}
	if total == 0 || len(mems) == 0 {
		t.Fatal("expected stored memory after remember command")
	}
	if !strings.Contains(mems[0].Content, "Alice likes tea") {
		t.Fatalf("unexpected stored memory: %+v", mems[0])
	}
	if mems[0].ScopeType != domain.MemoryScopeAgent || mems[0].ScopeID != "memory-agent" {
		t.Fatalf("expected remembered preference to be stored in agent scope, got %+v", mems[0])
	}

	entityFiles, err := filepath.Glob(filepath.Join(home, "data", "memories", "entities", "*.md"))
	if err != nil {
		t.Fatalf("glob entity files failed: %v", err)
	}
	if len(entityFiles) == 0 {
		t.Fatal("expected file memory markdown file on disk")
	}
	data, err := os.ReadFile(entityFiles[0])
	if err != nil {
		t.Fatalf("read stored memory file failed: %v", err)
	}
	if !strings.Contains(string(data), "Alice likes tea") {
		t.Fatalf("stored file did not contain remembered content: %s", string(data))
	}

	second, err := svc.Chat(ctx, "what do I like to drink?")
	if err != nil {
		t.Fatalf("second chat failed: %v", err)
	}
	if got := second.Text(); got != "You like tea." {
		t.Fatalf("unexpected second response: %q", got)
	}
	if !llm.sawMemoryContext {
		t.Fatal("expected second turn to include memory context in LLM input")
	}
}

func TestAgentWithMemoryRecallsAfterServiceRestart(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	sessionID := "session-restart"

	writerLLM := &fileMemoryTestLLM{}
	writer, err := New("memory-agent").
		WithConfig(testAgentConfig(home)).
		WithLLM(writerLLM).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build writer failed: %v", err)
	}

	writer.SetSessionID(sessionID)
	if _, err := writer.Chat(ctx, "remember: Alice likes tea"); err != nil {
		t.Fatalf("writer chat failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer close failed: %v", err)
	}

	readerLLM := &fileMemoryTestLLM{}
	reader, err := New("memory-agent").
		WithConfig(testAgentConfig(home)).
		WithLLM(readerLLM).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build reader failed: %v", err)
	}
	defer reader.Close()

	reader.SetSessionID(sessionID)
	result, err := reader.Chat(ctx, "what do I like to drink?")
	if err != nil {
		t.Fatalf("reader chat failed: %v", err)
	}
	if got := result.Text(); got != "You like tea." {
		t.Fatalf("unexpected restart recall response: %q", got)
	}
	if !readerLLM.sawMemoryContext {
		t.Fatal("expected restarted service to inject memory context")
	}
}

func TestAgentWithMemoryStoresOrdinaryDialogueViaStoreIfWorthwhile(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &fileMemoryTestLLM{
		forceStoredMemory:  true,
		storedMemoryText:   "Alice prefers coffee over tea.",
		expectedRecallText: "Alice prefers coffee over tea.",
	}

	svc, err := New("memory-agent").
		WithConfig(testAgentConfig(home)).
		WithLLM(llm).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	first, err := svc.Chat(ctx, "Alice prefers coffee over tea.")
	if err != nil {
		t.Fatalf("ordinary dialogue chat failed: %v", err)
	}
	if got := first.Text(); got != "Understood." {
		t.Fatalf("unexpected first response: %q", got)
	}

	mems, total, err := svc.MemoryService().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list memories failed: %v", err)
	}
	if total == 0 || len(mems) == 0 {
		t.Fatal("expected StoreIfWorthwhile to persist extracted memory")
	}

	found := false
	for _, mem := range mems {
		if strings.Contains(mem.Content, "Alice prefers coffee over tea.") {
			if mem.ScopeType != domain.MemoryScopeAgent || mem.ScopeID != "memory-agent" {
				t.Fatalf("expected extracted preference to be stored in agent scope, got %+v", mem)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected extracted memory in store, got %+v", mems)
	}

	second, err := svc.Chat(ctx, "what drink does Alice prefer?")
	if err != nil {
		t.Fatalf("recall chat failed: %v", err)
	}
	if got := second.Text(); got != "I remember that detail." {
		t.Fatalf("unexpected recall response: %q", got)
	}
	if !llm.sawMemoryContext {
		t.Fatal("expected recalled ordinary-dialogue memory to be injected")
	}
}

func TestAgentWithMemoryStoresOrdinaryDialogueViaHeuristicFallback(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &fileMemoryTestLLM{
		expectedRecallText: "Alice prefers coffee over tea.",
	}

	svc, err := New("memory-agent").
		WithConfig(testAgentConfig(home)).
		WithLLM(llm).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	first, err := svc.Chat(ctx, "Alice prefers coffee over tea.")
	if err != nil {
		t.Fatalf("ordinary dialogue chat failed: %v", err)
	}
	if got := first.Text(); got != "Understood." {
		t.Fatalf("unexpected first response: %q", got)
	}

	mems, total, err := svc.MemoryService().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list memories failed: %v", err)
	}
	if total == 0 || len(mems) == 0 {
		t.Fatal("expected heuristic fallback to persist extracted memory")
	}

	found := false
	for _, mem := range mems {
		if strings.Contains(mem.Content, "Alice prefers coffee over tea.") {
			if mem.ScopeType != domain.MemoryScopeAgent || mem.ScopeID != "memory-agent" {
				t.Fatalf("expected heuristic preference to be stored in agent scope, got %+v", mem)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected heuristic memory in store, got %+v", mems)
	}

	second, err := svc.Chat(ctx, "what drink does Alice prefer?")
	if err != nil {
		t.Fatalf("recall chat failed: %v", err)
	}
	if got := second.Text(); got != "I remember that detail." {
		t.Fatalf("unexpected recall response: %q", got)
	}
	if !llm.sawMemoryContext {
		t.Fatal("expected heuristic fallback memory to be injected")
	}
}

func TestMemoryToolsAreHiddenInFileOnlyMode(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	svc, err := New("memory-agent").
		WithConfig(testAgentConfig(home)).
		WithLLM(&fileMemoryTestLLM{}).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	if svc.toolRegistry.Has("memory_recall") || svc.toolRegistry.Has("memory_save") || svc.toolRegistry.Has("memory_update") || svc.toolRegistry.Has("memory_delete") {
		t.Fatal("expected memory CRUD tools to be hidden in file-only mode")
	}

	if _, err := svc.Chat(ctx, "remember: Alice likes tea"); err != nil {
		t.Fatalf("writer chat failed: %v", err)
	}

	result, err := svc.Chat(ctx, "what do I like to drink?")
	if err != nil {
		t.Fatalf("recall chat failed: %v", err)
	}
	if got := result.Text(); got != "You like tea." {
		t.Fatalf("expected file-only mode to still recall memory via pre-injected context, got %q", got)
	}
}
