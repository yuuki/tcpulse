package stats

import (
	"reflect"
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
	if s := r.Events(); !reflect.DeepEqual(s, EventSummary{}) {
		t.Fatalf("no event = %+v, want zero", s)
	}
	r.RecordEvents(100*time.Microsecond, Events{})
	r.RecordEvents(200*time.Microsecond, Events{})
	r.RecordEvents(time.Second, Events{SYNRetrans: 2})
	r.RecordEvents(300*time.Millisecond, Events{RTORetrans: 1, OtherRetrans: 3})
	r.RecordEvents(time.Millisecond, Events{FastOpenRetrans: 1})
	r.RecordEvents(time.Millisecond, Events{ResetsReceived: 1})
	r.RecordEvents(2*time.Second, Events{RTORetrans: 1, Drops: [DropSlots]DropCount{{Reason: 15, Count: 2}, {Reason: 6, Count: 1}}})
	// A slot that has a reason but no drop is not an event.
	r.RecordEvents(time.Millisecond, Events{Drops: [DropSlots]DropCount{{Reason: 15}}})
	r.RecordUntracked()
	r.RecordUnmatched(Events{SYNRetrans: 3, RTORetrans: 1, ResetsReceived: 2, Drops: [DropSlots]DropCount{{Reason: 15, Count: 1}}, OtherDrops: 4})

	want := EventSummary{
		RoundTrips:        5,
		RetransRoundTrips: RetransCounts{SYN: 1, FastOpen: 1, RTO: 2, Other: 1},
		WithEvents:        LatencySummary{Count: 5, P99: 2 * time.Second, Max: 2 * time.Second},
		WithoutEvents:     LatencySummary{Count: 3, P99: time.Millisecond, Max: time.Millisecond},
		ResetsReceived:    3,
		DropRoundTrips:    1,
		Drops:             map[uint32]int64{15: 3, 6: 1},
		OtherDrops:        4,
		Unmatched:         RetransCounts{SYN: 3, RTO: 1},
		Untracked:         1,
	}
	s := r.Events()
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("events = %+v\nwant %+v", s, want)
	}
	// The summary is a copy.
	s.Drops[15] = 0
	if r.Events().Drops[15] != 3 {
		t.Fatal("the summary shares the drops of the Recorder")
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

func TestEventsSubDrops(t *testing.T) {
	// Between the readings, a second slot was taken and the first counted
	// again; the third was taken but not yet counted.
	before := Events{Drops: [DropSlots]DropCount{{Reason: 15, Count: 2}}}
	after := Events{
		Drops:      [DropSlots]DropCount{{Reason: 15, Count: 3}, {Reason: 6, Count: 1}, {Reason: 77}},
		OtherDrops: 2,
	}
	want := Events{Drops: [DropSlots]DropCount{{Reason: 15, Count: 1}, {Reason: 6, Count: 1}}, OtherDrops: 2}
	if d := after.Sub(before); d != want {
		t.Fatalf("Sub = %+v\nwant %+v", d, want)
	}
	if n := after.DroppedPackets(); n != 6 {
		t.Fatalf("DroppedPackets = %d, want 6", n)
	}
	// Unchanged drops: no slot keeps its reason, and nothing happened.
	if d := after.Sub(after); d != (Events{}) || !d.IsZero() {
		t.Fatalf("Sub of the same reading = %+v, want zero", d)
	}
	if (Events{Drops: [DropSlots]DropCount{{Reason: 77}}}).IsZero() != true {
		t.Fatal("a slot without drops is an event")
	}
}
