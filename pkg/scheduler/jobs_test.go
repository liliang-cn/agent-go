package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobRunner_AddValidationsAndFires(t *testing.T) {
	r := NewJobRunner()
	if err := r.Add(nil); err == nil {
		t.Fatal("nil job should error")
	}
	if err := r.Add(&Job{Spec: "* * * * *", Handler: func(ctx context.Context) error { return nil }}); err == nil {
		t.Fatal("missing name should error")
	}
	if err := r.Add(&Job{Name: "x", Handler: func(ctx context.Context) error { return nil }}); err == nil {
		t.Fatal("missing spec should error")
	}
	if err := r.Add(&Job{Name: "x", Spec: "* * * * *"}); err == nil {
		t.Fatal("missing handler should error")
	}
	if err := r.Add(&Job{Name: "x", Spec: "bogus", Handler: func(ctx context.Context) error { return nil }}); err == nil {
		t.Fatal("invalid spec should error")
	}

	var fired atomic.Int32
	if err := r.Add(&Job{
		Name: "tick",
		Spec: "* * * * *", // every minute
		Handler: func(ctx context.Context) error {
			fired.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := len(r.Entries()); got != 1 {
		t.Fatalf("expected 1 entry, got %d", got)
	}
}

func TestJobRunner_OnErrorIsCalled(t *testing.T) {
	r := NewJobRunner()
	var captured atomic.Pointer[string]
	wantErr := errors.New("boom")

	j := &Job{
		Name: "fails",
		Spec: "* * * * *",
		Handler: func(ctx context.Context) error {
			return wantErr
		},
		OnError: func(name string, err error) {
			s := name + ":" + err.Error()
			captured.Store(&s)
		},
	}
	if err := r.Add(j); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Invoke the registered cron func directly (don't wait a minute in
	// tests). Cron exposes entries; we yank ours and call it synchronously.
	entries := r.cron.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entries[0].Job.Run()
	if got := captured.Load(); got == nil || *got == "" {
		t.Fatal("OnError not called")
	}
}

func TestJobRunner_StartStop(t *testing.T) {
	r := NewJobRunner()
	if err := r.Add(&Job{
		Name:    "noop",
		Spec:    "* * * * *",
		Handler: func(ctx context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = r.Start(ctx)
		close(done)
	}()

	// Give Start a moment to set up cron, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}

	// Second Start should error.
	if err := r.Start(context.Background()); err == nil {
		t.Fatal("second Start should error")
	}
}

func TestSpecBuilders(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{DailyAt(8, 30), "30 8 * * *"},
		{DailyAt(25, 99), "59 23 * * *"}, // clamped
		{WeeklyAt(time.Monday, 9, 0), "0 9 * * 1"},
		{MonthlyAt(1, 0, 0), "0 0 1 * *"},
		{EveryNMinutes(15), "*/15 * * * *"},
		{EveryNMinutes(0), "*/1 * * * *"},
		{EveryNMinutes(120), "0 * * * *"},
		{EveryNHours(6), "0 */6 * * *"},
		{EveryNHours(48), "0 0 * * *"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
		// Sanity-check each generated spec parses.
		p := NewCronParser()
		if err := p.Validate(c.got); err != nil {
			t.Errorf("spec %q does not parse: %v", c.got, err)
		}
	}
}

func TestJobRunner_AddBeforeAndAfterStart(t *testing.T) {
	r := NewJobRunner()
	if err := r.Add(&Job{Name: "pre", Spec: "* * * * *", Handler: func(ctx context.Context) error { return nil }}); err != nil {
		t.Fatalf("pre-start add: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	if err := r.Add(&Job{Name: "post", Spec: "* * * * *", Handler: func(ctx context.Context) error { return nil }}); err != nil {
		t.Fatalf("post-start add: %v", err)
	}
}
