//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
)

// processCurrentRSS reads the resident set size from /proc/self/statm, whose
// second field is resident pages.
//
// Current RSS, not the peak: on a task that runs for hours the shape of this
// number over time is the difference between "big" and "leaking".
func processCurrentRSS() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * uint64(os.Getpagesize()), true
}
