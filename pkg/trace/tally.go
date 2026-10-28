package trace

import (
	"sync"
	"time"
)

// Tokens is a token split: every prompt token (cached or not), the
// completion, the prompt-cache reads among the prompt, and the prompt tokens
// written to the cache.
type Tokens struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Cached     int `json:"cached,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
}

func (t Tokens) plus(o Tokens) Tokens {
	return Tokens{t.Prompt + o.Prompt, t.Completion + o.Completion, t.Cached + o.Cached, t.CacheWrite + o.CacheWrite}
}

// PurposeSummary is the model calls made for one purpose.
type PurposeSummary struct {
	Calls  int    `json:"calls"`
	Ms     int64  `json:"ms"`
	Tokens Tokens `json:"tokens"`
}

// ToolSummary is the calls of one tool.
type ToolSummary struct {
	Calls    int   `json:"calls"`
	Failures int   `json:"failures,omitempty"`
	Ms       int64 `json:"ms"`
}

// Tally adds up a run as it happens: where the time went, how many model and
// tool calls by purpose and by name, tokens, lint retries and compactions.
// It is the one place this arithmetic lives — the run timeline (Timeline, the
// "trace" event, AGENTGO_TRACE lines) and agent.TraceWriter's run_end line
// both read a Tally, so they cannot disagree about a run.
//
// Safe for concurrent use: tools run in parallel.
type Tally struct {
	mu    sync.Mutex
	start time.Time

	model, memory, tool        time.Duration
	modelCalls, toolCalls      int
	toolFailures               int
	firstModelAt, firstTokenAt time.Duration
	tokens                     Tokens
	purposes                   map[string]*PurposeSummary
	tools                      map[string]*ToolSummary
	lintRetries, lintBlocks    int
	compactions                int
}

// NewTally starts a tally for a run that began at start.
func NewTally(start time.Time) *Tally {
	return &Tally{start: start, purposes: map[string]*PurposeSummary{}, tools: map[string]*ToolSummary{}}
}

// ModelStarted notes that a model call began at at; the first one is the
// run's first_model_call.
func (t *Tally) ModelStarted(at time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.modelCalls == 0 && t.firstModelAt == 0 {
		t.firstModelAt = at.Sub(t.start)
	}
}

// Model adds one finished model call. ttft is its wait for the first output
// when it streamed, else 0; the first answering turn ("turn") that streamed
// sets first_answer_token.
func (t *Tally) Model(purpose string, startedAt time.Time, dur, ttft time.Duration, tok Tokens) {
	if t == nil {
		return
	}
	if purpose == "" {
		purpose = "other"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstModelAt == 0 && t.modelCalls == 0 && !startedAt.IsZero() {
		t.firstModelAt = startedAt.Sub(t.start)
	}
	t.model += dur
	t.modelCalls++
	t.tokens = t.tokens.plus(tok)
	p := t.purposes[purpose]
	if p == nil {
		p = &PurposeSummary{}
		t.purposes[purpose] = p
	}
	p.Calls++
	p.Ms += dur.Milliseconds()
	p.Tokens = p.Tokens.plus(tok)
	if ttft > 0 && t.firstTokenAt == 0 && purpose == "turn" && !startedAt.IsZero() {
		t.firstTokenAt = startedAt.Sub(t.start) + ttft
	}
}

// Tool adds one finished tool call.
func (t *Tally) Tool(name string, dur time.Duration, failed bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tool += dur
	t.toolCalls++
	s := t.tools[name]
	if s == nil {
		s = &ToolSummary{}
		t.tools[name] = s
	}
	s.Calls++
	s.Ms += dur.Milliseconds()
	if failed {
		s.Failures++
		t.toolFailures++
	}
}

// Memory adds one memory step.
func (t *Tally) Memory(dur time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.memory += dur
	t.mu.Unlock()
}

// Lint adds one lint rejection: retried, or the one that blocked the run.
func (t *Tally) Lint(retry bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if retry {
		t.lintRetries++
	} else {
		t.lintBlocks++
	}
	t.mu.Unlock()
}

// Compaction adds one history-folding step.
func (t *Tally) Compaction() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.compactions++
	t.mu.Unlock()
}

// Summary is the tally so far, its wall time measured to now.
func (t *Tally) Summary() Summary { return t.SummaryAt(time.Now()) }

// SummaryAt is the tally with its wall time measured to now.
func (t *Tally) SummaryAt(now time.Time) Summary {
	if t == nil {
		return Summary{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := Summary{
		TotalMs: now.Sub(t.start).Milliseconds(), FirstModelCallMs: t.firstModelAt.Milliseconds(),
		FirstAnswerTokenMs: t.firstTokenAt.Milliseconds(), ModelMs: t.model.Milliseconds(), ModelCalls: t.modelCalls,
		MemoryMs: t.memory.Milliseconds(), ToolMs: t.tool.Milliseconds(), ToolCalls: t.toolCalls,
		ToolFailures: t.toolFailures, LintRetries: t.lintRetries, LintBlocks: t.lintBlocks, Compactions: t.compactions,
	}
	if t.tokens != (Tokens{}) {
		tok := t.tokens
		s.Tokens = &tok
	}
	if len(t.purposes) > 0 {
		s.ByPurpose = make(map[string]PurposeSummary, len(t.purposes))
		for k, v := range t.purposes {
			s.ByPurpose[k] = *v
		}
	}
	if len(t.tools) > 0 {
		s.Tools = make(map[string]ToolSummary, len(t.tools))
		for k, v := range t.tools {
			s.Tools[k] = *v
		}
	}
	return s
}
