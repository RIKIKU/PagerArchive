//go:build !linux

package server

type process struct {
	cpuSeconds    float64
	residentBytes uint64
}

// readProcess has nothing to read off Linux, where the server doesn't run.
func readProcess() (process, bool) { return process{}, false }
