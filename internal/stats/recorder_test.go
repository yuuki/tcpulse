package stats

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{t: time.Unix(1700000000, 0)} }
func (c *fakeClock) recorder() *Recorder     { return newRecorder(c.now) }
func recordN(r *Recorder, n int, d time.Duration) {
	for range n {
		r.Record(d)
	}
}

func TestRecorderIntervalsAndTotal(t *testing.T) {
	clock := newFakeClock()
	r := clock.recorder()

	recordN(r, 10, time.Millisecond)
	clock.advance(2 * time.Second)
	first := r.TakeInterval()
	if first.Count != 10 || first.Rate != 5 || first.Max != time.Millisecond {
		t.Fatalf("first interval = %+v, want 10 round trips of 1ms at 5/s", first)
	}

	recordN(r, 30, 3*time.Millisecond)
	clock.advance(time.Second)
	second := r.TakeInterval()
	if second.Count != 30 || second.Rate != 30 || second.Min != 3*time.Millisecond {
		t.Fatalf("second interval = %+v, want 30 round trips of 3ms at 30/s", second)
	}

	total := r.Total(4 * time.Second)
	if total.Count != 40 || total.Min != time.Millisecond || total.Max != 3*time.Millisecond {
		t.Fatalf("total = %+v, want 40 round trips between 1ms and 3ms", total)
	}
	if total.Rate != 10 {
		t.Fatalf("total rate = %v, want 40 over the 4s of the run", total.Rate)
	}
	if want := (10*time.Millisecond + 90*time.Millisecond) / 40; total.Mean != want {
		t.Fatalf("total mean = %v, want %v", total.Mean, want)
	}
}

func TestRecorderEmpty(t *testing.T) {
	clock := newFakeClock()
	r := clock.recorder()
	if s := r.TakeInterval(); s != (Summary{}) {
		t.Fatalf("empty interval without elapsed time = %+v, want zero", s)
	}
	if s := r.Total(time.Second); s != (Summary{}) {
		t.Fatalf("empty total = %+v, want zero", s)
	}
}

func TestRecorderConcurrent(t *testing.T) {
	r := NewRecorder()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { recordN(r, 1000, time.Microsecond) })
	}
	wg.Go(func() {
		for range 100 {
			r.TakeInterval()
		}
	})
	wg.Wait()
	if got := r.Total(time.Second).Count; got != 8000 {
		t.Fatalf("total count = %d, want 8000", got)
	}
}

func TestSet(t *testing.T) {
	// The same address given twice is two targets.
	separate := NewSet(3, false)
	if separate.For(0) == nil || separate.For(0) == separate.For(1) || separate.For(0) == separate.For(2) {
		t.Fatal("targets do not have a Recorder each")
	}

	merged := NewSet(2, true)
	if merged.For(0) == nil || merged.For(0) != merged.For(1) {
		t.Fatal("merged targets do not share a Recorder")
	}
}

func TestRecorderConns(t *testing.T) {
	r := NewRecorder()
	if s := r.Conns(); s != (ConnSummary{}) {
		t.Fatalf("no connection = %+v, want zero", s)
	}
	r.RecordConn(Conn{SegsOut: 10, RTT: 100 * time.Microsecond})
	r.RecordConn(Conn{RetransSegs: 2, SegsOut: 12, RTT: 300 * time.Microsecond, FastOpen: true})
	// An unanswered SYN: retransmitted, without an RTT.
	r.RecordConn(Conn{RetransSegs: 4, SegsOut: 5})

	want := ConnSummary{
		Conns:         3,
		RetransConns:  2,
		RetransSegs:   6,
		SegsOut:       27,
		RTTMean:       200 * time.Microsecond, // the connection without an RTT does not count
		RTTP99:        300 * time.Microsecond,
		RTTMax:        300 * time.Microsecond,
		FastOpenConns: 1,
	}
	if s := r.Conns(); s != want {
		t.Fatalf("connections = %+v, want %+v", s, want)
	}
	// Round trips are counted apart.
	if s := r.Total(time.Second); s.Count != 0 {
		t.Fatalf("round trips = %+v, want none", s)
	}
}

