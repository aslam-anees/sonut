package tui

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// startupTracer logs startup phases when ATCODE_STARTUP_TRACE=1.
type startupTracer struct {
	enabled   bool
	startTime time.Time
	mu        sync.Mutex
	phases    []struct {
		name     string
		duration time.Duration
	}
}

var tracer = &startupTracer{
	enabled:   atcodeEnv("STARTUP_TRACE") == "1",
	startTime: time.Now(),
}

// tracePhase records the completion of a startup phase
func tracePhase(name string, duration time.Duration) {
	if !tracer.enabled {
		return
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	tracer.phases = append(tracer.phases, struct {
		name     string
		duration time.Duration
	}{name, duration})
	elapsed := time.Since(tracer.startTime)
	fmt.Fprintf(os.Stderr, "[startup] %s: %v (total: %v)\n", name, duration, elapsed)
}

// dumpTrace outputs all recorded phases
func dumpTrace() {
	if !tracer.enabled || len(tracer.phases) == 0 {
		return
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	fmt.Fprintf(os.Stderr, "\n[startup trace]\n")
	var total time.Duration
	for _, p := range tracer.phases {
		fmt.Fprintf(os.Stderr, "  %-20s %10v\n", p.name, p.duration)
		total += p.duration
	}
	fmt.Fprintf(os.Stderr, "  %-20s %10v\n", "total", total)
	fmt.Fprintf(os.Stderr, "\n")
}
