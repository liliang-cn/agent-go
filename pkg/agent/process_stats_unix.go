//go:build unix

package agent

import (
	"runtime"
	"syscall"
)

// processCPUAndPeakRSS reads cumulative CPU time and the peak resident set
// from getrusage(RUSAGE_SELF) — no cgo, no dependency, no /proc.
//
// ru_maxrss is bytes on darwin and kilobytes on linux, which is a difference
// the man pages disagree about and every program gets wrong once.
func processCPUAndPeakRSS() (userSeconds, systemSeconds float64, peakRSS uint64, ok bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0, 0, false
	}
	user := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
	sys := float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
	peak := uint64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		peak *= 1024
	}
	return user, sys, peak, true
}
