package stats

import (
	"sync"
	"time"
)

// Summary describes the round trips of a period.
type Summary struct {
	Count int64
	Min   time.Duration
	Max   time.Duration
	Mean  time.Duration
	P90   time.Duration
	P95   time.Duration
	P99   time.Duration
	// Rate is Count divided by the length of the period, in round trips per
	// second.
	Rate float64
}

func summarize(h *Histogram, elapsed time.Duration) Summary {
	s := Summary{
		Count: h.Count(),
		Min:   h.Min(),
		Max:   h.Max(),
		Mean:  h.Mean(),
		P90:   h.Quantile(0.90),
		P95:   h.Quantile(0.95),
		P99:   h.Quantile(0.99),
	}
	if elapsed > 0 {
		s.Rate = float64(s.Count) / elapsed.Seconds()
	}
	return s
}

// Recorder accumulates round trips over two periods: the whole run, and the
// current reporting interval, since the last TakeInterval (or since the
// Recorder was created). It is safe for concurrent use.
type Recorder struct {
	now func() time.Time

	mu            sync.Mutex
	total         Histogram
	interval      Histogram
	intervalStart time.Time
}

// NewRecorder returns a Recorder whose first interval starts now.
func NewRecorder() *Recorder {
	return newRecorder(time.Now)
}

func newRecorder(now func() time.Time) *Recorder {
	return &Recorder{now: now, intervalStart: now()}
}

// Record adds a round trip that took d.
func (r *Recorder) Record(d time.Duration) {
	r.mu.Lock()
	r.total.Record(d)
	r.interval.Record(d)
	r.mu.Unlock()
}

// Total summarizes every round trip of the run, whose length (for the rate)
// the caller knows: the run stops when its last echo is back, which can be
// well before its configured duration.
func (r *Recorder) Total(run time.Duration) Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return summarize(&r.total, run)
}

// TakeInterval summarizes the round trips since the previous call (or since
// the Recorder was created) and starts a new interval.
func (r *Recorder) TakeInterval() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.now()
	s := summarize(&r.interval, t.Sub(r.intervalStart))
	r.interval.Reset()
	r.intervalStart = t
	return s
}

// Set holds the Recorders of a run: one per target, or a single one that
// every target shares when their results are merged. Targets are numbered in
// the order they were given; an address given twice is two targets, each
// loaded and counted on its own.
type Set struct {
	merged  *Recorder
	targets []*Recorder
}

// NewSet returns the Recorders of n targets.
func NewSet(n int, merge bool) *Set {
	if merge {
		return &Set{merged: NewRecorder()}
	}
	s := &Set{targets: make([]*Recorder, n)}
	for i := range s.targets {
		s.targets[i] = NewRecorder()
	}
	return s
}

// For returns the Recorder of target i, or the shared one when merged.
func (s *Set) For(i int) *Recorder {
	if s.merged != nil {
		return s.merged
	}
	return s.targets[i]
}
