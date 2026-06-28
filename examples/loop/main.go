// Command loop demonstrates the full AgentGo autonomy loop, end-to-end and
// fully offline:
//
//	scheduler  ->  subagent implements (inside an isolated git worktree)
//	           ->  output-lint as verifier (blocks until the deliverable is right)
//	           ->  checkpoint (auto-persisted by the TeamManager)
//	           ->  human gate (yield / resume)  ->  verified & done
//
// Everything runs against a scripted MockLLM so `go run ./examples/loop` is
// deterministic and needs no network or API keys. The flow:
//
//  1. Scheduler: a custom scheduler.Executor is registered and fired once via
//     RunTask (the full NewScheduler/Start/CreateTask/Stop wiring is shown).
//  2. The executor spawns an *implementer* sub-agent that runs INSIDE a git
//     worktree (pkg/worktree). The sub-agent writes its deliverable through its
//     fs_write tool, which is rooted at the worktree — so the file lands in the
//     throwaway checkout, NOT the repo root.
//  3. An OutputLint acts as the *verifier*. On the first pass the implementer
//     produces the wrong content, so the lint BLOCKS the result — proving the
//     gate is real.
//  4. A TeamManager task is submitted (checkpoints auto-persist), then YIELDED
//     to a human. We inspect the checkpoints, simulate a human supplying the
//     missing token, and RESUME — the second pass satisfies the verifier and
//     the task completes.
//
// To run against a real provider instead of the MockLLM, swap the generator:
//
//	brain, _ := pool.NewPool(pool.PoolConfig{ ... })   // see examples/operator-collab
//	manager.SetLLM(brain)
//	// and pass `brain` instead of `mock` to agent.New(...).WithLLM(...)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
	"github.com/liliang-cn/agent-go/v2/pkg/worktree"
)

// requiredToken is the verification condition: the deliverable file must
// contain this exact token for the verifier lint to pass.
const (
	deliverableFile = "solution.txt"
	requiredToken   = "VERIFIED-OK"
)

