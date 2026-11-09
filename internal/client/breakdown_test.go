package client

import (
	"testing"
	"time"

	"github.com/yuuki/tcpulse/internal/stats"
)

// fakeStamps hands the splitter the transmit timestamps of pending at the
// next Drain, as the error queue does.
type fakeStamps struct {
	tx, pending map[uint32]time.Time
	outOfOrder  uint32
}

func newFakeStamps() *fakeStamps {
	return &fakeStamps{tx: map[uint32]time.Time{}, pending: map[uint32]time.Time{}}
}

func (f *fakeStamps) Drain() error {
	for k, v := range f.pending {
		f.tx[k] = v
	}
	clear(f.pending)
	return nil
}

func (f *fakeStamps) Transmitted(key uint32) (time.Time, bool) {
	ts, ok := f.tx[key]
	return ts, ok
}

func (f *fakeStamps) Forget(key uint32) {
	for k := range f.tx {
		if int32(k-key) <= 0 {
			delete(f.tx, k)
		}
	}
}

func (f *fakeStamps) OutOfOrder() (uint32, bool, error) { return f.outOfOrder, true, nil }

func TestSplitter(t *testing.T) {
	rec := stats.NewRecorder()
	f := newFakeStamps()
	s := &splitter{run: &run{rec: rec, logger: discard, addr: "x"}, stamps: f, ooo: true}
	t0 := time.Unix(1700000000, 0)
	at := func(us int) time.Time { return t0.Add(time.Duration(us) * time.Microsecond) }

	// Messages of 10 bytes: the last byte of the n-th has key 10n-1.
	// 1: sent 30µs after the start, received 50µs later, read 20µs later.
	f.pending[9] = at(30)
	s.roundTrip(at(0), at(100), at(80), 10)
	// 2: its transmit timestamp comes after its end: it waits.
	s.roundTrip(at(200), at(300), at(280), 10)
	f.pending[19] = at(230)
	// 3: no receive timestamp; the second gets its transmit timestamp.
	s.roundTrip(at(400), at(500), time.Time{}, 10)
	// 4: packets of its echo out of order.
	f.outOfOrder++
	f.pending[39] = at(630)
	s.roundTrip(at(600), at(700), at(680), 10)
	// 5: sent before it started: a negative part.
	f.pending[49] = at(790)
	s.roundTrip(at(800), at(900), at(880), 10)
	// 6: its transmit timestamp never comes, which the close settles.
	s.roundTrip(at(1000), at(1100), at(1080), 10)
	s.close()

	b := rec.Breakdown()
	if b.RoundTrips != 2 || b.Missing != 2 || b.OutOfOrder != 1 || b.Negative != 1 {
		t.Fatalf("breakdown = %+v, want 2 split, 2 missing, 1 out of order, 1 negative", b)
	}
	near := func(got time.Duration, us int) bool {
		want := time.Duration(us) * time.Microsecond
		return got >= want && got <= want+want/64
	}
	if !near(b.Send.P99, 30) || !near(b.Network.P99, 50) || !near(b.Receive.P99, 20) {
		t.Fatalf("parts = %+v, want 30, 50 and 20µs", b)
	}
	if len(f.tx) != 0 {
		t.Fatalf("timestamps kept after the close: %v", f.tx)
	}
}

// TestSplitterNotOn counts the round trips of a connection whose timestamps
// are not on (yet): missing.
func TestSplitterNotOn(t *testing.T) {
	rec := stats.NewRecorder()
	s := &splitter{run: &run{rec: rec, logger: discard, addr: "x"}, broken: true}
	s.roundTrip(time.Now(), time.Now(), time.Now(), 10)
	s.close()
	var nilSplitter *splitter
	nilSplitter.roundTrip(time.Now(), time.Now(), time.Now(), 10)
	nilSplitter.close()
	if b := rec.Breakdown(); b.Missing != 1 || b.RoundTrips != 0 {
		t.Fatalf("breakdown = %+v, want one missing", b)
	}
}
