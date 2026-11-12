package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// turnScriptLLM plays one prepared turn per model call and records the messages
// each call was given, so a test can say both what the model did and what the
// runtime told it in between.
type turnScriptLLM struct {
	mu          sync.Mutex
	turns       []*domain.GenerationResult
	calls       int
	seen        []string
	constraints string
}

func (s *turnScriptLLM) next(messages []domain.Message) *domain.GenerationResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	s.seen = append(s.seen, b.String())
	i := s.calls
	s.calls++
	if i >= len(s.turns) {
		i = len(s.turns) - 1
	}
	return s.turns[i]
}

func (s *turnScriptLLM) sawAt(call int, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return call < len(s.seen) && strings.Contains(s.seen[call], text)
}

func (s *turnScriptLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *turnScriptLLM) Generate(ctx context.Context, p string, o *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (s *turnScriptLLM) Stream(ctx context.Context, p string, o *domain.GenerationOptions, cb func(string)) error {
	return nil
}
func (s *turnScriptLLM) GenerateWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return s.next(m), nil
}
func (s *turnScriptLLM) StreamWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(s.next(m))
}
func (s *turnScriptLLM) GenerateStructured(ctx context.Context, p string, schema interface{}, o *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if s.constraints != "" && strings.Contains(p, "report ONLY the constraints") {
		return &domain.StructuredResult{Raw: s.constraints, Valid: true}, nil
	}
	return structuredJSON(map[string]interface{}{}), nil
}
func (s *turnScriptLLM) RecognizeIntent(ctx context.Context, r string) (*domain.IntentResult, error) {
	return nil, nil
}

func turnCalling(id, name string, args map[string]interface{}) *domain.GenerationResult {
	if args == nil {
		args = map[string]interface{}{}
	}
	return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
		ID: id, Type: "function",
		Function: domain.FunctionCall{Name: name, Arguments: args},
	}}}
}

func turnBlocked(id, blocker string) *domain.GenerationResult {
	return turnCalling(id, "task_blocked", map[string]interface{}{"blocker": blocker})
}

const untriedMarker = "this run has not tried anything"
const untriedSecondMarker = "stopping again without having run anything"

// The run that started this: asked for a deck, one model call, straight to
// task_blocked "no presentation tool", with a shell on offer that it never
// touched. The block is sent back with what to try, the model then does the
// work, and the run completes.
func TestBlockWithNothingTriedIsSentBack(t *testing.T) {
	t.Parallel()

	llm := &turnScriptLLM{turns: []*domain.GenerationResult{
		turnBlocked("b1", "There is no presentation tool available."),
		turnCalling("c1", "bash", map[string]interface{}{"command": "python3 make_deck.py"}),
		{Content: "The deck is written: 15 slides."},
	}}
	svc, err := New("untried-block").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	ran := 0
	svc.AddTool("bash", "Run a shell command", map[string]interface{}{"type": "object"},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			ran++
			return "ok", nil
		})

	res, err := svc.Run(context.Background(), "Make a 15-slide deck about energy storage.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Blocked {
		t.Fatalf("the run gave up with a shell it never used: %q", res.Text())
	}
	if !llm.sawAt(1, untriedMarker) {
		t.Error("the model was not told it had tried nothing")
	}
	if ran != 1 {
		t.Errorf("bash ran %d times, want 1", ran)
	}
}

// A model that will not move still gets to stop: it answers the push with the
// same block, and the block stands.
func TestBlockWithNothingTriedStandsAfterOnePush(t *testing.T) {
	t.Parallel()

	llm := &turnScriptLLM{turns: []*domain.GenerationResult{
		turnBlocked("b", "I cannot make presentations."),
	}}
	svc, err := New("untried-block-stands").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.AddTool("bash", "Run a shell command", map[string]interface{}{"type": "object"},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) { return "ok", nil })

	res, err := svc.Run(context.Background(), "Make a deck about energy storage.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Blocked {
		t.Fatalf("a model that blocks again must be allowed to stop, got %q", res.Text())
	}
	if got := llm.callCount(); got != 2 {
		t.Errorf("model was called %d times, want 2 (one block, one push)", got)
	}
	if !llm.sawAt(1, untriedMarker) {
		t.Error("the push did not arrive")
	}
	if !strings.Contains(res.Text(), "cannot make presentations") {
		t.Errorf("the model's own blocker was lost: %q", res.Text())
	}
}

// A run that worked on the task and then hit a wall has found something. Its
// block goes through untouched.
func TestBlockAfterARealAttemptIsNotSentBack(t *testing.T) {
	t.Parallel()

	llm := &turnScriptLLM{turns: []*domain.GenerationResult{
		turnCalling("c1", "bash", map[string]interface{}{"command": "pip install python-pptx"}),
		turnBlocked("b", "pip cannot reach the network: connection refused."),
	}}
	svc, err := New("tried-block").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.AddTool("bash", "Run a shell command", map[string]interface{}{"type": "object"},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return "connection refused", nil
		})

	res, err := svc.Run(context.Background(), "Make a deck about energy storage.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Blocked {
		t.Fatalf("want blocked, got %q", res.Text())
	}
	if got := llm.callCount(); got != 2 {
		t.Errorf("model was called %d times, want 2: a block after real work is final", got)
	}
}