func main() {
	if !worktree.Available() {
		log.Fatal("git is required for this example (worktree isolation)")
	}
	ctx := context.Background()

	// --- isolate everything in a temp repo so we never touch the real repo ---
	repoDir, cleanupRepo := mustInitTempRepo()
	defer cleanupRepo()

	home, err := os.MkdirTemp("", "loop-home-")
	if err != nil {
		log.Fatalf("create temp home: %v", err)
	}
	defer os.RemoveAll(home)

	fmt.Println("=== AgentGo autonomy loop (offline / deterministic) ===")
	fmt.Printf("repo  : %s\n", repoDir)
	fmt.Printf("home  : %s\n\n", home)

	// The scripted brain shared by the sub-agent and the manager task.
	mock := newScriptedLLM()

	// ----------------------------------------------------------------------
	// Build the implementer Service (the sub-agent's parent). It carries the
	// verifier lint and the mock brain. The sub-agent will run a worktree-
	// rooted CHILD of this service (pkg/agent WithSubAgentWorktree).
	// ----------------------------------------------------------------------
	cfg := loadConfig(home)
	parentSandbox, err := sandbox.NewLocal() // throwaway; child re-roots at worktree
	if err != nil {
		log.Fatalf("create parent sandbox: %v", err)
	}
	defer parentSandbox.Close()

	implementer, err := agent.New("Implementer").
		WithLLM(mock).
		WithConfig(cfg).
		WithSandbox(parentSandbox).
		Build()
	if err != nil {
		log.Fatalf("build implementer service: %v", err)
	}

	// Verifier-as-lint: block the result unless the deliverable exists in the
	// worktree AND contains the required token. The lint reads the live
	// worktree path (published by the executor) so it inspects real files.
	var liveWorktree string
	implementer.RegisterOutputLint(agent.LintFunc{
		NameValue: "deliverable_must_be_verified",
		Fn: func(text string, lc agent.LintContext) (bool, string) {
			path := filepath.Join(liveWorktree, deliverableFile)
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return false, fmt.Sprintf("deliverable %q not found in worktree (%v)", deliverableFile, readErr)
			}
			if !strings.Contains(string(data), requiredToken) {
				return false, fmt.Sprintf("deliverable is missing required token %q (got: %q)", requiredToken, strings.TrimSpace(string(data)))
			}
			return true, ""
		},
	}, "Implementer")

	// ----------------------------------------------------------------------
	// 1) SCHEDULER: register a custom executor and fire it once via RunTask.
	// ----------------------------------------------------------------------
	sched := scheduler.NewScheduler(cfg)
	exec := &implementExecutor{
		ctx:         ctx,
		parent:      implementer,
		repoDir:     repoDir,
		mock:        mock,
		setWorktree: func(p string) { liveWorktree = p },
	}
	sched.RegisterExecutor(exec)
	if err := sched.Start(); err != nil {
		log.Fatalf("scheduler start: %v", err)
	}
	defer sched.Stop()

	// --- PASS 1: scheduler fires; sub-agent implements in a worktree; the
	// verifier blocks because the deliverable is wrong. ---
	taskID, err := sched.CreateTask(&scheduler.Task{
		Type:        string(implementTaskType),
		Description: "implement the deliverable inside an isolated worktree",
		Enabled:     true,
		// No Schedule -> not auto-run; we trigger it explicitly with RunTask
		// to keep the demo fast. A cron like "*/1 * * * *" would auto-fire.
		Parameters: map[string]string{"goal": "write the deliverable", "human_approved": "false"},
	})
	if err != nil {
		log.Fatalf("scheduler create task: %v", err)
	}
	fmt.Printf("[scheduler] created task %s; firing now via RunTask\n", taskID[:8])

	res1, err := sched.RunTask(taskID)
	if err != nil {
		log.Fatalf("scheduler run task: %v", err)
	}
	fmt.Printf("[scheduler] pass 1 result: success=%v output=%q\n\n", res1.Success, strings.TrimSpace(res1.Output))

	// ----------------------------------------------------------------------
	// 4 + 5) TeamManager task: checkpoint + human gate (yield/resume).
	//
	// The verifier blocked pass 1. We now model the human gate with a real
	// TeamManager task: it is submitted (checkpoints auto-persist), YIELDED to
	// a human, the checkpoints are inspected, and it is RESUMED with the
	// human's approval — which completes the task.
	// ----------------------------------------------------------------------
	runManagerGate(ctx, home, mock)

	// --- PASS 2: after the human gate approved the work, fire the scheduler
	// task again with human_approved=true; the sub-agent now writes the
	// correct deliverable and the verifier PASSES. ---
	fmt.Println("\n--- scheduler re-run after human approval ---")
	if err := sched.UpdateTask(&scheduler.Task{
		ID:          taskID,
		Type:        string(implementTaskType),
		Description: "implement the deliverable inside an isolated worktree",
		Enabled:     true,
		Parameters:  map[string]string{"goal": "write the deliverable", "human_approved": "true"},
	}); err != nil {
		log.Fatalf("scheduler update task: %v", err)
	}
	res2, err := sched.RunTask(taskID)
	if err != nil {
		log.Fatalf("scheduler run task (pass 2): %v", err)
	}
	fmt.Printf("[scheduler] pass 2 result: success=%v output=%q\n", res2.Success, strings.TrimSpace(res2.Output))

	fmt.Println("\n=== loop complete ===")
}

// ============================================================================
// Scheduler executor: drives the worktree sub-agent + verifier loop.
// ============================================================================

const implementTaskType scheduler.TaskType = "implement-in-worktree"

type implementExecutor struct {
	ctx         context.Context
	parent      *agent.Service
	repoDir     string
	mock        *scriptedLLM
	setWorktree func(string)
}

