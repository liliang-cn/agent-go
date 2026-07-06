// Package main is an ambitious, single-file showcase of the AgentGo stack wired
// for OpenTelemetry / Arize Phoenix observability. It runs a "Deep Research"
// pipeline and exports a rich trace tree.
//
// # What it demonstrates
//
//   - Observer + Phoenix: one otelobserver.Phoenix() Observer is attached to
//     EVERY Service so the whole run exports OpenInference spans; a second local
//     BaseObserver prints every model / tool / sub-agent / checkpoint event with
//     timing so the flow is visible on stdout even with Phoenix offline.
//   - TaskPlan.Validate(): the coordinator builds a valid 4-item research plan
//     (3 subtopics + a synthesis that is BlockedBy the three) and validates it;
//     it then intentionally builds a cyclic plan and logs the structured
//     *TaskPlanValidationError the guard returns (then discards it).
//   - Sub-agents in parallel: three worker sub-agents research subtopics
//     concurrently, each doing real tool calls (web_search + fetch_url over a
//     canned in-code corpus) and model turns. They share the coordinator's
//     TaskID so their AGENT / LLM / TOOL spans nest under one Phoenix trace.
//   - stream MergeEvents / Concat: the three workers' event streams are fanned
//     in with agent.MergeEvents(...) for a live merged log; the synthesizer's
//     streamed output is accumulated with agent.Concat(...).
//   - Sandbox: the synthesizer runs with a pkg/sandbox NewLocal sandbox and
//     writes the final report.md into the workspace via its fs_write tool.
//   - Checkpoint: the synthesizer runs as a top-level task, so its terminal
//     checkpoint fires the OnCheckpoint observer seam and closes the trace root.
//
// PTC is OFF on every Service (WithPTC(false)) — the intended live provider
// (Alibaba/Qwen OpenAI-compatible) rejects PTC.
//
// # Environment (provider is read ONLY from env — no secrets in this file)
//
//	LLM_BASE_URL   OpenAI-compatible endpoint, e.g. https://.../compatible-mode/v1
//	LLM_API_KEY    API key (secret; read from env only, never written to disk)
//	LLM_MODEL      chat model, e.g. qwen3.7-plus
//	EMBED_MODEL    (optional) embedding model — not required by this example
//
// When LLM_BASE_URL + LLM_API_KEY + LLM_MODEL are all set the example drives the
// real provider; otherwise it falls back to a deterministic offline scripted
// mock. Both paths are dependency-free: the research tools return canned data.
//
// # Run it
//
//	# offline, deterministic, exits 0 with no env set:
//	GOWORK=off go run ./examples/deep-research
//
//	# against a real provider + Phoenix:
//	pip install arize-phoenix && phoenix serve   # or docker run -p 6006:6006 arizephoenix/phoenix
//	export LLM_BASE_URL=... LLM_API_KEY=... LLM_MODEL=...
//	GOWORK=off go run ./examples/deep-research
//	# then open http://localhost:6006
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/otelobserver"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

// sessionTaskIDKey mirrors the unexported runtime key the framework uses to
// carry a task id on a Session's Context (pkg/agent/service_memory_context.go).
// Setting it on the parent session propagates the coordinator TaskID into each
// isolated sub-agent session, so worker spans nest under the same Phoenix root.
const sessionTaskIDKey = "runtime.task_id"

// ─────────────────────────────────────────────────────────────────────────────
// Canned research corpus — keyed loosely by subtopic. Tools return the
// {ok,data,error} shape so both the mock and a real model consume stable JSON.
// ─────────────────────────────────────────────────────────────────────────────

type corpusDoc struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Body    string `json:"body"`
}

