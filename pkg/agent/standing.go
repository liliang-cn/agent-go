package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// A standing responsibility: an agent that is never finished.
//
// Everything else in this package has an end. A run ends when the model
// answers; a long run ends when its plan is checked off; a background task
// ends and hands back a result. A responsibility does not end. "Keep the
// support inbox triaged", "watch this repo's CI and tell me when main is
// red", "make sure nothing on the calendar collides" — these stay true, or
// stop being true, and the agent's job is to keep them true for as long as
// the person wants.
//
// The shape here is the one the always-on products converged on. The person
// states what should stay true, what to watch, what is worth telling them,
// and what must never happen. The agent sleeps. It wakes for one of four
// reasons — it asked to (standing_wake_me), a fixed interval came round, the
// host delivered an event (a message, a webhook, a mail), or an idle scan was
// due — runs one ordinary run with that reason in front of it, keeps notes
// for next time, tells the person what they should know, says when to wake
// it again, and sleeps. Each wake is a fresh session over the same task id,
// exactly as RunSegments does, so the plan and the checkpoints are the
// responsibility's and the context never grows across weeks.
//
// There is still one loop. A Standing runs nothing itself: every wake is
// svc.Run, with options. What it owns is the clock, the queue of reasons to
// wake, the notes, and the two ceilings — wakes per day and cost per day —
// that keep an agent that is never finished from spending without end.

// WakeKind says why a responsibility woke.
type WakeKind string

const (
	// WakeSelf: the agent asked to be woken, with standing_wake_me.
	WakeSelf WakeKind = "self"
	// WakeSchedule: Responsibility.Every came round.
	WakeSchedule WakeKind = "schedule"
	// WakeEvent: the host delivered a StandingEvent while nothing was running.
	WakeEvent WakeKind = "event"
	// WakeScan: an idle scan (Responsibility.Scan) was due — reading only.
	WakeScan WakeKind = "scan"
	// WakeHost: the host called WakeNow.
	WakeHost WakeKind = "host"
)

