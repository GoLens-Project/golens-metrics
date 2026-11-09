package golens

import (
	"context"
	"runtime"
	"syscall"
	"time"
)

const (
	MetricGoAllocBytes    = "go_memstats_alloc_bytes"
	MetricGoSysBytes      = "go_memstats_sys_bytes"
	MetricGoHeapInuse     = "go_memstats_heap_inuse_bytes"
	MetricGoHeapObjects   = "go_memstats_heap_objects"
	MetricGoGoroutines    = "go_goroutines"
	MetricCPUUsagePercent = "cpu_usage_percent"
)

// runtimeMetricDefs is the single source of truth for the process's own
// runtime-health gauges: startRuntimeMetrics registers each into the user
// registry (the history pipeline and /metrics/data serve them from there),
// and the dashboard card builder excludes the same list, so a gauge added
// here can never leak into the All Metrics grid.
var runtimeMetricDefs = []struct {
	name, description string
	min, max          float64
}{
	{MetricGoAllocBytes, "currently allocated heap bytes", 0, 0},
	{MetricGoSysBytes, "bytes obtained from the OS", 0, 0},
	{MetricGoHeapInuse, "bytes in active heap spans", 0, 0},
	{MetricGoHeapObjects, "total number of allocated objects", 0, 0},
	{MetricGoGoroutines, "application goroutines (excludes runtime)", 0, 0},
	{MetricCPUUsagePercent, "process CPU usage percentage", 0, 100},
}

// isRuntimeMetric reports whether name is one of the process's own
// runtime-health gauges (see runtimeMetricDefs).
func isRuntimeMetric(name string) bool {
	for _, m := range runtimeMetricDefs {
		if m.name == name {
			return true
		}
	}
	return false
}

// startRuntimeMetrics launches a background goroutine that periodically
// collects Go runtime stats and records them as gauge metrics.
// The goroutine exits when ctx is cancelled.
func startRuntimeMetrics(ctx context.Context, r *Registry, interval time.Duration) {
	for _, m := range runtimeMetricDefs {
		r.Register(m.name, GaugeType, m.description, nil, nil, m.min, m.max)
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		prevCPU := cpuTime()
		prevWall := time.Now()
		collectRuntime(r)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				curCPU := cpuTime()
				wallDelta := now.Sub(prevWall).Seconds()
				if wallDelta > 0 {
					cpuDelta := curCPU - prevCPU
					pct := (cpuDelta / wallDelta) * 100
					if pct < 0 {
						pct = 0
					}
					if pct > 100*float64(runtime.NumCPU()) {
						pct = 100 * float64(runtime.NumCPU())
					}
					r.Record(MetricCPUUsagePercent, pct)
				}
				prevCPU = curCPU
				prevWall = now
				collectRuntime(r)
			}
		}
	}()
}

func collectRuntime(r *Registry) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	r.Record(MetricGoAllocBytes, float64(ms.Alloc))
	r.Record(MetricGoSysBytes, float64(ms.Sys))
	r.Record(MetricGoHeapInuse, float64(ms.HeapInuse))
	r.Record(MetricGoHeapObjects, float64(ms.HeapObjects))

	// Show only application goroutines. The baseline is captured in Start()
	// before the loop/runtime-metrics goroutines are spawned, so it already
	// excludes the background goroutines that appear after Start() returns.
	totalGoroutines := runtime.NumGoroutine()
	appGoroutines := totalGoroutines - r.GoroutineBaseline
	if appGoroutines < 0 {
		appGoroutines = 0 // Shouldn't happen, but safeguard
	}
	r.Record(MetricGoGoroutines, float64(appGoroutines))
}

// cpuTime returns the total CPU time (user + system) in seconds for the
// current process.
func cpuTime() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	user := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
	sys := float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
	return user + sys
}
