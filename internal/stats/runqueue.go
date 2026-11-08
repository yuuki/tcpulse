package stats

import "time"

// RunQueueSummary describes how long the threads of the process waited for a
// CPU (--runqueue-latency): the time from when a thread became runnable,
// woken up or preempted, to when it ran.
type RunQueueSummary struct {
	// Waits is the number of waits that ended.
	Waits int64
	// Preempted is the number of involuntary context switches of the
	// threads, as the kernel counts them: the scheduler took the CPU from a
	// thread that was still runnable (its time slice ended, it was
	// preempted, or it yielded).
	Preempted int64
	// Total is the sum of the waits.
	Total time.Duration
	// P50 and P99 are percentiles of the waits, rounded up to at most 1/8
	// above the exact value; Max is exact.
	P50 time.Duration
	P99 time.Duration
	Max time.Duration
}