func (e *implementExecutor) Type() scheduler.TaskType { return implementTaskType }

func (e *implementExecutor) Validate(params map[string]string) error {
	if strings.TrimSpace(params["goal"]) == "" {
		return fmt.Errorf("parameter 'goal' is required")
	}
	return nil
}

func (e *implementExecutor) Execute(_ context.Context, params map[string]string) (*scheduler.TaskResult, error) {
	goal := params["goal"]
	// The mock brain keys off the conversation; appending the human-approval
	// token to the goal is what makes pass 2 write the correct deliverable.
	if params["human_approved"] == "true" {
		goal += " (human approved: include token " + requiredToken + ")"
	}

	// Spawn the implementer sub-agent INSIDE a git worktree. Its fs_write tool
	// is rooted at the worktree checkout (WithSubAgentWorktree), so the file
	// lands in the throwaway checkout, not the repo root.
	sa := agent.NewSubAgent(
		agent.SubAgentConfig{
			Agent:   agent.NewAgent("Implementer"),
			Service: e.parent,
			Goal:    goal,
			Mode:    agent.SubAgentModeForeground,
			// Keep the (dirty) worktree alive past the sub-agent run so the
			// verifier can inspect the produced file; the executor force-removes
			// it after verification to self-clean.
			Worktree: &agent.WorktreeSpec{
				RepoDir:     e.repoDir,
				Options:     []worktree.Option{worktree.WithDetach()},
				KeepOnDirty: true,
			},
		},
		agent.WithSubAgentService(e.parent),
		agent.WithSubAgentMaxTurns(4),
	)

	// Drain progress events (also keeps the worktree path fresh).
	go func() {
		for range sa.ProgressChan() {
			if p := sa.WorktreePath(); p != "" {
				e.setWorktree(p)
			}
		}
	}()

	fmt.Println("[executor] running implementer sub-agent in an isolated worktree...")
	result, runErr := sa.Run(e.ctx)
	if runErr != nil {
		return &scheduler.TaskResult{Success: false, Error: runErr.Error()}, nil
	}
	wtPath := sa.WorktreePath()
	e.setWorktree(wtPath)
	if wtPath == "" {
		return &scheduler.TaskResult{Success: false, Error: "sub-agent did not create a worktree"}, nil
	}
	fmt.Printf("[executor] sub-agent worktree: %s\n", wtPath)
	// Prove the write landed in the worktree, not the repo root.
	if _, err := os.Stat(filepath.Join(wtPath, deliverableFile)); err == nil {
		fmt.Printf("[executor] deliverable written INSIDE worktree: %s\n", filepath.Join(wtPath, deliverableFile))
	} else {
		fmt.Printf("[executor] WARNING: deliverable not found in worktree: %v\n", err)
	}
	if _, err := os.Stat(filepath.Join(e.repoDir, deliverableFile)); os.IsNotExist(err) {
		fmt.Printf("[executor] repo root is clean (no %s leaked)\n", deliverableFile)
	}

	// VERIFIER-AS-LINT: run the registered output lint over the sub-agent's
	// final text. (The sub-agent's bespoke loop does not auto-run lints, so we
	// invoke the same registry explicitly — same LintFunc, same LintContext.)
	answer := fmt.Sprint(result)
	violation := e.parent.OutputLints().Run(answer, agent.LintContext{
		AgentName: "Implementer",
		Goal:      goal,
	})

	// Verification done — self-clean the worktree now (we kept it alive so the
	// verifier could inspect the file).
	if rmErr := (&worktree.Worktree{Path: wtPath, RepoDir: e.repoDir}).Remove(e.ctx, true); rmErr != nil {
		fmt.Printf("[executor] worktree cleanup note: %v\n", rmErr)
	}

	if violation != nil {
		fmt.Printf("[verifier] BLOCKED: %s -> %s\n", violation.LintName, violation.Reason)
		return &scheduler.TaskResult{Success: false, Output: "blocked by verifier: " + violation.Reason}, nil
	}
	fmt.Println("[verifier] PASSED: deliverable verified")
	return &scheduler.TaskResult{Success: true, Output: answer}, nil
}