// A run that may not use tools has nothing to try with, whoever said so.
func TestBlockIsNotSentBackWhenToolsAreOff(t *testing.T) {
	t.Parallel()

	t.Run("caller disabled tools", func(t *testing.T) {
		r := &Runtime{cfg: DefaultRunConfig()}
		r.cfg.ToolsDisabled = true
		msgs := []domain.Message{}
		if r.redirectUntriedBlock(context.Background(), &msgs, newQueryLoopState("goal", msgs, 5)) {
			t.Fatal("a run with tools disabled was told to go and use tools")
		}
	})
	t.Run("user forbade tools", func(t *testing.T) {
		r := &Runtime{cfg: DefaultRunConfig()}
		r.cfg.resolvedConstraints = &RunConstraints{ForbidTools: true}
		msgs := []domain.Message{}
		if r.redirectUntriedBlock(context.Background(), &msgs, newQueryLoopState("goal", msgs, 5)) {
			t.Fatal("a run whose user forbade tools was told to go and use tools")
		}
	})
	t.Run("missing-delivery redirect already spoke", func(t *testing.T) {
		r := &Runtime{cfg: DefaultRunConfig(), deliverableFallbackUsed: true}
		msgs := []domain.Message{}
		if r.redirectUntriedBlock(context.Background(), &msgs, newQueryLoopState("goal", msgs, 5)) {
			t.Fatal("two redirects contradicted each other on one run")
		}
	})
}

// What counts as trying. A tool search is how a run looks for a way to start,
// and an empty one is the usual last step before giving up, so it cannot be
// what makes a block final. Work done in an earlier segment of the same task
// can.
func TestWhatCountsAsAnAttempt(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"task_blocked", "task_complete", "search_available_tools", "tool_search_tool_bm25", "tool_search_tool_regex", "", "  "} {
		if countsAsAttempt(name) {
			t.Errorf("%q counted as an attempt", name)
		}
	}
	for _, name := range []string{"bash", "fs_read", "web_search", "memory_recall", "mcp_noted_list_tasks"} {
		if !countsAsAttempt(name) {
			t.Errorf("%q did not count as an attempt", name)
		}
	}
	if attemptedAnything([]string{"search_available_tools", "task_blocked"}) {
		t.Error("a search and a block is not an attempt")
	}

	r := &Runtime{cfg: DefaultRunConfig(), toolNamesUsed: map[string]bool{"search_available_tools": true}}
	r.cfg.PriorToolCalls = []string{"bash"}
	if !attemptedAnything(r.toolNamesUsedForTask()) {
		t.Error("work done in an earlier segment of the task was forgotten")
	}
}

// The push is made of what the run did and has — counts and whether it
// searched — and says nothing that depends on what any tool is called.
func TestUntriedBlockPromptIsBuiltFromTheRunNotFromNames(t *testing.T) {
	t.Parallel()

	first := untriedBlockPrompt(7, false)
	if !strings.Contains(first, "it has 7 tools") || !strings.Contains(first, "Search it once") {
		t.Errorf("first push is missing its facts: %s", first)
	}
	if strings.Contains(untriedBlockPrompt(7, true), "Search it once") {
		t.Error("a run that already searched was told to search")
	}
}

// A run that answered the first push by searching the catalogue, and stopped
// there, is pushed once more: a search that found no ready-made tool is the
// usual last step before giving up. Then the block stands.
func TestBlockAfterOnlyASearchIsPushedOnceMore(t *testing.T) {
	t.Parallel()

	r := &Runtime{cfg: DefaultRunConfig(), toolNamesUsed: map[string]bool{}}
	r.svc = &Service{toolRegistry: NewToolRegistry()}
	r.svc.toolRegistry.Register(domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{Name: "do_things"}}, nil, CategoryCustom)
	msgs := []domain.Message{}
	state := newQueryLoopState("goal", msgs, 5)

	if !r.redirectUntriedBlock(context.Background(), &msgs, state) {
		t.Fatal("first block with nothing tried should be sent back")
	}
	r.toolNamesUsed["search_available_tools"] = true
	if !r.redirectUntriedBlock(context.Background(), &msgs, state) {
		t.Fatal("a block after only a search should be sent back once more")
	}
	if !strings.Contains(msgs[len(msgs)-1].Content, untriedSecondMarker) {
		t.Errorf("second push reads wrong: %s", msgs[len(msgs)-1].Content)
	}
	if r.redirectUntriedBlock(context.Background(), &msgs, state) {
		t.Fatal("third block must stand")
	}
}

