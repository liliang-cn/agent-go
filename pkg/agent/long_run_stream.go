// Watching a long run from a program, not a log file.
//
// RunSegments returns when the task is over, which on the tasks it exists for
// is hours away. Every event its segments produce is collected inside
// Service.Run and discarded — so a host with a window has nothing to draw
// until the whole thing finishes, and a host that wants to show a stop button
// has nothing to attach it to.
//
// The Observer callbacks added alongside this cover the other half of the
// problem: they narrate a run into a file for whoever greps it afterwards.
// They are not an event stream, and a UI needs an event stream.
//
// So: the same supervisor, with its segments' events forwarded as they happen
// and the LongRunResult delivered at the end.
package agent

import "context"

// LongRunStream carries a long run in flight.
type LongRunStream struct {
	// Events is every event from every segment, in order, closed when the
	// task ends. Read it to completion, or the run blocks on a full channel
	// exactly like RunStream's.
	Events <-chan *Event
	// Result is closed after Events, carrying the finished LongRunResult and
	// any error. Reading it before Events is drained will deadlock for the
	// same reason.
	Result <-chan LongRunOutcome
}

// LongRunOutcome is what RunSegments would have returned.
type LongRunOutcome struct {
	Result *LongRunResult
	Err    error
}

// RunSegmentsStream drives a task across segments the way RunSegments does,
// and forwards the events instead of swallowing them.
//
// The task runs in its own goroutine; cancelling ctx stops it exactly as it
// stops RunSegments. The caller must drain Events.
func (s *Service) RunSegmentsStream(ctx context.Context, goal string, cfg LongRunConfig, opts ...RunOption) *LongRunStream {
	events := make(chan *Event, longRunStreamBuffer)
	result := make(chan LongRunOutcome, 1)

	go func() {
		defer close(result)
		defer close(events)
		out, err := s.runSegments(ctx, goal, cfg, events, opts...)
		result <- LongRunOutcome{Result: out, Err: err}
	}()

	return &LongRunStream{Events: events, Result: result}
}

// longRunStreamBuffer is deep enough that a consumer drawing a frame does not
// stall the loop, and shallow enough that a consumer that has stopped reading
// is noticed rather than buffered for hours.
const longRunStreamBuffer = 256
