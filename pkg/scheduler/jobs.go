package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Job is a Go function executed on a recurring schedule. Lightweight
// alternative to the database-backed Task/Executor model in this same
// package — use Job when the work is "call this Go function on this
// cadence" and you don't need cross-process persistence, retries, or
// task history.
//
// The Handler receives a derived context that cancels when the runner
// stops. Long-running handlers should respect ctx.Done.
type Job struct {
	Name    string                          // human-readable, surfaced in logs
	Spec    string                          // standard cron expression (5 fields: "min hr dom mon dow"); use DailyAt / WeeklyAt etc. for ergonomic helpers
	Handler func(ctx context.Context) error // function to invoke when the schedule fires
	// OnError, if non-nil, receives the error returned by Handler.
	// Default behavior (nil) logs the error via the standard logger.
	OnError func(name string, err error)
}

// JobRunner schedules in-process Go functions on cron expressions.
// Backed by robfig/cron/v3 — the parser shared with the rest of this
// package, kept consistent so a Spec valid here is valid for storage
// records too.
//
// Safe to add jobs before OR after Start; jobs added after start fire
// at their next due time.
type JobRunner struct {
	mu     sync.Mutex
	cron   *cron.Cron
	logger func(format string, args ...any)
	jobs   []registered

	started  bool
	stopped  bool
	stopOnce sync.Once
	ctx      context.Context
	cancel   context.CancelFunc
}

type registered struct {
	id  cron.EntryID
	job *Job
}

// NewJobRunner builds an in-process cron runner. Use NewJobRunnerWithLocation
// when you need a non-local timezone.
func NewJobRunner() *JobRunner {
	return NewJobRunnerWithLocation(time.Local)
}

// NewJobRunnerWithLocation constructs a runner whose cron parses against
// the given location. Useful when scheduling work for users in a fixed
// timezone (e.g. always 8 AM Asia/Tokyo).
func NewJobRunnerWithLocation(loc *time.Location) *JobRunner {
	return &JobRunner{
		cron: cron.New(cron.WithLocation(loc), cron.WithParser(
			cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow),
		)),
		logger: log.Printf,
	}
}

// WithLogger replaces the default log.Printf with a custom logger.
// Useful when embedding into a host with structured logging.
func (r *JobRunner) WithLogger(fn func(format string, args ...any)) *JobRunner {
	if fn != nil {
		r.logger = fn
	}
	return r
}

// Add registers a Job. Returns an error when Spec is empty or invalid.
// May be called before or after Start.
func (r *JobRunner) Add(j *Job) error {
	if j == nil {
		return fmt.Errorf("nil job")
	}
	if j.Name == "" {
		return fmt.Errorf("job name is required")
	}
	if j.Spec == "" {
		return fmt.Errorf("job %q: spec is required", j.Name)
	}
	if j.Handler == nil {
		return fmt.Errorf("job %q: handler is required", j.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	id, err := r.cron.AddFunc(j.Spec, func() {
		ctx := r.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := j.Handler(ctx); err != nil {
			if j.OnError != nil {
				j.OnError(j.Name, err)
			} else {
				r.logger("[cron] job %q failed: %v", j.Name, err)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("job %q: parse spec: %w", j.Name, err)
	}
	r.jobs = append(r.jobs, registered{id: id, job: j})
	return nil
}

// MustAdd is like Add but panics on error. Useful at server startup
// when an invalid schedule should fail loudly rather than silently
// drop a job.
func (r *JobRunner) MustAdd(j *Job) {
	if err := r.Add(j); err != nil {
		panic(err)
	}
}

// Start runs the scheduler until ctx is cancelled. Blocks until shutdown
// completes. Typically called in its own goroutine.
func (r *JobRunner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return fmt.Errorf("job runner already started")
	}
	r.started = true
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.cron.Start()
	r.mu.Unlock()

	<-ctx.Done()
	r.shutdown()
	return nil
}

// Stop cancels the runner. Safe to call from any goroutine; idempotent.
// Waits for in-flight jobs to return before exiting.
func (r *JobRunner) Stop() {
	r.shutdown()
}

func (r *JobRunner) shutdown() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		if r.cancel != nil {
			r.cancel()
		}
		stopCtx := r.cron.Stop()
		r.mu.Unlock()
		// Wait for the cron's internal goroutine to drain.
		<-stopCtx.Done()
	})
}

// Entries returns metadata about scheduled jobs (next/prev run times).
// Useful for observability endpoints.
func (r *JobRunner) Entries() []JobEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]JobEntry, 0, len(r.jobs))
	for _, reg := range r.jobs {
		e := r.cron.Entry(reg.id)
		out = append(out, JobEntry{
			Name:  reg.job.Name,
			Spec:  reg.job.Spec,
			Next:  e.Next,
			Prev:  e.Prev,
			Valid: e.Valid(),
		})
	}
	return out
}

// JobEntry is a snapshot of one scheduled job's runtime state.
type JobEntry struct {
	Name  string
	Spec  string
	Next  time.Time
	Prev  time.Time
	Valid bool
}

// ── Ergonomic spec builders ────────────────────────────────────────────────
// Callers who don't want to hand-write cron strings can use these helpers
// to produce a Spec value. They emit standard 5-field cron expressions
// compatible with the runner's parser.

// DailyAt returns a cron spec that fires once per day at the given hour
// and minute (server-local time unless the runner was built with a
// custom location).
//
//	scheduler.DailyAt(8, 0)  // "0 8 * * *"
func DailyAt(hour, minute int) string {
	return fmt.Sprintf("%d %d * * *", clamp(minute, 0, 59), clamp(hour, 0, 23))
}

// WeeklyAt fires once per week on the given weekday at hour:minute.
//
//	scheduler.WeeklyAt(time.Monday, 9, 0)  // "0 9 * * 1"
func WeeklyAt(dow time.Weekday, hour, minute int) string {
	return fmt.Sprintf("%d %d * * %d", clamp(minute, 0, 59), clamp(hour, 0, 23), int(dow))
}

// MonthlyAt fires once per month on dayOfMonth at hour:minute.
//
//	scheduler.MonthlyAt(1, 9, 0)  // "0 9 1 * *"
func MonthlyAt(dayOfMonth, hour, minute int) string {
	return fmt.Sprintf("%d %d %d * *", clamp(minute, 0, 59), clamp(hour, 0, 23), clamp(dayOfMonth, 1, 31))
}

// EveryNMinutes fires every n minutes starting at minute 0.
//
//	scheduler.EveryNMinutes(15)  // "*/15 * * * *"
func EveryNMinutes(n int) string {
	if n <= 0 {
		n = 1
	}
	if n >= 60 {
		// Cron's */N stops at the field's max; 60+ collapses to once per hour.
		return "0 * * * *"
	}
	return fmt.Sprintf("*/%d * * * *", n)
}

// EveryNHours fires every n hours at minute 0.
//
//	scheduler.EveryNHours(6)  // "0 */6 * * *"
func EveryNHours(n int) string {
	if n <= 0 {
		n = 1
	}
	if n >= 24 {
		return "0 0 * * *"
	}
	return fmt.Sprintf("0 */%d * * *", n)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
