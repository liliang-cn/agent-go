package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
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

type explicitRecallTestLLM struct {
	generateCalls          int
	generateWithToolsCalls int
	lastGeneratePrompt     string
}

func (e *explicitRecallTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	e.generateCalls++
	e.lastGeneratePrompt = prompt
	if strings.Contains(prompt, "The vector memory test token is mango-9135.") {
		return "mango-9135", nil
	}
	if strings.Contains(prompt, "Dashboard") {
		return "明天上午处理 Dashboard 相关工作。", nil
	}
	if strings.Contains(prompt, "用户明天17:00去万达广场吃饭。") {
		return "明天下午17:00去万达广场吃饭。", nil
	}
	return "I couldn't find that in memory.", nil
}

func (e *explicitRecallTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (e *explicitRecallTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	e.generateWithToolsCalls++
	return &domain.GenerationResult{Content: "tool-path"}, nil
}

func (e *explicitRecallTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (e *explicitRecallTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return structuredJSON(map[string]interface{}{
		"intent_type": "general_qa",
		"confidence":  0.95,
	}), nil
}

func (e *explicitRecallTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentQuestion, Confidence: 0.95}, nil
}

type memoryToolCallingTestLLM struct {
	generateWithToolsCalls int
	sawMemoryPrompt        bool
	sawMemorySaveTool      bool
}

func (m *memoryToolCallingTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (m *memoryToolCallingTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (m *memoryToolCallingTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	m.generateWithToolsCalls++
	if len(messages) > 0 {
		systemPrompt := messages[0].Content
		if strings.Contains(systemPrompt, "Memory tool usage:") && strings.Contains(systemPrompt, "`memory_save`") {
			m.sawMemoryPrompt = true
		}
	}
	for _, tool := range tools {
		if tool.Function.Name == "memory_save" {
			m.sawMemorySaveTool = true
			break
		}
	}

	if m.generateWithToolsCalls == 1 {
		if strings.Contains(userContent(messages), "明天下午3：00开启动会") {
			return &domain.GenerationResult{
				ToolCalls: []domain.ToolCall{{
					ID:   "memory-save-implicit-schedule",
					Type: "function",
					Function: domain.FunctionCall{
						Name: "memory_save",
						Arguments: map[string]interface{}{
							"content": "明天下午3：00开启动会。",
							"type":    "fact",
						},
					},
				}},
			}, nil
		}
		if strings.Contains(userContent(messages), "万达") {
			return &domain.GenerationResult{
				ToolCalls: []domain.ToolCall{{
					ID:   "memory-save-schedule-wanda",
					Type: "function",
					Function: domain.FunctionCall{
						Name: "memory_save",
						Arguments: map[string]interface{}{
							"content": "用户明天17:00去万达广场吃饭。",
							"type":    "context",
						},
					},
				}},
			}, nil
		}
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID:   "memory-save-1",
				Type: "function",
				Function: domain.FunctionCall{
					Name: "memory_save",
					Arguments: map[string]interface{}{
						"content": "My secret code is abc-123.",
						"type":    "fact",
					},
				},
			}},
		}, nil
	}

	return &domain.GenerationResult{Content: "I'll remember that."}, nil
}

func (m *memoryToolCallingTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (m *memoryToolCallingTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	switch {
	case schemaHasProperty(schema, "intent_type"):
		return structuredJSON(map[string]interface{}{
			"intent_type": "memory_save",
			"confidence":  0.95,
		}), nil
	case schemaHasProperty(schema, "should_store"):
		return structuredJSON(map[string]interface{}{
			"should_store": false,
			"memories":     []map[string]interface{}{},
		}), nil
	default:
		return structuredJSON(map[string]interface{}{}), nil
	}
}

func (m *memoryToolCallingTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.95}, nil
}

func userContent(messages []domain.Message) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[len(messages)-1].Content
}

type vectorMemoryTestEmbedder struct{}

func (vectorMemoryTestEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "secret"), strings.Contains(lower, "code"), strings.Contains(lower, "abc-123"):
		return []float64{1, 0, 0}, nil
	default:
		return []float64{0, 1, 0}, nil
	}
}