var researchCorpus = map[string][]corpusDoc{
	"training": {
		{
			URL:     "https://research.example/energy/training-cost",
			Title:   "The energy cost of training frontier models",
			Snippet: "Training a frontier LLM can draw megawatt-hours; compute has grown ~4x/year.",
			Body:    "Frontier model training runs consume on the order of 1,000–10,000 MWh. The dominant driver is accelerator-hours: cluster size times wall-clock weeks. Efficiency gains (better kernels, FP8) partially offset the 4x/year growth in training compute.",
		},
	},
	"inference": {
		{
			URL:     "https://research.example/energy/inference-footprint",
			Title:   "Inference now dominates lifetime energy",
			Snippet: "At scale, serving a model costs more total energy than training it.",
			Body:    "For widely deployed models, aggregate inference energy overtakes the one-time training cost within months. Per-query energy is small but multiplied by billions of requests. Batching, quantization, and speculative decoding are the main levers.",
		},
	},
	"datacenter": {
		{
			URL:     "https://research.example/energy/datacenter-pue",
			Title:   "Data-center efficiency and PUE trends",
			Snippet: "Hyperscale PUE approaches 1.1; cooling and grid mix decide real impact.",
			Body:    "Power Usage Effectiveness (PUE) at hyperscale sites is near 1.1, so most drawn power reaches the accelerators. The carbon impact then hinges on regional grid mix and on water/heat-reuse for cooling rather than on the servers alone.",
		},
	},
}

