// Package bashguard refuses destructive shell commands before they run.
//
// It is a ToolCallFilter over the shell tools (bash, shell_start,
// shell_send). The command the model wrote is split into simple commands
// across &&, ||, ;, |, newlines and $( ) / backtick / <( ) substitutions;
// wrappers such as sudo, env VAR=x and command are stripped; ~, $HOME and
// ${HOME} are read as the same directory; quotes are removed. Each named rule
// then looks at one simple command, or at a pipeline for rules about how
// commands are joined (a download piped into a shell).
//
// A matched command is refused and the model reads the rule's reason as the
// tool's error, so it can choose another way. A host that wants a person in
// the loop supplies an Approver instead: flagged commands go to it, and only
// a refusal (or an approver that fails) blocks.
//
// It reads the command the model wrote — the output side — and never the
// user's request. Whether a command is what the user wanted depends on the
// request (dropping a database the user asked to drop is fine), and a guard
// that looked at the wording of the request to decide would be a phrase
// table. So the default set is only commands that are destructive whatever
// was asked: deleting /, wiping a disk, piping a download into a shell.
package bashguard

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// Finding is one rule that matched one command or pipeline.
type Finding struct {
	Rule    string `json:"rule"`
	Reason  string `json:"reason"`
	Command string `json:"command"` // the normalised simple command or pipeline
}

// ApprovalRequest is what an Approver is asked about.
type ApprovalRequest struct {
	Tool      string
	Command   string // the command as the model wrote it
	Findings  []Finding
	SessionID string
	AgentID   string
}

// Approver decides whether a flagged command may run anyway. Returning
// (false, nil) refuses it; an error refuses it too, because a command nobody
// could approve is one nobody approved.
type Approver func(ctx context.Context, req ApprovalRequest) (bool, error)

// Allow exempts commands from a rule. Match is tested against the normalised
// text of the simple command (or pipeline) that fired — Finding.Command.
// An empty Rule exempts the match from every rule.
type Allow struct {
	Rule  string
	Match *regexp.Regexp
}

// DefaultTools maps each shell tool to the argument that carries its command.
func DefaultTools() map[string]string {
	return map[string]string{"bash": "command", "shell_start": "command", "shell_send": "input"}
}

// Option configures the extension.
type Option func(*Extension)

// WithRules replaces the rule set. Default: DefaultRules().
func WithRules(rules ...Rule) Option {
	return func(e *Extension) { e.rules = append([]Rule(nil), rules...) }
}

// WithExtraRules adds rules to the set.
func WithExtraRules(rules ...Rule) Option {
	return func(e *Extension) { e.rules = append(e.rules, rules...) }
}

// WithoutRules removes rules by name.
func WithoutRules(names ...string) Option {
	return func(e *Extension) { e.rules = dropRules(e.rules, names) }
}

// WithAllow adds allowlist entries.
func WithAllow(entries ...Allow) Option {
	return func(e *Extension) { e.allow = append(e.allow, entries...) }
}

// WithTools replaces the tool set: tool name → name of the argument holding
// the command. Default: DefaultTools().
func WithTools(tools map[string]string) Option {
	return func(e *Extension) {
		e.tools = map[string]string{}
		for k, v := range tools {
			e.tools[k] = v
		}
	}
}

// WithTool adds one tool to the set.
func WithTool(name, arg string) Option {
	return func(e *Extension) { e.tools[name] = arg }
}

// WithApprover sends flagged commands to fn instead of blocking them.
func WithApprover(fn Approver) Option {
	return func(e *Extension) { e.approver = fn }
}

// Extension implements agent.Extension and agent.ToolCallFilter. It is safe
// for concurrent use; rules and allowlist may be changed while runs are in
// flight.
type Extension struct {
	mu       sync.RWMutex
	rules    []Rule
	allow    []Allow
	tools    map[string]string
	approver Approver

	statsMu sync.Mutex
	stats   map[string]int
}

