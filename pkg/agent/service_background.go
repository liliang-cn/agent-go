package agent

import "sync"

// Background work started by a run.
//
// completeRunWithStop saves to memory in a goroutine with context.Background(),
// so the write outlives the run that started it — deliberately, since the caller
// should not wait on memory extraction to get an answer. But nothing tracked it,
// so it also outlived Close(): a test that ran an agent inside t.TempDir() would
// return, the directory would be removed, and the goroutine would recreate files
// inside it. That surfaced as a flaky "TempDir RemoveAll cleanup: directory not
// empty" — a failure with nothing to do with what the test asserted.
//
// Track those goroutines so Close can wait for them. Fire-and-forget stays
// fire-and-forget during a run; it just no longer escapes the Service's lifetime.

// goBackground runs fn in a goroutine that Close will wait for.
func (s *Service) goBackground(fn func()) {
	if s == nil {
		go fn()
		return
	}
	s.bgWork.Add(1)
	go func() {
		defer s.bgWork.Done()
		fn()
	}()
}

// waitBackground blocks until every goroutine started via goBackground is done.
func (s *Service) waitBackground() {
	if s == nil {
		return
	}
	s.bgWork.Wait()
}

// bgWorkGroup is the wait group type, named so the zero value is usable and no
// constructor has to remember to initialise it.
type bgWorkGroup = sync.WaitGroup