func TestRecorderConnsWithoutRTT(t *testing.T) {
	r := NewRecorder()
	r.RecordConn(Conn{RetransSegs: 6, SegsOut: 7})
	if s := r.Conns(); s.Conns != 1 || s.RetransConns != 1 || s.RTTMean != 0 || s.RTTP99 != 0 || s.RTTMax != 0 {
		t.Fatalf("connections = %+v, want one retransmitting connection and no RTT", s)
	}
}

func TestRecorderConnsConcurrent(t *testing.T) {
	r := NewRecorder()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				r.RecordConn(Conn{RetransSegs: 1, SegsOut: 3, RTT: time.Millisecond})
				r.Record(time.Millisecond)
			}
		})
	}
	wg.Wait()
	if s := r.Conns(); s.Conns != 800 || s.RetransSegs != 800 || s.SegsOut != 2400 || s.RTTMax != time.Millisecond {
		t.Fatalf("connections = %+v, want 800", s)
	}
}

func TestRecorderEvents(t *testing.T) {
	r := NewRecorder()
	if s := r.Events(); s != (EventSummary{}) {
		t.Fatalf("no event = %+v, want zero", s)
	}
	r.RecordEvents(100*time.Microsecond, Events{})
	r.RecordEvents(200*time.Microsecond, Events{})
	r.RecordEvents(time.Second, Events{SYNRetrans: 2})
	r.RecordEvents(300*time.Millisecond, Events{RTORetrans: 1, OtherRetrans: 3})
	r.RecordEvents(time.Millisecond, Events{FastOpenRetrans: 1})
	r.RecordEvents(time.Millisecond, Events{ResetsReceived: 1})
	r.RecordUntracked()
	r.RecordUnmatched(Events{SYNRetrans: 3, RTORetrans: 1, ResetsReceived: 2})

	want := EventSummary{
		RoundTrips:        4,
		RetransRoundTrips: RetransCounts{SYN: 1, FastOpen: 1, RTO: 1, Other: 1},
		WithEvents:        LatencySummary{Count: 4, P99: time.Second, Max: time.Second},
		WithoutEvents:     LatencySummary{Count: 2, P99: 200 * time.Microsecond, Max: 200 * time.Microsecond},
		ResetsReceived:    3,
		Unmatched:         RetransCounts{SYN: 3, RTO: 1},
		Untracked:         1,
	}
	if s := r.Events(); s != want {
		t.Fatalf("events = %+v\nwant %+v", s, want)
	}
	if want.Unmatched.Total() != 4 {
		t.Fatalf("Total() = %d, want 4", want.Unmatched.Total())
	}
}

func TestEventsSub(t *testing.T) {
	before := Events{SYNRetrans: 1, OtherRetrans: 2}
	after := Events{SYNRetrans: 1, OtherRetrans: 5, ResetsReceived: 1}
	if d := after.Sub(before); d != (Events{OtherRetrans: 3, ResetsReceived: 1}) {
		t.Fatalf("Sub = %+v", d)
	}
	if !before.Sub(before).IsZero() || after.IsZero() {
		t.Fatal("IsZero")
	}
}

func TestRecorderBreakdown(t *testing.T) {
	r := NewRecorder()
	if s := r.Breakdown(); s != (BreakdownSummary{}) {
		t.Fatalf("no breakdown = %+v, want zero", s)
	}
	for i := range 100 {
		r.RecordBreakdown(time.Duration(i+1)*time.Microsecond, time.Duration(i+1)*time.Millisecond, 3*time.Microsecond)
	}
	r.RecordMissingBreakdown()
	r.RecordMissingBreakdown()
	r.RecordNegativeBreakdown()
	s := r.Breakdown()
	if s.RoundTrips != 100 || s.Missing != 2 || s.Negative != 1 {
		t.Fatalf("counts = %+v", s)
	}
	// Nearest rank, within the 1/128 of the histogram.
	near := func(got, want time.Duration) bool { return got >= want && got <= want+want/128 }
	if !near(s.Send.P50, 50*time.Microsecond) || !near(s.Send.P99, 99*time.Microsecond) ||
		!near(s.Network.P50, 50*time.Millisecond) || !near(s.Network.P99, 99*time.Millisecond) ||
		!near(s.Receive.P50, 3*time.Microsecond) || !near(s.Receive.P99, 3*time.Microsecond) {
		t.Fatalf("parts = %+v", s)
	}
}
