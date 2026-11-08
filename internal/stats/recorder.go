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

// Conn is what the kernel reports of a TCP connection when it ends.
type Conn struct {
	// RetransSegs is the number of segments the connection retransmitted,
	// SYN included.
	RetransSegs uint32
	// SegsOut is the number of segments the connection sent,
	// retransmissions included.
	SegsOut uint32
	// RTT is the kernel's smoothed round-trip time, or 0 when it has none.
	RTT time.Duration
	// FastOpen reports that the data sent in the SYN was acknowledged.
	FastOpen bool
}

// ConnSummary describes the TCP connections of a run.
type ConnSummary struct {
	// Conns is the number of connections recorded.
	Conns int64
	// RetransConns is the number of connections that retransmitted at
	// least one segment.
	RetransConns int64
	// RetransSegs and SegsOut are the segments that the connections
	// retransmitted and sent.
	RetransSegs int64
	SegsOut     int64
	// RTTMean, RTTP99 and RTTMax describe the smoothed RTTs of the
	// connections that have one; they are 0 when none has.
	RTTMean time.Duration
	RTTP99  time.Duration
	RTTMax  time.Duration
	// FastOpenConns is the number of connections whose data in the SYN was
	// acknowledged.
	FastOpenConns int64
}

// Recorder accumulates round trips over two periods: the whole run, and the
// current reporting interval, since the last TakeInterval (or since the
// Recorder was created). It also accumulates the TCP connections of the
// run. It is safe for concurrent use.
type Recorder struct {
	now func() time.Time

	mu            sync.Mutex
	total         Histogram
	interval      Histogram
	intervalStart time.Time

	// connMu guards conns, so that recording a connection does not hold up
	// the round trips.
	connMu sync.Mutex
	conns  ConnSummary
	rtts   Histogram
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

// RecordConn adds a TCP connection that ended.
func (r *Recorder) RecordConn(c Conn) {
	r.connMu.Lock()
	defer r.connMu.Unlock()
	r.conns.Conns++
	if c.RetransSegs > 0 {
		r.conns.RetransConns++
	}
	r.conns.RetransSegs += int64(c.RetransSegs)
	r.conns.SegsOut += int64(c.SegsOut)
	if c.FastOpen {
		r.conns.FastOpenConns++
	}
	// A connection without an RTT sample (for instance a SYN sent with
	// Fast Open and never answered) has no RTT, not an RTT of 0.
	if c.RTT > 0 {
		r.rtts.Record(c.RTT)
	}
}

// Conns summarizes the TCP connections recorded during the run.
func (r *Recorder) Conns() ConnSummary {
	r.connMu.Lock()
	defer r.connMu.Unlock()
	s := r.conns
	s.RTTMean = r.rtts.Mean()
	s.RTTP99 = r.rtts.Quantile(0.99)
	s.RTTMax = r.rtts.Max()
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
