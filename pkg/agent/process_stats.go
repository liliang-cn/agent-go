package agent

import (
	"context"
	"runtime"
	"sync/atomic"
	"time"
)

// What the process itself is doing.
//
// Everything else in this package observes the agent: model turns, tools,
// tokens, cost, lints, compaction, checkpoints. None of it says anything
// about the program those things run inside — and a framework whose whole
// point is a task that runs for hours is exactly where that matters. A run
// that leaks a goroutine per tool call, or holds every tool result it ever
// read, does not fail a lint or a test. It fails at 03:00 with the OOM
// killer, and the loop's own telemetry says the run was going fine.
//
// So: one sampler, cheap enough to call at every round boundary, reporting
// what is knowable without cgo or a dependency.

// ProcessStats is a point-in-time reading of what this process is using.
//
// Everything here is the whole process, not one run: a Service runs many
// tasks at once and they share a heap. Read it as "what the host program
// looks like right now, while this run was in flight".
type ProcessStats struct {
	At time.Time `json:"at"`

	// HeapAllocBytes is live heap — the number that grows when an agent
	// keeps what it should have dropped.
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	// HeapSysBytes is heap memory obtained from the OS. It only ever grows
	// back down slowly, so it tracks the high-water mark of the heap.
	HeapSysBytes uint64 `json:"heap_sys_bytes"`
	// HeapObjects is the count of live objects, which separates "one big
	// buffer" from "a million small leaks".
	HeapObjects uint64 `json:"heap_objects"`
	// StackInuseBytes is stack memory in use, which rises with goroutines.
	StackInuseBytes uint64 `json:"stack_inuse_bytes"`

	// Goroutines is the count right now. A tool that starts a goroutine and
	// never joins it shows up here long before it shows up anywhere else.
	Goroutines int `json:"goroutines"`

	NumGC         uint32        `json:"num_gc"`
	GCPauseTotal  time.Duration `json:"gc_pause_total_ns"`
	GCCPUFraction float64       `json:"gc_cpu_fraction"`

	// RSSBytes is resident set size — what the OS says the process holds,
	// which is the number an OOM killer reads. Zero when RSSKnown is false:
	// reading it without cgo is a Linux-only trick, and saying zero on a
	// platform that cannot answer would look like a process using no memory.
	RSSBytes uint64 `json:"rss_bytes,omitempty"`
	RSSKnown bool   `json:"rss_known"`
	// PeakRSSBytes is the high-water mark the kernel remembers. Available on
	// unix everywhere, and the right number for "did this task come close to
	// being killed".
	PeakRSSBytes uint64 `json:"peak_rss_bytes,omitempty"`

	// CPUUserSeconds and CPUSystemSeconds are cumulative process CPU time.
	// They are counters, not a percentage: two samples and the wall time
	// between them give the rate, and a rate is what you want anyway.
	CPUUserSeconds   float64 `json:"cpu_user_seconds"`
	CPUSystemSeconds float64 `json:"cpu_system_seconds"`
	CPUKnown         bool    `json:"cpu_known"`

	NumCPU int           `json:"num_cpu"`
	Uptime time.Duration `json:"uptime_ns"`
}

// CPUSeconds is total process CPU time, user plus system.
func (p ProcessStats) CPUSeconds() float64 { return p.CPUUserSeconds + p.CPUSystemSeconds }

// processStart is when this process first sampled, which is close enough to
// when it started for an uptime that is only ever read by a human.
var processStart = time.Now()

// sampleCount counts samples taken, for the tests that assert the framework
// does not sample when nobody is listening.
var sampleCount atomic.Int64

// SampleProcess reads the current process statistics.
//
// It calls runtime.ReadMemStats, which stops the world briefly (tens of
// microseconds at ordinary heap sizes). That is why the runtime samples at
// round boundaries — seconds apart — and never inside a tool call or a
// streaming loop.
func SampleProcess() ProcessStats {
	sampleCount.Add(1)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	s := ProcessStats{
		At:              time.Now(),
		HeapAllocBytes:  ms.HeapAlloc,
		HeapSysBytes:    ms.HeapSys,
		HeapObjects:     ms.HeapObjects,
		StackInuseBytes: ms.StackInuse,
		Goroutines:      runtime.NumGoroutine(),
		NumGC:           ms.NumGC,
		GCPauseTotal:    time.Duration(ms.PauseTotalNs),
		GCCPUFraction:   ms.GCCPUFraction,
		NumCPU:          runtime.NumCPU(),
		Uptime:          time.Since(processStart),
	}
	if user, sys, peak, ok := processCPUAndPeakRSS(); ok {
		s.CPUUserSeconds, s.CPUSystemSeconds, s.CPUKnown = user, sys, true
		s.PeakRSSBytes = peak
	}
	if rss, ok := processCurrentRSS(); ok {
		s.RSSBytes, s.RSSKnown = rss, true
	}
	return s
}

// ResourceSample is one process reading, tagged with the run it was taken
// during. Round is the round about to start; Final marks the sample taken as
// the run ended, which is the one worth comparing against the first.
type ResourceSample struct {
	TaskID    string
	RunID     string
	SessionID string
	AgentName string
	Round     int
	Final     bool
	Stats     ProcessStats
}

// ResourceObserver receives a process reading at every round boundary and
// once more as the run ends.
//
// It is deliberately NOT a method on Observer. Observer is implemented by
// hosts outside this repository, and adding a fourteenth method would break
// every one of them that does not embed BaseObserver. An optional interface
// costs a type assertion and breaks nobody — the same rule the extension
// seams already follow.
//
//	type myObs struct{ agent.BaseObserver }
//	func (myObs) OnResourceSample(_ context.Context, s agent.ResourceSample) { … }
type ResourceObserver interface {
	OnResourceSample(ctx context.Context, sample ResourceSample)
}

// hasResourceObserver reports whether anyone is listening for process
// samples. The sampler stops the world, briefly, so a service nobody is
// watching must not pay for it — this is checked before every sample.
func (s *Service) hasResourceObserver() bool {
	if s == nil {
		return false
	}
	s.observersMu.RLock()
	defer s.observersMu.RUnlock()
	for _, o := range s.observers {
		if _, ok := o.(ResourceObserver); ok {
			return true
		}
	}
	return false
}

// emitResourceSample takes one reading and hands it to every observer that
// asked for them. Nothing is sampled when nobody is listening.
func (s *Service) emitResourceSample(ctx context.Context, sample ResourceSample) {
	if s == nil || !s.hasResourceObserver() {
		return
	}
	sample.Stats = SampleProcess()
	s.observersMu.RLock()
	snapshot := make([]Observer, len(s.observers))
	copy(snapshot, s.observers)
	s.observersMu.RUnlock()
	for _, o := range snapshot {
		ro, ok := o.(ResourceObserver)
		if !ok {
			continue
		}
		s.invokeObserver(o, func(Observer) { ro.OnResourceSample(ctx, sample) })
	}
}

// emitResourceSample is the runtime's side of the same thing: it knows the
// run the sample belongs to.
func (r *Runtime) emitResourceSample(ctx context.Context, round int, final bool) {
	if r == nil || r.svc == nil {
		return
	}
	r.svc.emitResourceSample(ctx, ResourceSample{
		TaskID:    currentTaskID(r.session),
		RunID:     r.runID(),
		SessionID: r.session.GetID(),
		AgentName: r.currentAgentName(),
		Round:     round,
		Final:     final,
	})
}