func (e vectorMemoryTestEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	results := make([][]float64, 0, len(texts))
	for _, text := range texts {
		vector, err := e.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		results = append(results, vector)
	}
	return results, nil
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

func testCortexAgentConfig(home string) *config.Config {
	cfg := testAgentConfig(home)
	cfg.Memory.StoreType = "cortex"
	return cfg
}

func TestAgentWithMemoryStoresAndRecallsFileMemory(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &fileMemoryTestLLM{}

	svc, err := New("memory-agent").
		WithPTC(false).
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

func TestAgentUsesMemorySaveToolWhenPromptSignalsDurableMemory(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &memoryToolCallingTestLLM{}

	svc, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testCortexAgentConfig(home)).
		WithLLM(llm).
		WithEmbedder(vectorMemoryTestEmbedder{}).
		WithMemory(WithMemoryStoreType("cortex")).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	result, err := svc.Chat(ctx, "Please remember that my secret code is abc-123.")
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if got := result.Text(); got != "I'll remember that." {
		t.Fatalf("unexpected response: %q", got)
	}
	if !llm.sawMemoryPrompt {
		t.Fatal("expected system prompt to include memory tool guidance")
	}
	if !llm.sawMemorySaveTool {
		t.Fatal("expected memory_save to be exposed to the LLM")
	}
	if llm.generateWithToolsCalls < 2 {
		t.Fatalf("expected multiple tool-calling rounds, got %d", llm.generateWithToolsCalls)
	}

	mems, total, err := svc.MemoryService().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list memories failed: %v", err)
	}
	if total == 0 || len(mems) == 0 {
		t.Fatal("expected memory_save tool call to persist memory")
	}

	found := false
	for _, mem := range mems {
		if strings.Contains(mem.Content, "My secret code is abc-123.") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected saved memory content, got %+v", mems)
	}
}

func TestAgentUsesMemorySaveToolForImplicitScheduleStatement(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &memoryToolCallingTestLLM{}

	svc, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testCortexAgentConfig(home)).
		WithLLM(llm).
		WithEmbedder(vectorMemoryTestEmbedder{}).
		WithMemory(WithMemoryStoreType("cortex")).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	result, err := svc.Chat(ctx, "明天下午3：00开启动会。")
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if got := result.Text(); got != "I'll remember that." {
		t.Fatalf("unexpected response: %q", got)
	}
	if !llm.sawMemoryPrompt || !llm.sawMemorySaveTool {
		t.Fatal("expected implicit schedule statement to have memory-save guidance and tool access")
	}

	mems, total, err := svc.MemoryService().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list memories failed: %v", err)
	}
	if total == 0 || len(mems) == 0 {
		t.Fatal("expected implicit schedule statement to persist memory")
	}

	found := false
	for _, mem := range mems {
		if strings.Contains(mem.Content, "明天下午3：00开启动会") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected saved implicit schedule memory, got %+v", mems)
	}
}

func TestMemoryToolsUseInheritedScopeForBuiltInArchivist(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	svc, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testCortexAgentConfig(home)).
		WithLLM(&memoryToolCallingTestLLM{}).
		WithEmbedder(vectorMemoryTestEmbedder{}).
		WithMemory(WithMemoryStoreType("cortex")).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	session := NewSession("session-archivist-scope")
	session.SetContext(sessionContextMemoryAgentScope, "Concierge")
	session.SetContext(sessionContextMemoryTeamScope, "default-team")

	toolCtx := withCurrentSession(ctx, session)
	toolCtx = withCurrentAgent(toolCtx, NewAgent("Archivist"))

	if _, err := svc.toolRegistry.Call(toolCtx, "memory_save", map[string]interface{}{
		"content": "用户明天17:00去万达广场吃饭。",
		"type":    "context",
	}); err != nil {
		t.Fatalf("memory_save failed: %v", err)
	}

	mems, total, err := svc.MemoryService().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list memories failed: %v", err)
	}
	if total == 0 || len(mems) == 0 {
		t.Fatal("expected memory_save to persist memory")
	}

	found := false
	for _, mem := range mems {
		if strings.Contains(mem.Content, "万达广场吃饭") {
			found = true
			if mem.ScopeType != domain.MemoryScopeAgent || mem.ScopeID != "Concierge" {
				t.Fatalf("expected built-in Archivist save to inherit Concierge scope, got %+v", mem)
			}
		}
	}
	if !found {
		t.Fatalf("expected saved schedule memory, got %+v", mems)
	}

	rawRecall, err := svc.toolRegistry.Call(toolCtx, "memory_recall", map[string]interface{}{
		"query": "明天有什么安排",
	})
	if err != nil {
		t.Fatalf("memory_recall failed: %v", err)
	}

	recall, ok := rawRecall.(map[string]interface{})
	if !ok {
		t.Fatalf("memory_recall returned %T, want map[string]interface{}", rawRecall)
	}
	if count, ok := recall["count"].(int); !ok || count < 1 {
		t.Fatalf("expected scoped recall hit count, got %#v", recall["count"])
	}
	memories, _ := recall["memories"].(string)
	if !strings.Contains(memories, "万达广场吃饭") {
		t.Fatalf("expected recalled memories to mention stored schedule, got %q", memories)
	}
}

