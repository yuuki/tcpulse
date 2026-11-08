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

	total := r.Total()
	if total.Count != 40 || total.Min != time.Millisecond || total.Max != 3*time.Millisecond {
		t.Fatalf("total = %+v, want 40 round trips between 1ms and 3ms", total)
	}
	if total.Rate != 40.0/3 {
		t.Fatalf("total rate = %v, want 40/3", total.Rate)
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
	clock.advance(time.Second)
	if s := r.Total(); s != (Summary{}) {
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
	if got := r.Total().Count; got != 8000 {
		t.Fatalf("total count = %d, want 8000", got)
	}
}

func TestSet(t *testing.T) {
	separate := NewSet([]string{"a:1", "b:2", "a:1"}, false)
	if separate.For("a:1") == nil || separate.For("a:1") == separate.For("b:2") {
		t.Fatal("peers do not have a Recorder each")
	}
	if separate.For("c:3") != nil {
		t.Fatal("unknown peer has a Recorder")
	}

	merged := NewSet([]string{"a:1", "b:2"}, true)
	if merged.For("a:1") == nil || merged.For("a:1") != merged.For("b:2") || merged.For("") != merged.For("a:1") {
		t.Fatal("merged peers do not share a Recorder")
	}
}
