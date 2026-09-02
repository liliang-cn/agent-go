//go:build !unix

package agent

// processCPUAndPeakRSS has no portable answer off unix. Saying so is better
// than reporting zero, which reads as a process that used no CPU.
func processCPUAndPeakRSS() (userSeconds, systemSeconds float64, peakRSS uint64, ok bool) {
	return 0, 0, 0, false
}
