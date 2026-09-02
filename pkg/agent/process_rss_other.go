//go:build !linux

package agent

// processCurrentRSS has no cgo-free answer outside linux; darwin's task_info
// needs cgo. PeakRSSBytes from getrusage is what those platforms get, and
// RSSKnown says so rather than reporting a zero that looks like an answer.
func processCurrentRSS() (uint64, bool) { return 0, false }