// ============================================================================
// TeamManager human-gate (yield/resume) + checkpoint demonstration.
// ============================================================================

func runManagerGate(ctx context.Context, home string, mock *scriptedLLM) {
	fmt.Println("--- human gate: checkpoint + yield/resume via TeamManager ---")

	cfg := loadConfig(home)
	store, err := agent.NewStore(cfg.AgentDBPath())
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	manager := agent.NewTeamManager(store)
	manager.SetConfig(cfg)
	manager.SetLLM(mock)           // inject scripted brain
	manager.SetDisableMemory(true) // no embedder needed
	if err := manager.SeedDefaultMembers(); err != nil {
		log.Fatalf("seed members: %v", err)
	}
	// A plain worker agent that runs through the normal runtime, so a terminal
	// checkpoint is written (avoids the Dispatcher's short-circuit paths).
	if _, err := manager.CreateAgent(ctx, &agent.AgentModel{
		Name:         "Worker",
		Kind:         agent.AgentKindAgent,
		Description:  "produces a report",
		Instructions: "Produce the requested report as plain text.",
	}); err != nil {
		log.Fatalf("create worker agent: %v", err)
	}

	tasks := manager.Tasks()

	// Submit a task. The manager auto-wires the checkpoint sink, so a terminal
	// checkpoint is persisted once the run finishes.
	task, err := tasks.Submit(ctx, agent.TaskSubmitOptions{
		SessionID: "loop-session",
		AgentName: "Worker",
		Input:     "Give me a one-sentence status update.",
	})
	if err != nil {
		log.Fatalf("submit task: %v", err)
	}
	fmt.Printf("[manager] submitted task %s\n", task.ID[:8])

	first, err := tasks.Await(ctx, task.ID)
	if err != nil {
		fmt.Printf("[manager] await note: %v\n", err)
	}
	if first != nil {
		fmt.Printf("[manager] first pass finished: status=%s output=%q\n", first.Status, strings.TrimSpace(first.Output))
	}

	// CHECKPOINT: inspect what the runtime persisted.
	cps, err := tasks.ListCheckpoints(ctx, task.ID, 8)
	if err != nil {
		fmt.Printf("[manager] list checkpoints note: %v\n", err)
	}
	fmt.Printf("[manager] checkpoints persisted: %d\n", len(cps))
	for _, cp := range cps {
		fmt.Printf("           - seq=%d round=%d agent=%s msgs=%d\n", cp.Seq, cp.Round, cp.AgentName, len(cp.Messages))
	}

	// HUMAN GATE: demonstrate the yield/resume API surface — a human pauses the
	// task and then resumes it with input.
	if _, err := tasks.Yield(ctx, task.ID, agent.TaskYieldOptions{
		Reason: "verifier wants confirmation; waiting for human input",
	}); err != nil {
		fmt.Printf("[manager] yield note: %v\n", err)
	} else {
		fmt.Println("[manager] task YIELDED -> waiting at human gate")
	}
	fmt.Printf("[human] supplying correction: include token %q\n", requiredToken)
	if _, err := tasks.Resume(ctx, task.ID, agent.TaskResumeOptions{
		Input: "Human approved. Include the token " + requiredToken + " and finish.",
	}); err != nil {
		fmt.Printf("[manager] resume note: %v\n", err)
	} else {
		fmt.Println("[manager] task RESUMED with human input")
	}

	// CHECKPOINT REPLAY: re-run from the latest checkpoint, appending the human
	// approval as a follow-up so the resumed pass includes the token.
	if len(cps) > 0 {
		if _, err := tasks.ResumeFromCheckpoint(ctx, task.ID, agent.CheckpointResumeOptions{
			FollowUp: "Human approved. Include the token " + requiredToken + " and finish.",
		}); err != nil {
			fmt.Printf("[manager] resume-from-checkpoint note: %v\n", err)
		} else {
			fmt.Println("[manager] RESUMED FROM CHECKPOINT with human correction")
		}
	}

	final, err := tasks.Await(ctx, task.ID)
	if err != nil {
		fmt.Printf("[manager] final await note: %v\n", err)
	}
	if final != nil {
		fmt.Printf("[manager] task status=%s output=%q\n", final.Status, strings.TrimSpace(final.Output))
	}
}

