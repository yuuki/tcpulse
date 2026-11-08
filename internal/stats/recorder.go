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

// Recorder accumulates round trips over two periods: the whole run, since
// the Recorder was created, and the current reporting interval, since the
// last TakeInterval. It is safe for concurrent use.
type Recorder struct {
	now func() time.Time

	mu            sync.Mutex
	total         Histogram
	interval      Histogram
	start         time.Time
	intervalStart time.Time
}

// NewRecorder returns a Recorder whose run and first interval start now.
func NewRecorder() *Recorder {
	return newRecorder(time.Now)
}

func newRecorder(now func() time.Time) *Recorder {
	t := now()
	return &Recorder{now: now, start: t, intervalStart: t}
}

// Record adds a round trip that took d.
func (r *Recorder) Record(d time.Duration) {
	r.mu.Lock()
	r.total.Record(d)
	r.interval.Record(d)
	r.mu.Unlock()
}

// Total summarizes every round trip since the Recorder was created.
func (r *Recorder) Total() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return summarize(&r.total, r.now().Sub(r.start))
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

// Set holds the Recorders of a run: one per peer, or a single one that every
// peer shares when the results of the peers are merged.
type Set struct {
	merged *Recorder
	peers  map[string]*Recorder
}

// NewSet returns the Recorders of peers. A peer listed twice gets a single
// Recorder.
func NewSet(peers []string, merge bool) *Set {
	if merge {
		return &Set{merged: NewRecorder()}
	}
	s := &Set{peers: make(map[string]*Recorder, len(peers))}
	for _, p := range peers {
		if _, ok := s.peers[p]; !ok {
			s.peers[p] = NewRecorder()
		}
	}
	return s
}

// For returns the Recorder of peer: the shared one when merged, nil for a
// peer NewSet was not given otherwise.
func (s *Set) For(peer string) *Recorder {
	if s.merged != nil {
		return s.merged
	}
	return s.peers[peer]
}
