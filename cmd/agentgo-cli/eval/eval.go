// Package eval is the CLI adapter for the eval/runner harness. It loads
// scenarios from disk and runs them either in mock mode (deterministic,
// scripted LLM) or live mode (real LLM via the configured provider pool).
package eval

import (
	"context"
	"fmt"
	"path/filepath"

	evalrunner "github.com/liliang-cn/agent-go/v2/eval/runner"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/spf13/cobra"
)

var (
	flagProfile     string
	flagScenarioDir string
	flagResultsDir  string
	flagSaveResults bool
	flagFilter      string
	flagRuns        int
	flagVerbose     bool
)

// Cmd is registered onto the root agentgo command.
var Cmd = &cobra.Command{
	Use:   "eval",
	Short: "Run the behavioral eval harness",
	Long: `Run scenarios from eval/scenarios/ against agent-go.

By default the runner uses a scripted MockLLM (deterministic, fast,
CI-runnable). Pass --profile=live to drive scenarios with the configured
real provider; live scenarios live in eval/scenarios/ alongside mock
ones, distinguished by 'mode: live' in their YAML.

Examples:
  agentgo eval                                # mock-only run
  agentgo eval --profile=live                 # use the real LLM
  agentgo eval --filter=lint_dispatcher       # only matching scenarios
  agentgo eval --save                         # write JSON to eval/results/`,
	RunE: runEval,
}

func init() {
	Cmd.Flags().StringVar(&flagProfile, "profile", "mock", "mock | live | all")
	Cmd.Flags().StringVar(&flagScenarioDir, "dir", "eval/scenarios", "directory of scenario YAML files")
	Cmd.Flags().StringVar(&flagResultsDir, "results-dir", "eval/results", "where to write timestamped JSON when --save is set")
	Cmd.Flags().BoolVar(&flagSaveResults, "save", false, "persist results JSON to --results-dir")
	Cmd.Flags().StringVar(&flagFilter, "filter", "", "substring of scenario name; only matching scenarios run")
	Cmd.Flags().IntVar(&flagRuns, "runs", 0, "override scenario.runs (0 = use scenario value, default 1)")
	Cmd.Flags().BoolVar(&flagVerbose, "verbose", false, "print per-scenario reasons even when passing")
}

func runEval(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	scenarios, err := evalrunner.LoadScenariosFromDir(flagScenarioDir)
	if err != nil {
		return err
	}
	scenarios = filterScenarios(scenarios, flagProfile, flagFilter, flagRuns)
	if len(scenarios) == 0 {
		fmt.Println("(no scenarios matched the given filters)")
		return nil
	}

	opts := evalrunner.RunOptions{}
	if needsLive(scenarios) {
		live, err := buildLiveBuilder()
		if err != nil {
			return fmt.Errorf("live profile requested but provider setup failed: %w", err)
		}
		opts.Live = live
	}

	results := make([]*evalrunner.RunResult, 0, len(scenarios))
	for _, sc := range scenarios {
		res, err := evalrunner.Run(ctx, sc, opts)
		if err != nil {
			return fmt.Errorf("scenario %s: %w", sc.Name, err)
		}
		results = append(results, res)
	}

	fmt.Print(evalrunner.FormatSummary(results))

	if flagVerbose {
		for _, r := range results {
			if r.Pass && len(r.Reasons) > 0 {
				fmt.Printf("\nverbose %s:\n", r.Scenario)
				for _, reason := range r.Reasons {
					fmt.Printf("  - %s\n", reason)
				}
			}
		}
	}

	if flagSaveResults {
		path, err := evalrunner.SaveResults(results, flagProfile, flagResultsDir)
		if err != nil {
			return err
		}
		fmt.Printf("\nresults saved: %s\n", path)
	}

	failed := 0
	for _, r := range results {
		if !r.Pass {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d scenario(s) failed", failed)
	}
	return nil
}

// filterScenarios narrows the loaded set by profile (mock | live | all),
// optional name substring, and optional runs override.
func filterScenarios(in []*evalrunner.Scenario, profile, filter string, runsOverride int) []*evalrunner.Scenario {
	out := make([]*evalrunner.Scenario, 0, len(in))
	for _, sc := range in {
		switch profile {
		case "mock":
			if sc.Mode == evalrunner.ModeLive {
				continue
			}
		case "live":
			if sc.Mode != evalrunner.ModeLive {
				continue
			}
		case "all", "":
			// no filter
		}
		if filter != "" && !contains(sc.Name, filter) {
			continue
		}
		if runsOverride > 0 {
			sc.Runs = runsOverride
		}
		out = append(out, sc)
	}
	return out
}

func needsLive(scenarios []*evalrunner.Scenario) bool {
	for _, sc := range scenarios {
		if sc.Mode == evalrunner.ModeLive {
			return true
		}
	}
	return false
}

// buildLiveBuilder returns a runner.LiveBuilder that constructs each
// scenario's Service via the configured agentgo global pool. The setup
// mirrors what `agentgo chat` does so live-mode runs use whatever the
// user's agentgo.toml / SQLite store says.
func buildLiveBuilder() (evalrunner.LiveBuilder, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("no config found; set up an LLM provider first (`agentgo llm add ...`)")
	}
	if err := services.GetGlobalPoolService().Initialize(context.Background(), cfg); err != nil {
		return nil, fmt.Errorf("initialize provider pool: %w", err)
	}
	llm, err := services.GetGlobalPoolService().GetLLMService()
	if err != nil {
		return nil, fmt.Errorf("get LLM service: %w", err)
	}

	return func(scenarioName, agentName string, lints []string, home string) (*agent.Service, error) {
		evalCfg := &config.Config{
			Home: home,
			LLM:  cfg.LLM,
			RAG:  cfg.RAG,
			Memory: config.MemoryConfig{
				StoreType:  "file",
				MemoryPath: filepath.Join(home, "data", "memories"),
			},
		}
		// Force RAG off in eval — it injects external context that makes
		// behavioral runs noisy and provider-dependent.
		evalCfg.RAG.Enabled = false
		evalCfg.ApplyHomeLayout()

		// Build via TeamManager so live scenarios exercise the full
		// agent ecosystem: route_builtin_request, registered MCP tools,
		// auto-wired output lints (TeamManager calls
		// applyBuiltInOutputLints for Dispatcher/Archivist), and the
		// dispatcher tool catalogue. Standalone agent.New().Build()
		// services miss all of this and only test the LLM-glue layer.
		store, err := agent.NewStore(filepath.Join(home, "data", "agentgo.db"))
		if err != nil {
			return nil, fmt.Errorf("build eval store: %w", err)
		}
		mgr := agent.NewTeamManager(store)
		mgr.SetConfig(evalCfg)
		mgr.SetLLM(llm)
		if err := mgr.SeedDefaultMembers(); err != nil {
			return nil, fmt.Errorf("seed default members: %w", err)
		}
		svc, err := mgr.GetAgentService(agentName)
		if err != nil {
			return nil, fmt.Errorf("get agent service %q: %w", agentName, err)
		}
		// Live runs always wire the default lint set so the agent gets
		// the same harness rules production code does. Scenario authors
		// who need a different set can register on top after the build.
		agent.RegisterDefaultOutputLints(svc)
		return svc, nil
	}, nil
}

func contains(haystack, needle string) bool {
	return needle == "" || index(haystack, needle) >= 0
}

// tiny strings.Index inline to avoid an extra import dependency surface.
func index(s, sub string) int {
	if sub == "" {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