// New returns the extension with the default rules and tools, then opts.
func New(opts ...Option) *Extension {
	e := &Extension{
		rules: DefaultRules(),
		tools: DefaultTools(),
		stats: map[string]int{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Name implements agent.Extension.
func (e *Extension) Name() string { return "bashguard" }

// AddRule adds a rule, replacing one with the same name.
func (e *Extension) AddRule(r Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append(dropRules(e.rules, []string{r.Name}), r)
}

// RemoveRule removes a rule by name.
func (e *Extension) RemoveRule(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = dropRules(e.rules, []string{name})
}

// AddAllow adds an allowlist entry.
func (e *Extension) AddAllow(a Allow) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.allow = append(e.allow, a)
}

// Rules returns the names of the active rules.
func (e *Extension) Rules() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, len(e.rules))
	for i, r := range e.rules {
		out[i] = r.Name
	}
	return out
}

// Stats reports how many commands each rule has flagged since construction.
func (e *Extension) Stats() map[string]int {
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	out := make(map[string]int, len(e.stats))
	for k, v := range e.stats {
		out[k] = v
	}
	return out
}

func dropRules(rules []Rule, names []string) []Rule {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	out := rules[:0:0]
	for _, r := range rules {
		if !drop[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

// Check returns every rule the command line trips, after the allowlist.
func (e *Extension) Check(command string) []Finding {
	e.mu.RLock()
	rules, allow := e.rules, e.allow
	e.mu.RUnlock()

	script := Parse(command)
	var out []Finding
	seen := map[string]bool{}
	add := func(r Rule, text string) {
		key := r.Name + "\x00" + text
		if seen[key] || allowed(allow, r.Name, text) {
			return
		}
		seen[key] = true
		out = append(out, Finding{Rule: r.Name, Reason: r.Reason, Command: text})
	}
	for _, p := range script.Pipelines {
		for _, r := range rules {
			if r.Pipeline != nil && r.Pipeline(p, script) {
				add(r, p.Text)
			}
			if r.Command != nil {
				for _, c := range p.Commands {
					if r.Command(c) {
						add(r, c.Text)
					}
				}
			}
		}
	}
	return out
}

func allowed(allow []Allow, rule, text string) bool {
	for _, a := range allow {
		if (a.Rule == "" || a.Rule == rule) && a.Match != nil && a.Match.MatchString(text) {
			return true
		}
	}
	return false
}

// BeforeTool implements agent.ToolCallFilter.
func (e *Extension) BeforeTool(ctx context.Context, call agent.ToolCallInfo) (agent.ToolVerdict, error) {
	e.mu.RLock()
	arg, ok := e.tools[call.Name]
	approver := e.approver
	e.mu.RUnlock()
	if !ok {
		return agent.ToolVerdict{}, nil
	}
	raw, present := call.Args[arg]
	if !present || raw == nil {
		return agent.ToolVerdict{}, nil
	}
	command, isString := raw.(string)
	if !isString {
		// Fail closed: a command we could not read is not one we let run.
		return agent.ToolVerdict{Block: fmt.Sprintf("bashguard: argument %q is not a string", arg)}, nil
	}
	findings := e.Check(command)
	if len(findings) == 0 {
		return agent.ToolVerdict{}, nil
	}
	e.statsMu.Lock()
	for _, f := range findings {
		e.stats[f.Rule]++
	}
	e.statsMu.Unlock()

	if approver != nil {
		ok, err := approver(ctx, ApprovalRequest{
			Tool: call.Name, Command: command, Findings: findings,
			SessionID: call.SessionID, AgentID: call.AgentID,
		})
		if err == nil && ok {
			return agent.ToolVerdict{}, nil
		}
		if err != nil {
			return agent.ToolVerdict{Block: describe(findings) + " Approval could not be obtained, so it was not run."}, nil
		}
		return agent.ToolVerdict{Block: describe(findings) + " The user declined to run it."}, nil
	}
	return agent.ToolVerdict{Block: describe(findings) + " Do not retry it in another form; if it is really needed, ask the user to run it themselves."}, nil
}

func describe(fs []Finding) string {
	var b strings.Builder
	b.WriteString("This command was refused: ")
	for i, f := range fs {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "`%s` — %s", f.Command, f.Reason)
	}
	b.WriteString(".")
	return b.String()
}
