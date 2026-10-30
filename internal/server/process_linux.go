package server

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

type process struct {
	cpuSeconds    float64
	residentBytes uint64
}

// readProcess reads the API's CPU time and resident memory (R72).
func readProcess() (process, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return process{}, false
	}
	p := process{cpuSeconds: tv(ru.Utime) + tv(ru.Stime)}
	// statm: size resident shared ..., in pages.
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 1 {
			if pages, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				p.residentBytes = pages * uint64(os.Getpagesize())
			}
		}
	}
	return p, true
}

func tv(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