func TestNormalizeFileMemoryPathRewritesVectorDBPathForFileStore(t *testing.T) {
	home := t.TempDir()
	cfg := testAgentConfig(home)

	got := normalizeFileMemoryPath(config.MemoryStoreTypeFile, cfg.MemoryVectorDBPath(), cfg)
	want := filepath.Join(home, "data", "memories")
	if got != want {
		t.Fatalf("normalizeFileMemoryPath() = %s, want %s", got, want)
	}
}

func TestAgentExplicitMemoryRecallUsesShortcutAnswer(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &explicitRecallTestLLM{}

	svc, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(home)).
		WithLLM(llm).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	if err := svc.MemoryService().Add(ctx, &domain.Memory{
		ID:         "memory-token-1",
		SessionID:  "agent:memory-agent",
		ScopeType:  domain.MemoryScopeAgent,
		ScopeID:    "memory-agent",
		Type:       domain.MemoryTypePreference,
		Content:    "The vector memory test token is mango-9135.",
		Importance: 0.9,
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("add memory failed: %v", err)
	}

	result, err := svc.Chat(ctx, "What is the vector memory test token I asked you to remember? Reply with only the token.")
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}

	if got := strings.TrimSpace(result.Text()); got != "mango-9135" {
		t.Fatalf("expected explicit recall shortcut answer, got %q", got)
	}
	if llm.generateCalls == 0 {
		t.Fatal("expected shortcut to call Generate")
	}
	if llm.generateWithToolsCalls != 0 {
		t.Fatalf("expected shortcut to avoid GenerateWithTools, got %d calls", llm.generateWithToolsCalls)
	}
	if !strings.Contains(llm.lastGeneratePrompt, "The vector memory test token is mango-9135.") {
		t.Fatalf("expected recall prompt to contain memory context, got %q", llm.lastGeneratePrompt)
	}
}

func TestAgentScheduleRecallUsesShortcutAnswer(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &explicitRecallTestLLM{}

	svc, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(home)).
		WithLLM(llm).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	if err := svc.MemoryService().Add(ctx, &domain.Memory{
		ID:         "memory-schedule-1",
		SessionID:  "agent:memory-agent",
		ScopeType:  domain.MemoryScopeAgent,
		ScopeID:    "memory-agent",
		Type:       domain.MemoryTypeContext,
		Content:    "用户明天17:00去万达广场吃饭。",
		Importance: 0.9,
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("add memory failed: %v", err)
	}

	result, err := svc.Chat(ctx, "明天有什么安排？")
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}

	if got := strings.TrimSpace(result.Text()); got != "明天下午17:00去万达广场吃饭。" {
		t.Fatalf("expected schedule recall shortcut answer, got %q", got)
	}
	if llm.generateCalls == 0 {
		t.Fatal("expected shortcut to call Generate")
	}
	if llm.generateWithToolsCalls != 0 {
		t.Fatalf("expected shortcut to avoid GenerateWithTools, got %d calls", llm.generateWithToolsCalls)
	}
	if !strings.Contains(llm.lastGeneratePrompt, "用户明天17:00去万达广场吃饭。") {
		t.Fatalf("expected recall prompt to contain schedule memory context, got %q", llm.lastGeneratePrompt)
	}
}