// The other half of the same live test: a contract edited "with everything
// else untouched" came back with the new text in another font and a heading
// gone, and the run reported the rest preserved without having looked. A run
// that produced a file it owes is asked once to check it before "done" is
// taken — whatever kind of file it is.
func TestProducedFileIsCheckedOnceBeforeDone(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"contract-v2.pdf", "report.md", "config.json", "build.sh"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			out := filepath.Join(dir, name)

			llm := &turnScriptLLM{
				constraints: `{"forbid_tools":false,"deliverables":[{"kind":"file","description":"the result","path":"` + out + `"}]}`,
				turns: []*domain.GenerationResult{
					turnCalling("c1", "bash", map[string]interface{}{"command": "make it"}),
					turnCalling("t1", "task_complete", map[string]interface{}{"result": "Written; everything else is unchanged."}),
					turnCalling("c2", "bash", map[string]interface{}{"command": "check it"}),
					turnCalling("t2", "task_complete", map[string]interface{}{"result": "Written and read back: both changes are in, nothing else differs."}),
				},
			}
			svc, err := New("file-check").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			defer svc.Close()
			ran := 0
			svc.AddTool("bash", "Run a shell command", map[string]interface{}{"type": "object"},
				func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
					ran++
					if ran == 1 {
						if err := os.WriteFile(out, []byte("content"), 0o644); err != nil {
							return nil, err
						}
					}
					return "ok", nil
				})

			res, err := svc.Run(context.Background(), "Make the change and save it as "+out)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Blocked {
				t.Fatalf("blocked: %q", res.Text())
			}
			if !llm.sawAt(2, "Read it back from disk") || !llm.sawAt(2, out) {
				t.Error("the first 'done' was taken without asking the model to check the file")
			}
			if got := llm.callCount(); got != 4 {
				t.Errorf("model was called %d times, want 4: the check is asked for once, not on every 'done'", got)
			}
			if ran != 2 {
				t.Errorf("bash ran %d times, want 2 (write, then check)", ran)
			}
			if !strings.Contains(res.Text(), "read back") {
				t.Errorf("the answer is the first draft, not the checked one: %q", res.Text())
			}
		})
	}
}

func TestFileCheckLeavesOtherRunsAlone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	made := filepath.Join(dir, "made.txt")
	old := filepath.Join(dir, "old.txt")
	for _, p := range []string{made, old} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().Add(-time.Minute)
	if err := os.Chtimes(old, start.Add(-time.Hour), start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	file := func(path string) RunConstraints {
		return RunConstraints{Deliverables: []DeliverableRequirement{{Kind: "file", Path: path}}}
	}

	if got := producedDeliverables(file(made), "", start); len(got) != 1 {
		t.Errorf("a file written during the run should be checked, got %v", got)
	}
	// Written through a tool the contract does not look on disk for — a skill,
	// an MCP server — and yet there on disk: checked all the same.
	viaSkill := RunConstraints{Deliverables: []DeliverableRequirement{{Kind: "file", Path: made, SatisfiedBy: "skill_pdf"}}}
	if got := producedDeliverables(viaSkill, "", start); len(got) != 1 {
		t.Errorf("a file written through a skill was not checked: %v", got)
	}
	// There before the run and not touched: not this run's work.
	if got := producedDeliverables(file(old), "", start); len(got) != 0 {
		t.Errorf("a file the run did not write was sent for a check: %v", got)
	}
	// Not written yet: the delivery contract asks for it first.
	if got := producedDeliverables(file(filepath.Join(dir, "missing.txt")), "", start); len(got) != 0 {
		t.Errorf("a file that does not exist was sent for a check: %v", got)
	}
	// Relative paths resolve against the workspace, as the contract does.
	if got := producedDeliverables(file("made.txt"), dir, start); len(got) != 1 {
		t.Errorf("a workspace-relative file was not found: %v", got)
	}
	// A directory counts when something in it is new.
	if got := producedDeliverables(file(dir), "", start); len(got) != 1 {
		t.Errorf("a directory with a new file in it was not found: %v", got)
	}
	// Something that is not a file is not this check's business.
	mail := RunConstraints{Deliverables: []DeliverableRequirement{{Kind: "email", Description: "send it"}}}
	if got := producedDeliverables(mail, "", start); len(got) != 0 {
		t.Errorf("an email was sent for a file check: %v", got)
	}

	// No service, nothing called: no one to do the checking, nothing made.
	r := &Runtime{cfg: DefaultRunConfig()}
	c := file(made)
	r.cfg.resolvedConstraints = &c
	msgs := []domain.Message{}
	if r.redirectToDeliverableCheck(context.Background(), &msgs, newQueryLoopState("goal", msgs, 5)) {
		t.Error("a runtime with no service was redirected")
	}
}