// ============================================================================
// scriptedLLM: a deterministic, context-aware Generator (offline).
//
// Behavior:
//   - On a tool-enabled turn with no prior tool result, it emits an fs_write
//     tool call. The FIRST write produces the WRONG content (so the verifier
//     blocks); once it sees the human correction token in the conversation it
//     writes the correct content.
//   - After a tool result, it returns a short final text.
// ============================================================================

type scriptedLLM struct{}

func newScriptedLLM() *scriptedLLM { return &scriptedLLM{} }

func (m *scriptedLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "ok", nil
}

func (m *scriptedLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, cb func(string)) error {
	return nil
}

func (m *scriptedLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return m.decide(messages, tools), nil
}

func (m *scriptedLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(m.decide(messages, tools))
}

func (m *scriptedLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Data: map[string]interface{}{}, Raw: "{}", Valid: true}, nil
}

func (m *scriptedLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

func (m *scriptedLLM) decide(messages []domain.Message, tools []domain.ToolDefinition) *domain.GenerationResult {
	hasFSWrite := false
	for _, t := range tools {
		if t.Function.Name == "fs_write" {
			hasFSWrite = true
			break
		}
	}
	// Has the human supplied the correction token anywhere in the convo?
	// Has the implementer ALREADY written via fs_write this run?
	corrected := false
	alreadyWrote := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, requiredToken) {
			corrected = true
		}
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == "fs_write" {
				alreadyWrote = true
			}
		}
	}

	// If we already wrote the file, produce the final answer (no more tools).
	if alreadyWrote {
		if corrected {
			return &domain.GenerationResult{Content: "Deliverable written and verified. " + requiredToken, FinishReason: "stop"}
		}
		return &domain.GenerationResult{Content: "Draft deliverable written.", FinishReason: "stop"}
	}

	// Otherwise, if fs_write is available, write the deliverable once.
	if hasFSWrite {
		content := "draft work in progress" // wrong on the first pass -> verifier blocks
		if corrected {
			content = "final deliverable " + requiredToken
		}
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID:   "call_write_1",
				Type: "function",
				Function: domain.FunctionCall{
					Name:      "fs_write",
					Arguments: map[string]interface{}{"path": deliverableFile, "content": content},
				},
			}},
			FinishReason: "tool_calls",
		}
	}

	// No tools: emit final text. Include the token only after the human gate.
	if corrected {
		return &domain.GenerationResult{Content: "All systems nominal. " + requiredToken, FinishReason: "stop"}
	}
	return &domain.GenerationResult{Content: "All systems nominal.", FinishReason: "stop"}
}

// ============================================================================
// Helpers
// ============================================================================

func loadConfig(home string) *config.Config {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	cfg.Home = home
	cfg.ApplyHomeLayout()
	return cfg
}

// mustInitTempRepo creates a temp git repo with one commit and returns its path
// plus a cleanup function.
func mustInitTempRepo() (string, func()) {
	dir, err := os.MkdirTemp("", "loop-repo-")
	if err != nil {
		log.Fatalf("create temp repo: %v", err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "loop@example.com")
	run("config", "user.name", "Loop Demo")
	run("commit", "--allow-empty", "-m", "initial")
	return dir, func() { _ = os.RemoveAll(dir) }
}

var _ = time.Second // keep time imported for easy provider-swap edits