func TestAgentPersonalScheduleRecallExcludesIndirectFamilyEvents(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	llm := &explicitRecallTestLLM{}

	svc, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testCortexAgentConfig(home)).
		WithLLM(llm).
		WithMemory(WithMemoryStoreType("cortex")).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	for _, mem := range []*domain.Memory{
		{
			ID:         "memory-dashboard",
			SessionID:  "agent:memory-agent",
			ScopeType:  domain.MemoryScopeAgent,
			ScopeID:    "memory-agent",
			Type:       domain.MemoryTypeContext,
			Content:    "明天早上要处理一下Dashboard的事情。",
			Importance: 0.9,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "memory-sanbao-trip",
			SessionID:  "agent:memory-agent",
			ScopeType:  domain.MemoryScopeAgent,
			ScopeID:    "memory-agent",
			Type:       domain.MemoryTypeFact,
			Content:    "周二三宝要去春游，然后就放假了。",
			Importance: 0.8,
			CreatedAt:  time.Now(),
		},
	} {
		if err := svc.MemoryService().Add(ctx, mem); err != nil {
			t.Fatalf("add memory failed: %v", err)
		}
	}

	result, err := svc.Chat(ctx, "我这周有什么安排？")
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if got := strings.TrimSpace(result.Text()); got != "明天上午处理 Dashboard 相关工作。" {
		t.Fatalf("expected personal schedule answer, got %q", got)
	}
	if !strings.Contains(llm.lastGeneratePrompt, "Dashboard") {
		t.Fatalf("expected recall prompt to keep dashboard memory, got %q", llm.lastGeneratePrompt)
	}
	if strings.Contains(llm.lastGeneratePrompt, "三宝要去春游") {
		t.Fatalf("expected personal schedule prompt to exclude indirect family event, got %q", llm.lastGeneratePrompt)
	}
}

func TestAgentPersonalScheduleRecallAfterCorrectionPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	writer, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testCortexAgentConfig(home)).
		WithLLM(&fileMemoryTestLLM{}).
		WithMemory(WithMemoryStoreType("cortex")).
		Build()
	if err != nil {
		t.Fatalf("build writer failed: %v", err)
	}

	for _, mem := range []*domain.Memory{
		{
			ID:         "restart-dashboard",
			SessionID:  "agent:memory-agent",
			ScopeType:  domain.MemoryScopeAgent,
			ScopeID:    "memory-agent",
			Type:       domain.MemoryTypeContext,
			Content:    "明天早上要处理一下Dashboard的事情。",
			Importance: 0.9,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "restart-trip",
			SessionID:  "agent:memory-agent",
			ScopeType:  domain.MemoryScopeAgent,
			ScopeID:    "memory-agent",
			Type:       domain.MemoryTypeFact,
			Content:    "周二三宝要去春游，然后就放假了。",
			Importance: 0.8,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "restart-trip-correction",
			SessionID:  "agent:memory-agent",
			ScopeType:  domain.MemoryScopeAgent,
			ScopeID:    "memory-agent",
			Type:       domain.MemoryTypeFact,
			Content:    "三宝是跟着学校去春游，不用我。",
			Importance: 0.85,
			CreatedAt:  time.Now(),
		},
	} {
		if err := writer.MemoryService().Add(ctx, mem); err != nil {
			t.Fatalf("writer add memory failed: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer close failed: %v", err)
	}

	readerLLM := &explicitRecallTestLLM{}
	reader, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testCortexAgentConfig(home)).
		WithLLM(readerLLM).
		WithMemory(WithMemoryStoreType("cortex")).
		Build()
	if err != nil {
		t.Fatalf("build reader failed: %v", err)
	}
	defer reader.Close()

	result, err := reader.Chat(ctx, "我这周有什么安排？")
	if err != nil {
		t.Fatalf("reader chat failed: %v", err)
	}
	if got := strings.TrimSpace(result.Text()); got != "明天上午处理 Dashboard 相关工作。" {
		t.Fatalf("expected restart personal schedule answer, got %q", got)
	}
	if strings.Contains(readerLLM.lastGeneratePrompt, "三宝要去春游") || strings.Contains(readerLLM.lastGeneratePrompt, "不用我") {
		t.Fatalf("expected restart recall prompt to exclude corrected indirect family event, got %q", readerLLM.lastGeneratePrompt)
	}
}

func TestAgentWithMemoryRecallsAfterServiceRestart(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	sessionID := "session-restart"

	writerLLM := &fileMemoryTestLLM{}
	writer, err := New("memory-agent").
		WithPTC(false).
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
		WithPTC(false).
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
		WithPTC(false).
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
		WithPTC(false).
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

func TestMemoryToolsAreExposedInFileOnlyMode(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	svc, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(home)).
		WithLLM(&fileMemoryTestLLM{}).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	if !svc.toolRegistry.Has("memory_save") {
		t.Fatal("expected memory_save tool to be registered in file-only mode")
	}

	if _, err := svc.Chat(ctx, "remember: Alice likes tea"); err != nil {
		t.Fatalf("writer chat failed: %v", err)
	}

	result, err := svc.Chat(ctx, "what do I like to drink?")
	if err != nil {
		t.Fatalf("recall chat failed: %v", err)
	}
	if got := result.Text(); got != "You like tea." {
		t.Fatalf("expected file-only mode to recall memory, got %q", got)
	}
}
