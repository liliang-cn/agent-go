// Package runner is the agent-go behavioral eval harness. It loads YAML
// scenarios from eval/scenarios/, runs each one against a Service built
// from the scenario's spec (typically with a scripted mock LLM), and
// asserts on the resulting event stream.
//
// The harness is intentionally simple in this first iteration:
//   - the LLM is mocked with a per-scenario reply script
//   - tool calls are not synthesized — the runner exercises only the
//     "free-form final answer" path of the runtime, which is where the
//     output-lint hook lives
//   - assertions cover final status, final text shape, LLM call count,
//     and per-lint violation counts
//
// A real-LLM mode (--profile=live) is intentionally out of scope for v1;
// add it once the mock-LLM coverage is meaningful.
package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode selects which LLM the runner uses for a scenario.
type Mode string

const (
	// ModeMock drives the run with a scripted MockLLM. Fully deterministic;
	// CI-runnable. The default when scenario.mode is unset.
	ModeMock Mode = "mock"
	// ModeLive uses a real provider (via the caller-supplied LiveBuilder,
	// typically the agentgo global pool). Non-deterministic; intended for
	// `agentgo eval --profile=live` and not run by `make eval`.
	ModeLive Mode = "live"
)

// Scenario is the YAML schema for a single eval case.
type Scenario struct {
	// Name is a stable identifier (must match filename without extension).
	Name string `yaml:"name"`

	// Description is human-only documentation.
	Description string `yaml:"description"`

	// Mode selects mock vs live LLM. Default: mock.
	Mode Mode `yaml:"mode"`

	// Agent is the entry agent name. Defaults to "Responder" when empty.
	Agent string `yaml:"agent"`

	// Input is the user goal handed to RunStream.
	Input string `yaml:"input"`

	// RegisterLints names lints that should be wired into the service before
	// running. Use "default" to wire the full RegisterDefaultOutputLints set.
	// Otherwise the items must match known builtin names.
	RegisterLints []string `yaml:"register_lints"`

	// LLMReplies is the scripted sequence of free-form completions returned
	// by the mock LLM, consumed in order. The last reply is repeated if the
	// runtime asks for more turns than scripted. Required for mock; must be
	// empty for live (the real model decides).
	LLMReplies []string `yaml:"llm_replies"`

	// Runs is the number of times to execute the scenario. >1 is mainly
	// useful in live mode to amortize model non-determinism into a pass
	// rate. Defaults to 1.
	Runs int `yaml:"runs"`

	// Expect describes the post-conditions that must hold for a single run.
	Expect ExpectSpec `yaml:"expect"`
}

// ExpectSpec lists the assertions for a scenario.
type ExpectSpec struct {
	// Status is the expected terminal event type, one of:
	//   "completed"  -> EventTypeComplete
	//   "blocked"    -> EventTypeBlocked
	//   "error"      -> any EventTypeError without a terminal event
	Status string `yaml:"status"`

	// FinalTextMatch is a substring (or regex when prefixed with "re:") that
	// must appear in the final text. Empty means no positive constraint.
	FinalTextMatch string `yaml:"final_text_match"`

	// FinalTextMustNotMatch is a substring (or regex when prefixed with
	// "re:") that must NOT appear in the final text.
	FinalTextMustNotMatch string `yaml:"final_text_must_not_match"`

	// LLMCalls is the expected number of GenerateWithTools/StreamWithTools
	// invocations during the run. Zero means "do not check".
	LLMCalls int `yaml:"llm_calls"`

	// LintViolations records expected lint trips (by name and count). Each
	// entry's count must match exactly. Lints not listed here are expected
	// not to fire. Mostly useful in mock mode where the model is scripted.
	LintViolations []LintViolationExpectation `yaml:"lint_violations"`

	// MaxLintViolations is the live-mode equivalent: a cap on how many
	// times a given lint may fire. Useful when the real model is allowed
	// to self-heal a couple of times but should not loop forever. If
	// LintViolations is non-empty this is ignored.
	MaxLintViolations map[string]int `yaml:"max_lint_violations"`
}

// LintViolationExpectation pins how many times a particular lint should
// have rejected a response during the run.
type LintViolationExpectation struct {
	Lint  string `yaml:"lint"`
	Count int    `yaml:"count"`
}

// LoadScenario parses a YAML file into a Scenario, validating required
// fields and that Name matches the filename.
func LoadScenario(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario %s: %w", path, err)
	}
	var sc Scenario
	if err := yaml.Unmarshal(raw, &sc); err != nil {
		return nil, fmt.Errorf("parse scenario %s: %w", path, err)
	}
	if err := validateScenario(&sc, path); err != nil {
		return nil, err
	}
	return &sc, nil
}

// LoadScenariosFromDir loads every *.yaml file under dir as a scenario.
// Scenarios are returned in lexical order of filename for deterministic
// iteration.
func LoadScenariosFromDir(dir string) ([]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read scenarios dir %s: %w", dir, err)
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	out := make([]*Scenario, 0, len(paths))
	for _, p := range paths {
		sc, err := LoadScenario(p)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

func validateScenario(sc *Scenario, path string) error {
	if strings.TrimSpace(sc.Name) == "" {
		return fmt.Errorf("scenario %s: name is required", path)
	}
	expected := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".yaml"), ".yml")
	if sc.Name != expected {
		return fmt.Errorf("scenario %s: name %q must match filename %q", path, sc.Name, expected)
	}
	if strings.TrimSpace(sc.Input) == "" {
		return fmt.Errorf("scenario %s: input is required", path)
	}
	if sc.Mode == "" {
		sc.Mode = ModeMock
	}
	switch sc.Mode {
	case ModeMock:
		if len(sc.LLMReplies) == 0 {
			return fmt.Errorf("scenario %s: at least one llm_reply is required for mock mode", path)
		}
	case ModeLive:
		if len(sc.LLMReplies) > 0 {
			return fmt.Errorf("scenario %s: llm_replies must be empty for live mode (the real model decides)", path)
		}
	default:
		return fmt.Errorf("scenario %s: mode %q is invalid (allowed: mock | live)", path, sc.Mode)
	}
	if sc.Runs == 0 {
		sc.Runs = 1
	}
	if sc.Runs < 0 {
		return fmt.Errorf("scenario %s: runs must be >= 1", path)
	}
	switch sc.Expect.Status {
	case "completed", "blocked", "error":
	case "":
		return fmt.Errorf("scenario %s: expect.status is required", path)
	default:
		return fmt.Errorf("scenario %s: expect.status %q is invalid (allowed: completed | blocked | error)", path, sc.Expect.Status)
	}
	return nil
}