// lookupCorpus returns the best-matching corpus bucket for a free-text query.
func lookupCorpus(query string) (string, []corpusDoc) {
	q := strings.ToLower(query)
	// deterministic order for tie-breaking
	keys := make([]string, 0, len(researchCorpus))
	for k := range researchCorpus {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.Contains(q, k) {
			return k, researchCorpus[k]
		}
	}
	// Fallback keyword routing so a real model's paraphrase still lands.
	switch {
	case strings.Contains(q, "train"):
		return "training", researchCorpus["training"]
	case strings.Contains(q, "serv") || strings.Contains(q, "infer") || strings.Contains(q, "query"):
		return "inference", researchCorpus["inference"]
	default:
		return "datacenter", researchCorpus["datacenter"]
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Research tools (real Go closures, canned data — no network).
// ─────────────────────────────────────────────────────────────────────────────

type webSearchParams struct {
	Query string `json:"query" desc:"Search query for the research subtopic"`
}

type fetchURLParams struct {
	URL string `json:"url" desc:"URL to fetch full article text for"`
}

func newWebSearchTool() *agent.Tool {
	return agent.NewTool("web_search", "Search the research corpus for a subtopic. Returns matching documents (url, title, snippet).",
		func(_ context.Context, p *webSearchParams) (any, error) {
			bucket, docs := lookupCorpus(p.Query)
			results := make([]map[string]any, 0, len(docs))
			for _, d := range docs {
				results = append(results, map[string]any{"url": d.URL, "title": d.Title, "snippet": d.Snippet})
			}
			return map[string]any{"ok": true, "data": map[string]any{"topic": bucket, "results": results}}, nil
		})
}

func newFetchURLTool() *agent.Tool {
	return agent.NewTool("fetch_url", "Fetch the full body text of a corpus document by URL.",
		func(_ context.Context, p *fetchURLParams) (any, error) {
			for _, docs := range researchCorpus {
				for _, d := range docs {
					if d.URL == p.URL {
						return map[string]any{"ok": true, "data": map[string]any{"url": d.URL, "title": d.Title, "body": d.Body}}, nil
					}
				}
			}
			return map[string]any{"ok": false, "error": "url not found in corpus"}, nil
		})
}

// ─────────────────────────────────────────────────────────────────────────────
// Deterministic offline mock LLM.
//
// It is stateless and concurrency-safe: every decision is derived purely from
// the message history + offered tools, so parallel workers sharing one instance
// never race. Behaviour:
//   - web_search offered & not yet called   -> call web_search(goal)
//   - fetch_url offered & not yet called, and a web_search result exists
//                                            -> call fetch_url(first result url)
//   - fs_write offered & not yet called      -> call fs_write(report.md, report)
//   - otherwise                              -> emit a text summary of results
// ─────────────────────────────────────────────────────────────────────────────

type mockLLM struct{}

func offeredTool(tools []domain.ToolDefinition, name string) bool {
	for _, t := range tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

func toolAlreadyCalled(messages []domain.Message, name string) bool {
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == name {
				return true
			}
		}
	}
	return false
}

// firstResultURL scans prior tool-result messages for a corpus url.
func firstResultURL(messages []domain.Message) string {
	for _, m := range messages {
		if m.Role != "tool" {
			continue
		}
		for _, docs := range researchCorpus {
			for _, d := range docs {
				if strings.Contains(m.Content, d.URL) {
					return d.URL
				}
			}
		}
	}
	return ""
}

// lastUserGoal returns the earliest user message (the goal / task input).
func lastUserGoal(messages []domain.Message) string {
	for _, m := range messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

// collectToolFindings gathers snippets/bodies seen in tool-result messages so
// the final text summary and the report have real substance.
func collectToolFindings(messages []domain.Message) []string {
	var out []string
	for _, m := range messages {
		if m.Role != "tool" {
			continue
		}
		for _, docs := range researchCorpus {
			for _, d := range docs {
				if strings.Contains(m.Content, d.URL) {
					if strings.Contains(m.Content, "body") && strings.Contains(m.Content, d.Body) {
						out = append(out, fmt.Sprintf("%s — %s", d.Title, d.Body))
					} else {
						out = append(out, fmt.Sprintf("%s — %s", d.Title, d.Snippet))
					}
				}
			}
		}
	}
	return dedupe(out)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (m *mockLLM) turn(messages []domain.Message, tools []domain.ToolDefinition) *domain.GenerationResult {
	goal := lastUserGoal(messages)

	if offeredTool(tools, "web_search") && !toolAlreadyCalled(messages, "web_search") {
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID:       "call_search_" + uuid.NewString()[:8],
			Type:     "function",
			Function: domain.FunctionCall{Name: "web_search", Arguments: map[string]any{"query": goal}},
		}}}
	}

	if offeredTool(tools, "fetch_url") && !toolAlreadyCalled(messages, "fetch_url") {
		if url := firstResultURL(messages); url != "" {
			return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
				ID:       "call_fetch_" + uuid.NewString()[:8],
				Type:     "function",
				Function: domain.FunctionCall{Name: "fetch_url", Arguments: map[string]any{"url": url}},
			}}}
		}
	}

	if offeredTool(tools, "fs_write") && !toolAlreadyCalled(messages, "fs_write") {
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID:   "call_write_" + uuid.NewString()[:8],
			Type: "function",
			Function: domain.FunctionCall{Name: "fs_write", Arguments: map[string]any{
				"path":    "report.md",
				"content": buildReport(goal, messages),
			}},
		}}}
	}

	// Terminal text turn. If this is the synthesizer (fs_write already done),
	// acknowledge the saved report rather than re-listing raw findings.
	if toolAlreadyCalled(messages, "fs_write") {
		return &domain.GenerationResult{Content: "Wrote the synthesized research report to report.md.", FinishReason: "stop"}
	}
	// Otherwise summarize whatever corpus findings are in context.
	findings := collectToolFindings(messages)
	if len(findings) == 0 {
		return &domain.GenerationResult{Content: "No findings gathered.", FinishReason: "stop"}
	}
	var b strings.Builder
	b.WriteString("Summary of findings:\n")
	for _, f := range findings {
		b.WriteString("- ")
		b.WriteString(f)
		b.WriteString("\n")
	}
	return &domain.GenerationResult{Content: strings.TrimSpace(b.String()), FinishReason: "stop"}
}

