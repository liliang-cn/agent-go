package agent

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestSampleProcessReportsSomethingReal(t *testing.T) {
	s := SampleProcess()
	if s.Goroutines <= 0 {
		t.Errorf("Goroutines = %d, want at least this one", s.Goroutines)
	}
	if s.HeapAllocBytes == 0 || s.HeapObjects == 0 {
		t.Errorf("heap = %d bytes / %d objects, want non-zero in a running program", s.HeapAllocBytes, s.HeapObjects)
	}
	if s.NumCPU != runtime.NumCPU() {
		t.Errorf("NumCPU = %d, want %d", s.NumCPU, runtime.NumCPU())
	}
	if s.Uptime <= 0 || s.At.IsZero() {
		t.Errorf("uptime = %v at %v, want a positive age and a timestamp", s.Uptime, s.At)
	}

	// A number reported as unknown must be reported as zero, so nobody plots
	// "this process used no memory".
	if !s.RSSKnown && s.RSSBytes != 0 {
		t.Errorf("RSSBytes = %d with RSSKnown false", s.RSSBytes)
	}
	if !s.CPUKnown && s.CPUSeconds() != 0 {
		t.Errorf("CPU = %v with CPUKnown false", s.CPUSeconds())
	}
	if runtime.GOOS == "linux" && (!s.RSSKnown || s.RSSBytes == 0) {
		t.Errorf("linux must read current RSS from /proc: known=%v bytes=%d", s.RSSKnown, s.RSSBytes)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if !s.CPUKnown {
			t.Error("unix must read CPU time from getrusage")
		}
		if s.PeakRSSBytes == 0 {
			t.Error("unix must read peak RSS from getrusage")
		}
		// A peak RSS of a few kilobytes means the darwin/linux unit
		// difference was got wrong in one direction or the other.
		if s.PeakRSSBytes < 1<<20 {
			t.Errorf("PeakRSSBytes = %d, implausibly small — check the KB/bytes unit", s.PeakRSSBytes)
		}
	}
}

// recordingResourceObserver is a host that wants the process readings.
type recordingResourceObserver struct {
	BaseObserver
	mu      sync.Mutex
	samples []ResourceSample
}

func (o *recordingResourceObserver) OnResourceSample(_ context.Context, s ResourceSample) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.samples = append(o.samples, s)
}

func (o *recordingResourceObserver) all() []ResourceSample {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]ResourceSample(nil), o.samples...)
}

// A run hands its rounds' process readings to whoever asked for them, and
// marks the last one so it can be compared against the first.
func TestRunEmitsResourceSamples(t *testing.T) {
	obs := &recordingResourceObserver{}
	svc, err := New("resources").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLLM{finishAt: 0}).
		WithObserver(obs).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if _, err := svc.Run(context.Background(), "Work."); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := obs.all()
	if len(got) < 2 {
		t.Fatalf("got %d samples, want at least one round and one final", len(got))
	}
	finals := 0
	for _, s := range got {
		if s.Final {
			finals++
			continue
		}
		if s.Round <= 0 {
			t.Errorf("round sample with Round = %d", s.Round)
		}
	}
	if finals != 1 {
		t.Errorf("got %d final samples, want exactly one", finals)
	}
	last := got[len(got)-1]
	if !last.Final {
		t.Error("the last sample should be the final one")
	}
	if last.Stats.Goroutines <= 0 || last.Stats.HeapAllocBytes == 0 {
		t.Errorf("final sample carries no reading: %+v", last.Stats)
	}
	if last.RunID == "" || last.SessionID == "" {
		t.Errorf("a sample must say which run it came from: run=%q session=%q", last.RunID, last.SessionID)
	}
}

// Sampling stops the world, briefly. A service nobody is watching must not
// pay for it on every round of every run.
func TestNoResourceObserverMeansNoSampling(t *testing.T) {
	svc, err := New("no-resources").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLLM{finishAt: 0}).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	before := sampleCount.Load()
	if _, err := svc.Run(context.Background(), "Work."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if after := sampleCount.Load(); after != before {
		t.Errorf("sampled %d times with no resource observer registered", after-before)
	}
}

// The two long-run sinks carry the readings: the trace file a panel reads,
// and the log a human greps.
func TestTraceAndActivityLogCarryResourceReadings(t *testing.T) {
	var trace, activity strings.Builder
	svc, err := New("resource-sinks").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLLM{finishAt: 0}).
		WithObserver(NewTraceWriter(&trace)).
		WithObserver(NewActivityLog(&activity)).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.Run(context.Background(), "Work."); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(trace.String(), `"event":"resource"`) || !strings.Contains(trace.String(), `"goroutines"`) {
		t.Fatalf("the trace has no resource line:\n%s", trace.String())
	}
	if !strings.Contains(activity.String(), "res  ") || !strings.Contains(activity.String(), "heap=") {
		t.Fatalf("the activity log never narrated the process:\n%s", activity.String())
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{512, "512B"},
		{2048, "2.0KiB"},
		{5 * 1024 * 1024, "5.0MiB"},
		{3 * 1024 * 1024 * 1024, "3.0GiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