// Responsibility is what the person hands a standing agent. The four text
// fields are the brief; the rest bounds what carrying it out may cost.
type Responsibility struct {
	// ID names the responsibility and is the task id of every wake. Empty on
	// Add mints one.
	ID string `json:"id"`
	// Name is for the host's list.
	Name string `json:"name,omitempty"`
	// Goal is what should stay true.
	Goal string `json:"goal"`
	// Watch names the sources to keep an eye on, in the person's words.
	Watch []string `json:"watch,omitempty"`
	// Attention says what is worth telling the person about.
	Attention string `json:"attention,omitempty"`
	// Never lists what the agent must not do. It reaches the model as text
	// and, through ToolDenylist, the runtime — a forbidden tool is withheld,
	// not argued about.
	Never        []string `json:"never,omitempty"`
	ToolDenylist []string `json:"tool_denylist,omitempty"`

	// Every is a fixed wake interval. Zero means no fixed schedule: the agent
	// wakes only when it asks to, when an event arrives, or for a scan.
	Every time.Duration `json:"every,omitempty"`
	// Scan, when set, wakes the agent between assignments to look — with
	// reading tools only — for anything the person should know.
	Scan *ScanPolicy `json:"scan,omitempty"`

	// MaxWakesPerDay bounds how often it may run. Zero means
	// DefaultMaxWakesPerDay. A responsibility past its cap is paused until
	// the next day, and the host is told.
	MaxWakesPerDay int `json:"max_wakes_per_day,omitempty"`
	// MaxCostPerDayUSD bounds what it may spend; zero means no money ceiling
	// (the wake ceiling still holds). A wake whose model cannot be priced
	// counts toward wakes and not toward cost, and the status says so.
	MaxCostPerDayUSD float64 `json:"max_cost_per_day_usd,omitempty"`
	// MaxRoundsPerWake is the round budget of one wake. Zero means
	// DefaultStandingRounds.
	MaxRoundsPerWake int `json:"max_rounds_per_wake,omitempty"`

	// Paused stops wakes without forgetting anything.
	Paused bool `json:"paused,omitempty"`
	// PausedReason says why, when the Standing paused it rather than the host.
	PausedReason string `json:"paused_reason,omitempty"`

	// Notes are the agent's own, kept across wakes with standing_note. They
	// are the whole of what one wake hands the next, so the brief tells the
	// agent to write down what it would otherwise have to rediscover.
	Notes string `json:"notes,omitempty"`
	// NextWake is when the agent last asked to be woken. Zero means it did
	// not ask.
	NextWake time.Time `json:"next_wake,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ScanPolicy describes idle scanning.
type ScanPolicy struct {
	// Every is how often to look when nothing else woke the agent.
	Every time.Duration `json:"every"`
	// MaxRounds bounds one scan; zero means DefaultScanRounds. Scans are
	// cheap by construction — reading tools only — but a scan that reads
	// everything every time is not.
	MaxRounds int `json:"max_rounds,omitempty"`
}

const (
	DefaultMaxWakesPerDay = 24
	DefaultStandingRounds = 12
	DefaultScanRounds     = 6
)

// StandingEvent is something the host learned that a responsibility should
// hear about: a message for the agent, a webhook, a mail, a build result.
type StandingEvent struct {
	// Source names where it came from ("hive", "github", "mail").
	Source string `json:"source"`
	// Kind is the source's own event name ("message", "pull_request").
	Kind string `json:"kind,omitempty"`
	// Payload is the event as the model should read it.
	Payload string    `json:"payload"`
	At      time.Time `json:"at"`
}

// Notification is what a responsibility tells the host, who delivers it to
// the person on whatever channel they use.
type Notification struct {
	ResponsibilityID string `json:"responsibility_id"`
	WakeID           string `json:"wake_id,omitempty"`
	// Kind is what this is:
	//   message      — the agent called standing_notify; Message is for the person
	//   paused       — the day's ceiling was reached; Message says which
	//   error        — a wake failed or was blocked; Message is the reason
	//   wake_started — a wake began; Wake is its record so far
	//   wake_ended   — a wake finished; Wake carries cost, tool calls, error
	// The last two are for a host showing "running / last ran / cost" and
	// keeping a history; they are not for the person.
	Kind    string    `json:"kind"`
	Message string    `json:"message,omitempty"`
	Wake    *Wake     `json:"wake,omitempty"`
	At      time.Time `json:"at"`
}

// Notification kinds.
const (
	NotifyMessage     = "message"
	NotifyPaused      = "paused"
	NotifyError       = "error"
	NotifyWakeStarted = "wake_started"
	NotifyWakeEnded   = "wake_ended"
)

// Notifier receives Notifications. The host implements it; without one,
// notifications are logged and lost.
type Notifier interface {
	Notify(ctx context.Context, n Notification)
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(ctx context.Context, n Notification)

func (f NotifierFunc) Notify(ctx context.Context, n Notification) { f(ctx, n) }

// Wake is one run of a responsibility.
type Wake struct {
	ID               string         `json:"id"`
	ResponsibilityID string         `json:"responsibility_id"`
	Kind             WakeKind       `json:"kind"`
	Reason           string         `json:"reason,omitempty"`
	Event            *StandingEvent `json:"event,omitempty"`
	SessionID        string         `json:"session_id"`
	RunID            string         `json:"run_id"`
	StartedAt        time.Time      `json:"started_at"`
	EndedAt          time.Time      `json:"ended_at,omitempty"`
	CostUSD          float64        `json:"cost_usd"`
	Unpriced         bool           `json:"unpriced,omitempty"`
	ToolCalls        int            `json:"tool_calls"`
	Error            string         `json:"error,omitempty"`
	Notified         int            `json:"notified"`
}

// ResponsibilityStatus is one responsibility as the host sees it.
type ResponsibilityStatus struct {
	Responsibility Responsibility `json:"responsibility"`
	Running        *Wake          `json:"running,omitempty"`
	LastWake       *Wake          `json:"last_wake,omitempty"`
	// NextDue is the earliest of the self-wake, the schedule and the scan.
	NextDue      time.Time `json:"next_due,omitempty"`
	NextDueKind  WakeKind  `json:"next_due_kind,omitempty"`
	WakesToday   int       `json:"wakes_today"`
	CostTodayUSD float64   `json:"cost_today_usd"`
	// UnpricedToday counts wakes whose cost could not be known; the money
	// ceiling cannot see them.
	UnpricedToday int `json:"unpriced_today"`
}

// ErrNoResponsibility is returned for an id the Standing does not hold.
var ErrNoResponsibility = errors.New("agent: no such responsibility")

// ErrStandingClosed is returned after Close.
var ErrStandingClosed = errors.New("agent: standing is closed")

type standingItem struct {
	r        Responsibility
	running  *Wake
	last     *Wake
	nextTick time.Time // next schedule wake
	nextScan time.Time
	day      string
	wakes    int
	cost     float64
	unpriced int
	// pendingEvents arrived while a wake was starting and could not be
	// steered; the next wake carries them.
	pendingEvents []StandingEvent
}

// Standing holds a service's responsibilities and runs their wakes.
type Standing struct {
	svc      *Service
	store    StandingStore
	notifier Notifier
	clock    func() time.Time

	mu     sync.Mutex
	items  map[string]*standingItem
	closed bool

	ctx    context.Context
	cancel context.CancelFunc
	kick   chan struct{}
	wg     sync.WaitGroup
}

// StandingOption configures NewStanding.
type StandingOption func(*Standing)

// WithStandingStore sets where responsibilities persist. Without it, a
// service with a database keeps them in a table of its own (they survive a
// restart); a service without one keeps them in memory.
func WithStandingStore(s StandingStore) StandingOption {
	return func(st *Standing) { st.store = s }
}

// WithNotifier sets who hears what a responsibility has to say.
func WithNotifier(n Notifier) StandingOption {
	return func(st *Standing) { st.notifier = n }
}

// NewStanding builds the standing half of a service: it registers the three
// tools a wake uses (standing_notify, standing_note, standing_wake_me), loads
// what the store holds, and starts the clock. Close it before the service.
func NewStanding(svc *Service, opts ...StandingOption) (*Standing, error) {
	if svc == nil {
		return nil, errors.New("agent: NewStanding needs a service")
	}
	st := &Standing{
		svc:   svc,
		clock: time.Now,
		items: map[string]*standingItem{},
		kick:  make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(st)
	}
	if st.store == nil {
		if db := svc.store.DB(); db != nil {
			s, err := NewSQLiteStandingStore(db)
			if err != nil {
				return nil, err
			}
			st.store = s
		} else {
			st.store = NewMemoryStandingStore()
		}
	}
	st.ctx, st.cancel = context.WithCancel(context.Background())
	st.registerTools()

	loaded, err := st.store.Load(st.ctx)
	if err != nil {
		st.cancel()
		return nil, fmt.Errorf("agent: load responsibilities: %w", err)
	}
	now := st.clock()
	for _, r := range loaded {
		it := &standingItem{r: r}
		st.scheduleLocked(it, now)
		st.items[r.ID] = it
	}
	st.wg.Add(1)
	go st.loop()
	return st, nil
}

// Add registers a responsibility, persists it, and schedules its first wake.
// A fresh responsibility with a schedule wakes immediately, so the person
// sees it working rather than waiting out the first interval.
func (st *Standing) Add(ctx context.Context, r Responsibility) (Responsibility, error) {
	if strings.TrimSpace(r.Goal) == "" {
		return r, errors.New("agent: a responsibility needs a goal")
	}
	if r.Every < 0 || (r.Scan != nil && r.Scan.Every <= 0) {
		return r, errors.New("agent: intervals must be positive")
	}
	now := st.clock()
	if strings.TrimSpace(r.ID) == "" {
		r.ID = uuid.NewString()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	if err := st.store.Save(ctx, r); err != nil {
		return r, err
	}
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return r, ErrStandingClosed
	}
	it := &standingItem{r: r}
	if r.Every > 0 && !r.Paused {
		it.nextTick = now // first wake now
	}
	if r.Scan != nil {
		it.nextScan = now.Add(r.Scan.Every)
	}
	st.items[r.ID] = it
	st.mu.Unlock()
	st.poke()
	return r, nil
}

// Remove forgets a responsibility. A wake in flight is cancelled.
func (st *Standing) Remove(ctx context.Context, id string) error {
	st.mu.Lock()
	it, ok := st.items[id]
	if ok {
		delete(st.items, id)
	}
	st.mu.Unlock()
	if !ok {
		return ErrNoResponsibility
	}
	if it.running != nil {
		st.svc.CancelRun(it.running.RunID)
	}
	return st.store.Delete(ctx, id)
}

// Pause stops wakes; Resume lets them continue. Both persist.
func (st *Standing) Pause(ctx context.Context, id, reason string) error {
	return st.update(ctx, id, func(it *standingItem) {
		it.r.Paused, it.r.PausedReason = true, reason
	})
}

func (st *Standing) Resume(ctx context.Context, id string) error {
	err := st.update(ctx, id, func(it *standingItem) {
		it.r.Paused, it.r.PausedReason = false, ""
		if it.r.Every > 0 && it.nextTick.IsZero() {
			it.nextTick = st.clock()
		}
	})
	st.poke()
	return err
}

// Deliver hands an event to a responsibility. A wake in flight has it
// steered into its conversation (steered is true); otherwise a wake starts
// with the event as its reason. An event is never dropped: one that arrives
// while a wake is starting rides on the next.
func (st *Standing) Deliver(ctx context.Context, id string, ev StandingEvent) (steered bool, err error) {
	if ev.At.IsZero() {
		ev.At = st.clock()
	}
	st.mu.Lock()
	it, ok := st.items[id]
	if !ok {
		st.mu.Unlock()
		return false, ErrNoResponsibility
	}
	if st.closed {
		st.mu.Unlock()
		return false, ErrStandingClosed
	}
	if it.running != nil {
		session := it.running.SessionID
		st.mu.Unlock()
		if st.svc.Steer(session, domain.Message{Content: renderEvent(ev)}) {
			return true, nil
		}
		// The run ended between the check and the steer; fall through to a
		// fresh wake with the event.
		st.mu.Lock()
	}
	it.pendingEvents = append(it.pendingEvents, ev)
	st.mu.Unlock()
	st.startWake(id, WakeEvent, "an event arrived")
	return false, nil
}

// WakeNow wakes a responsibility at once, with a reason the model reads.
func (st *Standing) WakeNow(ctx context.Context, id, reason string) error {
	st.mu.Lock()
	_, ok := st.items[id]
	st.mu.Unlock()
	if !ok {
		return ErrNoResponsibility
	}
	return st.startWake(id, WakeHost, reason)
}

// Get returns one responsibility's status.
func (st *Standing) Get(id string) (ResponsibilityStatus, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	it, ok := st.items[id]
	if !ok {
		return ResponsibilityStatus{}, false
	}
	return st.statusLocked(it), true
}

// Status lists every responsibility, by name then id.
func (st *Standing) Status() []ResponsibilityStatus {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]ResponsibilityStatus, 0, len(st.items))
	for _, it := range st.items {
		out = append(out, st.statusLocked(it))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Responsibility, out[j].Responsibility
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return out
}

// Close stops the clock and cancels wakes in flight. Responsibilities stay
// in the store; a new Standing over the same store picks them up.
func (st *Standing) Close() {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	st.closed = true
	st.mu.Unlock()
	st.cancel()
	st.wg.Wait()
}

func (st *Standing) statusLocked(it *standingItem) ResponsibilityStatus {
	st.rollDayLocked(it)
	s := ResponsibilityStatus{
		Responsibility: it.r,
		WakesToday:     it.wakes,
		CostTodayUSD:   it.cost,
		UnpricedToday:  it.unpriced,
	}
	if it.running != nil {
		w := *it.running
		s.Running = &w
	}
	if it.last != nil {
		w := *it.last
		s.LastWake = &w
	}
	s.NextDue, s.NextDueKind = st.nextDueLocked(it)
	return s
}

func (st *Standing) update(ctx context.Context, id string, f func(*standingItem)) error {
	st.mu.Lock()
	it, ok := st.items[id]
	if !ok {
		st.mu.Unlock()
		return ErrNoResponsibility
	}
	f(it)
	it.r.UpdatedAt = st.clock()
	r := it.r
	st.mu.Unlock()
	return st.store.Save(ctx, r)
}

func (st *Standing) poke() {
	select {
	case st.kick <- struct{}{}:
	default:
	}
}

// scheduleLocked sets the clocks of an item loaded from the store, as if it
// had just finished a wake.
func (st *Standing) scheduleLocked(it *standingItem, now time.Time) {
	if it.r.Every > 0 && !it.r.Paused {
		it.nextTick = now.Add(it.r.Every)
	}
	if it.r.Scan != nil {
		it.nextScan = now.Add(it.r.Scan.Every)
	}
}

// nextDueLocked is the earliest reason this item will wake on its own.
func (st *Standing) nextDueLocked(it *standingItem) (time.Time, WakeKind) {
	if it.r.Paused || it.running != nil {
		return time.Time{}, ""
	}
	var due time.Time
	var kind WakeKind
	consider := func(t time.Time, k WakeKind) {
		if t.IsZero() {
			return
		}
		if due.IsZero() || t.Before(due) {
			due, kind = t, k
		}
	}
	consider(it.r.NextWake, WakeSelf)
	consider(it.nextTick, WakeSchedule)
	consider(it.nextScan, WakeScan)
	return due, kind
}

// loop is the clock: it sleeps until the earliest due wake, starts what is
// due, and sleeps again. Add, Resume and a self-wake poke it so a new,
// earlier time is noticed.
func (st *Standing) loop() {
	defer st.wg.Done()
	for {
		st.mu.Lock()
		now := st.clock()
		var soonest time.Time
		var due []struct {
			id   string
			kind WakeKind
		}
		for id, it := range st.items {
			t, k := st.nextDueLocked(it)
			if t.IsZero() {
				continue
			}
			if !t.After(now) {
				due = append(due, struct {
					id   string
					kind WakeKind
				}{id, k})
				continue
			}
			if soonest.IsZero() || t.Before(soonest) {
				soonest = t
			}
		}
		st.mu.Unlock()

		for _, d := range due {
			reason := map[WakeKind]string{
				WakeSelf:     "you asked to be woken now",
				WakeSchedule: "the scheduled check is due",
				WakeScan:     "idle scan: look for anything worth knowing",
			}[d.kind]
			_ = st.startWake(d.id, d.kind, reason)
		}

		wait := time.Hour
		if !soonest.IsZero() {
			if d := soonest.Sub(st.clock()); d < wait {
				wait = d
			}
		}
		if wait < 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-st.ctx.Done():
			timer.Stop()
			return
		case <-st.kick:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (st *Standing) rollDayLocked(it *standingItem) {
	day := st.clock().Format("2006-01-02")
	if it.day != day {
		it.day, it.wakes, it.cost, it.unpriced = day, 0, 0, 0
		if it.r.Paused && it.r.PausedReason == pausedDailyLimit {
			it.r.Paused, it.r.PausedReason = false, ""
		}
	}
}

const pausedDailyLimit = "daily limit reached"

// startWake begins one run for a responsibility, unless one is already in
// flight (the reason then waits for it) or the day's ceiling is reached (the
// responsibility is paused until tomorrow and the host is told).
func (st *Standing) startWake(id string, kind WakeKind, reason string) error {
	st.mu.Lock()
	it, ok := st.items[id]
	if !ok {
		st.mu.Unlock()
		return ErrNoResponsibility
	}
	if st.closed {
		st.mu.Unlock()
		return ErrStandingClosed
	}
	if it.running != nil {
		st.mu.Unlock()
		return nil
	}
	st.rollDayLocked(it)
	if it.r.Paused {
		st.mu.Unlock()
		return nil
	}
	maxWakes := it.r.MaxWakesPerDay
	if maxWakes <= 0 {
		maxWakes = DefaultMaxWakesPerDay
	}
	if it.wakes >= maxWakes || (it.r.MaxCostPerDayUSD > 0 && it.cost >= it.r.MaxCostPerDayUSD) {
		it.r.Paused, it.r.PausedReason = true, pausedDailyLimit
		it.r.NextWake = time.Time{}
		r := it.r
		why := fmt.Sprintf("%s: %d wakes and $%.4f today (limits %d wakes, $%.2f)", pausedDailyLimit, it.wakes, it.cost, maxWakes, it.r.MaxCostPerDayUSD)
		st.mu.Unlock()
		_ = st.store.Save(st.ctx, r)
		st.notify(Notification{ResponsibilityID: id, Kind: NotifyPaused, Message: why, At: st.clock()})
		return nil
	}

	events := it.pendingEvents
	it.pendingEvents = nil
	var ev *StandingEvent
	if len(events) > 0 {
		e := events[0]
		ev = &e
	}
	now := st.clock()
	w := &Wake{
		ID: uuid.NewString(), ResponsibilityID: id, Kind: kind, Reason: reason, Event: ev,
		SessionID: uuid.NewString(), RunID: uuid.NewString(), StartedAt: now,
	}
	it.running = w
	it.wakes++
	// The clocks that fired are consumed; the ones that did not stay.
	switch kind {
	case WakeSelf:
		it.r.NextWake = time.Time{}
	case WakeSchedule:
		it.nextTick = now.Add(it.r.Every)
	case WakeScan:
		it.nextScan = now.Add(it.r.Scan.Every)
	}
	r := it.r
	st.mu.Unlock()

	prompt := renderWake(r, w, events)
	opts := st.wakeOptions(r, w)
	started := *w
	st.notify(Notification{ResponsibilityID: id, WakeID: w.ID, Kind: NotifyWakeStarted, Wake: &started, At: now})

	st.wg.Add(1)
	go func() {
		defer st.wg.Done()
		res, err := st.svc.Run(st.ctx, prompt, opts...)
		st.finishWake(id, w, res, err)
	}()
	return nil
}

func (st *Standing) wakeOptions(r Responsibility, w *Wake) []RunOption {
	rounds := r.MaxRoundsPerWake
	if rounds <= 0 {
		rounds = DefaultStandingRounds
	}
	opts := []RunOption{
		WithTaskID(r.ID),
		WithSessionID(w.SessionID),
		WithRunID(w.RunID),
		WithTenant("standing:" + r.ID),
		WithMaxTurns(rounds),
		// The brief is explicit about what the run must and must not do;
		// the extraction pass would only re-derive it.
		WithConstraintExtraction(false),
	}
	if r.MaxCostPerDayUSD > 0 {
		opts = append(opts, WithMaxBudgetUSD(r.MaxCostPerDayUSD))
	}
	if w.Kind == WakeScan {
		if r.Scan != nil && r.Scan.MaxRounds > 0 {
			opts = append(opts, WithMaxTurns(r.Scan.MaxRounds))
		} else {
			opts = append(opts, WithMaxTurns(DefaultScanRounds))
		}
		// Reading only: the allowlist is every tool that declared itself
		// ReadOnly, plus the three standing tools. A scan cannot change
		// anything, whatever it finds.
		opts = append(opts, WithToolAllowlist(st.readOnlyTools()))
	}
	if len(r.ToolDenylist) > 0 {
		opts = append(opts, WithToolDenylist(r.ToolDenylist))
	}
	return opts
}

func (st *Standing) finishWake(id string, w *Wake, res *ExecutionResult, err error) {
	w.EndedAt = st.clock()
	if err != nil {
		w.Error = err.Error()
	}
	if res != nil {
		w.CostUSD, w.Unpriced = res.EstimatedCostUSD, res.CostUnpriced
		w.ToolCalls = res.ToolCalls
		if res.Blocked && w.Error == "" {
			w.Error = "blocked: " + strings.TrimSpace(res.Text())
		}
	}
	st.mu.Lock()
	it, ok := st.items[id]
	if ok {
		it.running = nil
		it.last = w
		st.rollDayLocked(it)
		it.cost += w.CostUSD
		if w.Unpriced {
			it.unpriced++
		}
		// A wake that could not end cleanly did not get to say when to wake
		// again; the schedule, if any, still will.
	}
	var r Responsibility
	if ok {
		r = it.r
	}
	st.mu.Unlock()
	if ok {
		_ = st.store.Save(st.ctx, r)
	}
	if w.Error != "" {
		st.notify(Notification{ResponsibilityID: id, WakeID: w.ID, Kind: NotifyError, Message: w.Error, At: w.EndedAt})
	}
	ended := *w
	st.notify(Notification{ResponsibilityID: id, WakeID: w.ID, Kind: NotifyWakeEnded, Wake: &ended, At: w.EndedAt})
	st.poke()
}

func (st *Standing) notify(n Notification) {
	if st.notifier == nil {
		agentgolog.WithModule("agent.standing").Info("notification with no notifier", "responsibility", n.ResponsibilityID, "kind", n.Kind, "message", n.Message)
		return
	}
	st.notifier.Notify(st.ctx, n)
}

// renderEvent is how an event reads inside a conversation.
func renderEvent(ev StandingEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[event from %s", ev.Source)
	if ev.Kind != "" {
		fmt.Fprintf(&b, " · %s", ev.Kind)
	}
	if !ev.At.IsZero() {
		fmt.Fprintf(&b, " · %s", ev.At.Format(time.RFC3339))
	}
	b.WriteString("]\n")
	b.WriteString(strings.TrimSpace(ev.Payload))
	return b.String()
}

// renderWake is the brief one wake opens with. Its sections are the four
// questions a responsibility answers, the agent's own notes, and why it is
// awake now; the closing instructions are what to do before sleeping.
func renderWake(r Responsibility, w *Wake, events []StandingEvent) string {
	var b strings.Builder
	b.WriteString("You hold a standing responsibility. This is one of its wakes: do what this wake needs, then stop.\n\n")
	fmt.Fprintf(&b, "WHAT SHOULD STAY TRUE\n%s\n\n", strings.TrimSpace(r.Goal))
	if len(r.Watch) > 0 {
		b.WriteString("WHAT TO WATCH\n")
		for _, s := range r.Watch {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(s))
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(r.Attention) != "" {
		fmt.Fprintf(&b, "WHEN TO TELL THE PERSON\n%s\n\n", strings.TrimSpace(r.Attention))
	}
	if len(r.Never) > 0 {
		b.WriteString("NEVER\n")
		for _, s := range r.Never {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(s))
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(r.Notes) != "" {
		fmt.Fprintf(&b, "YOUR NOTES FROM EARLIER WAKES\n%s\n\n", strings.TrimSpace(r.Notes))
	}
	fmt.Fprintf(&b, "WHY YOU ARE AWAKE NOW (%s)\n%s\n", w.Kind, strings.TrimSpace(w.Reason))
	for _, ev := range events {
		b.WriteString("\n")
		b.WriteString(renderEvent(ev))
		b.WriteString("\n")
	}
	b.WriteString("\nBEFORE YOU STOP\n")
	if w.Kind == WakeScan {
		b.WriteString("- This is a scan: look, do not act. Only reading tools are available.\n")
	}
	b.WriteString("- If the person should know something, call standing_notify with the message. Do not notify about nothing.\n")
	b.WriteString("- Call standing_note with what the next wake must know and would otherwise have to rediscover: what you checked, what you found, what you decided. It replaces the notes above, so carry forward what still matters.\n")
	b.WriteString("- Call standing_wake_me to say when to check again, unless a fixed schedule or an event will bring you back soon enough.\n")
	b.WriteString("- Then answer with one or two lines on what this wake did. The person does not read it; it is the record.\n")
	return b.String()
}