// buildReport renders a markdown report from the synthesizer goal (which packs
// the worker summaries) plus any findings visible in the message history.
func buildReport(goal string, messages []domain.Message) string {
	var b strings.Builder
	b.WriteString("# Deep Research Report\n\n")
	b.WriteString("## Question\n\n")
	b.WriteString("How much energy does modern AI actually use, across its lifecycle?\n\n")
	b.WriteString("## Findings by subtopic\n\n")
	// The synthesizer goal embeds "FINDINGS:\n..." lines from the workers.
	if idx := strings.Index(goal, "FINDINGS:"); idx >= 0 {
		b.WriteString(strings.TrimSpace(goal[idx+len("FINDINGS:"):]))
		b.WriteString("\n\n")
	}
	if extra := collectToolFindings(messages); len(extra) > 0 {
		b.WriteString("## Corroborating sources\n\n")
		for _, f := range extra {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Conclusion\n\n")
	b.WriteString("AI energy use is dominated by training compute early on, but at deployment scale inference overtakes it; data-center efficiency and grid mix determine the real carbon impact.\n")
	return b.String()
}

// Generator interface — only the tool-calling paths are exercised by the loop.
func (m *mockLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (m *mockLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (m *mockLLM) GenerateWithTools(_ context.Context, messages []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return m.turn(messages, tools), nil
}
func (m *mockLLM) StreamWithTools(_ context.Context, messages []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(m.turn(messages, tools))
}
func (m *mockLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (m *mockLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Local logging Observer: prints every seam with timing.
// ─────────────────────────────────────────────────────────────────────────────

type loggingObserver struct {
	agent.BaseObserver
	start time.Time
}

func (o *loggingObserver) at() string {
	return fmt.Sprintf("%7.1fms", float64(time.Since(o.start).Microseconds())/1000)
}
func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
func (o *loggingObserver) OnModelStart(_ context.Context, i agent.ModelInfo) {
	fmt.Printf("  [%s] model  START  agent=%-12s round=%d msgs=%d tools=%d\n", o.at(), i.AgentName, i.Round, i.Messages, i.Tools)
}
func (o *loggingObserver) OnModelEnd(_ context.Context, i agent.ModelInfo, r *agent.ModelResult, err error) {
	tc := 0
	if r != nil {
		tc = r.ToolCalls
	}
	fmt.Printf("  [%s] model  END    agent=%-12s toolCalls=%d err=%v\n", o.at(), i.AgentName, tc, err)
}
func (o *loggingObserver) OnToolStart(_ context.Context, i agent.ToolInfo) {
	fmt.Printf("  [%s] tool   START  %-10s agent=%-12s inner=%v call=%s\n", o.at(), i.Tool, i.AgentName, i.Inner, short(i.CallID))
}
func (o *loggingObserver) OnToolEnd(_ context.Context, i agent.ToolInfo, _ any, err error) {
	fmt.Printf("  [%s] tool   END    %-10s agent=%-12s err=%v\n", o.at(), i.Tool, i.AgentName, err)
}
func (o *loggingObserver) OnSubAgentStart(_ context.Context, i agent.SubAgentInfo) {
	fmt.Printf("  [%s] SUBAGT START  %-12s goal=%q\n", o.at(), i.Name, truncate(i.Goal, 48))
}
func (o *loggingObserver) OnSubAgentEnd(_ context.Context, i agent.SubAgentInfo, _ any, err error) {
	fmt.Printf("  [%s] SUBAGT END    %-12s err=%v\n", o.at(), i.Name, err)
}
func (o *loggingObserver) OnCheckpoint(_ context.Context, i agent.CheckpointInfo) {
	fmt.Printf("  [%s] CHKPT         task=%s reason=%s round=%d msgs=%d\n", o.at(), short(i.TaskID), i.Reason, i.Round, i.Messages)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ─────────────────────────────────────────────────────────────────────────────

type subtopic struct {
	id      string
	subject string
	goal    string
}

func main() {
	ctx := context.Background()
	logObs := &loggingObserver{start: time.Now()}

	// 1) One Phoenix Observer for the whole run. Safe even if Phoenix is down —
	//    the batch exporter buffers/drops and the program still exits 0.
	obs, shutdown, err := otelobserver.Phoenix(ctx,
		otelobserver.WithPhoenixServiceName("agent-go-deep-research"),
		otelobserver.WithObserverOptions(otelobserver.WithRootKind("AGENT")),
	)
	if err != nil {
		fmt.Printf("phoenix wiring failed: %v\n", err)
		os.Exit(1)
	}
	var shutdownOnce sync.Once
	flush := func() { shutdownOnce.Do(func() { _ = shutdown(ctx) }) }
	defer flush()

	// 2) LLM: real provider from env, else deterministic offline mock.
	llm, mode := resolveLLM()
	fmt.Println("=== Deep Research — AgentGo showcase ===")
	fmt.Printf("LLM: %s\n\n", mode)

	// 3) Isolated home so the example never touches the user's ~/.agentgo.
	home, err := os.MkdirTemp("", "deep-research-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)
	cfg := &config.Config{
		Home:   home,
		RAG:    config.RAGConfig{Enabled: false},
		Memory: config.MemoryConfig{StoreType: "file", MemoryPath: filepath.Join(home, "data", "memories")},
	}
	cfg.ApplyHomeLayout()

	// A single coordinator TaskID ties every span into one Phoenix trace tree.
	taskID := uuid.NewString()

	// ── Step 1: TaskPlan + Validate ──────────────────────────────────────────
	subtopics := []subtopic{
		{id: "r1", subject: "Training energy", goal: "Research the energy cost of TRAINING frontier AI models. Use web_search then fetch_url on the top result, then summarize the key numbers."},
		{id: "r2", subject: "Inference energy", goal: "Research the energy cost of INFERENCE / serving AI models at scale. Use web_search then fetch_url on the top result, then summarize."},
		{id: "r3", subject: "Data-center efficiency", goal: "Research DATACENTER efficiency (PUE) and grid mix for AI. Use web_search then fetch_url on the top result, then summarize."},
	}
	runPlanValidation(subtopics)

	// ── Step 2 & 3: parallel worker sub-agents + MergeEvents fan-in ──────────
	workerFinals := runWorkers(ctx, cfg, llm, taskID, subtopics, obs, logObs)

	// ── Step 4: synthesizer with a sandbox writes report.md ──────────────────
	reportPath := runSynthesizer(ctx, cfg, llm, home, taskID, subtopics, workerFinals, obs, logObs)

	// 5) Flush spans before exit so batched spans reach Phoenix.
	flush()

	fmt.Println()
	if reportPath != "" {
		fmt.Printf("Report written to: %s\n", reportPath)
	}
	fmt.Println("Trace exported to Phoenix — open http://localhost:6006")
	fmt.Println("(Runs fine with Phoenix offline — spans just don't appear.)")
}

// resolveLLM builds the real provider from env, or falls back to the mock.
func resolveLLM() (domain.Generator, string) {
	base := strings.TrimSpace(os.Getenv("LLM_BASE_URL"))
	key := strings.TrimSpace(os.Getenv("LLM_API_KEY"))
	model := strings.TrimSpace(os.Getenv("LLM_MODEL"))
	if base != "" && key != "" && model != "" {
		gen, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
			BaseURL:  base,
			APIKey:   key,
			LLMModel: model,
		})
		if err != nil {
			fmt.Printf("provider init failed (%v) — falling back to offline mock\n", err)
			return &mockLLM{}, "offline scripted mock (provider init failed)"
		}
		return gen, fmt.Sprintf("live provider model=%s", model)
	}
	return &mockLLM{}, "offline scripted mock (no LLM_* env set)"
}

// runPlanValidation builds a valid plan, validates it, then demonstrates the
// cyclic-plan guard.
func runPlanValidation(subtopics []subtopic) {
	fmt.Println("── Step 1: TaskPlan + Validate ─────────────────────────────")
	now := time.Now()
	items := make([]agent.TaskPlanItem, 0, len(subtopics)+1)
	blockers := make([]string, 0, len(subtopics))
	for _, s := range subtopics {
		items = append(items, agent.TaskPlanItem{
			ID: s.id, Subject: s.subject, Status: agent.PlanItemStatusPending,
			Blocks: []string{"synth"}, CreatedAt: now, UpdatedAt: now,
		})
		blockers = append(blockers, s.id)
	}
	items = append(items, agent.TaskPlanItem{
		ID: "synth", Subject: "Synthesize final report", Status: agent.PlanItemStatusPending,
		BlockedBy: blockers, CreatedAt: now, UpdatedAt: now,
	})
	plan := &agent.TaskPlan{
		ID: uuid.NewString(), Goal: "Deep research: AI energy use", Items: items,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := plan.Validate(); err != nil {
		fmt.Printf("  UNEXPECTED: valid plan failed validation: %v\n", err)
	} else {
		fmt.Printf("  valid plan OK: %d items, 'synth' BlockedBy %v\n", len(plan.Items), blockers)
	}

	// Intentionally-cyclic plan to demonstrate the structured guard.
	cyclic := &agent.TaskPlan{
		ID: uuid.NewString(), Goal: "broken plan", CreatedAt: now, UpdatedAt: now,
		Items: []agent.TaskPlanItem{
			{ID: "a", Subject: "A", BlockedBy: []string{"b"}, CreatedAt: now, UpdatedAt: now},
			{ID: "b", Subject: "B", BlockedBy: []string{"a"}, CreatedAt: now, UpdatedAt: now},
		},
	}
	if err := cyclic.Validate(); err != nil {
		fmt.Printf("  cyclic plan correctly rejected: %v\n", err)
	} else {
		fmt.Println("  UNEXPECTED: cyclic plan passed validation")
	}
	fmt.Println()
}

// runWorkers launches the three research sub-agents in parallel, fans their
// event streams in with MergeEvents for a live log, and returns each worker's
// final summary text keyed by subtopic subject.
func runWorkers(
	ctx context.Context,
	cfg *config.Config,
	llm domain.Generator,
	taskID string,
	subtopics []subtopic,
	obs, logObs agent.Observer,
) map[string]string {
	fmt.Println("── Step 2+3: parallel research sub-agents (MergeEvents fan-in) ─")

	// Worker service: research tools + observers, PTC OFF. All sub-agents run
	// against this Service so its observers (Phoenix + local log) fire for them.
	workerSvc, err := agent.New("research-coordinator").
		WithPTC(false).
		WithConfig(cfg).
		WithLLM(llm).
		WithObserver(obs).
		WithObserver(logObs).
		WithTool(newWebSearchTool()).
		WithTool(newFetchURLTool()).
		Build()
	if err != nil {
		panic(err)
	}
	defer workerSvc.Close()

	// Parent session carrying the coordinator TaskID; isolated sub-agent
	// sessions inherit it so their spans nest under the same Phoenix root.
	parent := agent.NewSession("research-coordinator")
	parent.SetContext(sessionTaskIDKey, taskID)

	type workerRun struct {
		subject string
		sa      *agent.SubAgent
		events  <-chan *agent.Event
	}
	runs := make([]workerRun, 0, len(subtopics))
	for _, s := range subtopics {
		worker := agent.NewAgent("worker-" + s.id)
		worker.SetInstructions("You are a research worker. Use web_search and fetch_url to gather facts on your subtopic, then give a concise factual summary. Do not invent numbers.")
		sa := agent.NewSubAgent(agent.SubAgentConfig{
			Agent:         worker,
			Goal:          s.goal,
			Service:       workerSvc,
			ParentSession: parent,
			Isolated:      true,
			MaxTurns:      5,
		})
		runs = append(runs, workerRun{subject: s.subject, sa: sa, events: sa.RunAsync(ctx)})
	}

	// Fan the three event streams into one and drain it for a live merged log.
	chans := make([]<-chan *agent.Event, 0, len(runs))
	for _, r := range runs {
		chans = append(chans, r.events)
	}
	merged := agent.MergeEvents(chans...)
	for evt := range merged {
		switch evt.Type {
		case agent.EventTypeToolCall:
			fmt.Printf("    · %-12s tool_call %s\n", evt.AgentName, evt.ToolName)
		case agent.EventTypeComplete:
			fmt.Printf("    · %-12s done\n", evt.AgentName)
		}
	}

	// Merged stream drained => every worker Run has returned. Collect final
	// texts reliably from GetResult (return value, not the lossy event buffer).
	finals := make(map[string]string, len(runs))
	for _, r := range runs {
		res, rerr := r.sa.GetResult()
		text := ""
		if s, ok := res.(string); ok {
			text = s
		}
		if rerr != nil {
			text = "(worker error: " + rerr.Error() + ")"
		}
		finals[r.subject] = strings.TrimSpace(text)
		fmt.Printf("  [%s] %s => %s\n", r.sa.GetState(), r.subject, truncate(finals[r.subject], 80))
	}
	fmt.Println()
	return finals
}

// runSynthesizer runs a top-level synthesizer task with a local sandbox. It
// accumulates the streamed output with Concat and writes report.md via fs_write.
func runSynthesizer(
	ctx context.Context,
	cfg *config.Config,
	llm domain.Generator,
	home, taskID string,
	subtopics []subtopic,
	workerFinals map[string]string,
	obs, logObs agent.Observer,
) string {
	fmt.Println("── Step 4: synthesizer + sandbox writes report.md (Concat) ────")

	workspace := filepath.Join(home, "workspace")
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(workspace))
	if err != nil {
		panic(err)
	}
	defer sb.Close()

	synthSvc, err := agent.New("synthesizer").
		WithPTC(false).
		WithConfig(cfg).
		WithLLM(llm).
		WithObserver(obs).
		WithObserver(logObs).
		WithSandbox(sb).
		Build()
	if err != nil {
		panic(err)
	}
	defer synthSvc.Close()

	// Pack the worker findings into the goal so both the mock and a real model
	// synthesize from the same material. buildReport() reads the FINDINGS block.
	var findings strings.Builder
	findings.WriteString("FINDINGS:\n")
	for _, s := range subtopics {
		findings.WriteString(fmt.Sprintf("### %s\n%s\n\n", s.subject, workerFinals[s.subject]))
	}
	goal := "Write a cited markdown research report answering: 'How much energy does modern AI use across its lifecycle?'. " +
		"Call fs_write to save it to report.md in the workspace. Base it strictly on the following worker findings.\n\n" +
		findings.String()

	// Top-level Run so terminal completion fires the checkpoint observer seam
	// (closing the Phoenix trace root) and shares the coordinator TaskID.
	events, err := synthSvc.RunStreamWithOptions(ctx, goal, agent.WithTaskID(taskID), agent.WithMaxTurns(6))
	if err != nil {
		panic(err)
	}
	final := agent.Concat(events) // accumulate streamed synthesizer output
	fmt.Printf("  synthesizer final: %s\n", truncate(final, 80))

	// Read the report the sandbox tool wrote.
	data, rerr := sb.ReadFile(ctx, "report.md")
	if rerr != nil {
		fmt.Printf("  report not found: %v\n", rerr)
		return ""
	}
	fmt.Println("  --- report.md (first lines) ---")
	for i, line := range strings.Split(string(data), "\n") {
		if i >= 6 {
			fmt.Println("  …")
			break
		}
		fmt.Printf("  | %s\n", line)
	}
	return filepath.Join(workspace, "report.md")
}
