//go:build unix

package client

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yuuki/tcpulse/internal/server"
	"github.com/yuuki/tcpulse/internal/stats"
)

// fakeEvents is an EventReader that answers from a script: the events of
// the conn-th connection (in the order of their first read) at its call-th
// read. It has no generation unless generation is set.
type fakeEvents struct {
	script     func(conn, call int) (stats.Events, error)
	generation func() uint64

	mu    sync.Mutex
	conns map[string]int
	calls map[string]int
}

func newFakeEvents(script func(conn, call int) (stats.Events, error)) *fakeEvents {
	return &fakeEvents{script: script, conns: map[string]int{}, calls: map[string]int{}}
}

func (f *fakeEvents) Generation() (uint64, bool) {
	if f.generation == nil {
		return 0, false
	}
	return f.generation(), true
}

// ReadEvents tells the sockets apart by their identity (device and inode),
// which the duplicates of a descriptor share.
func (f *fakeEvents) ReadEvents(fd int) (stats.Events, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return stats.Events{}, err
	}
	key := fmt.Sprint(st.Dev, st.Ino)
	f.mu.Lock()
	n, ok := f.conns[key]
	if !ok {
		n = len(f.conns)
		f.conns[key] = n
	}
	call := f.calls[key]
	f.calls[key]++
	f.mu.Unlock()
	return f.script(n, call)
}

func TestRunEventsPersistent(t *testing.T) {
	// One connection, 10 round trips. Read 0 follows the dial, read k the
	// k-th round trip, read 11 comes at the close.
	fake := newFakeEvents(func(_, call int) (stats.Events, error) {
		ev := stats.Events{SYNRetrans: 1} // during the dial: no round trip
		if call >= 3 {
			ev.OtherRetrans = 2 // during the 3rd round trip
		}
		if call >= 7 {
			ev.RTORetrans = 1 // during the 7th
		}
		if call >= 11 {
			ev.RTORetrans, ev.ResetsReceived = 3, 1 // after the last round trip
		}
		return ev, nil
	})
	cfg := config(ProtocolTCP, FlavorPersistent)
	cfg.Connections = 1
	cfg.Events = fake
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, echoServer(t, server.ProtocolTCP), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev := rec.Events()
	if ev.RoundTrips != 2 || ev.RetransRoundTrips != (stats.RetransCounts{Other: 1, RTO: 1}) {
		t.Errorf("round trips with events = %d, %+v; want the 3rd and the 7th", ev.RoundTrips, ev.RetransRoundTrips)
	}
	if ev.WithEvents.Count != 2 || ev.WithoutEvents.Count != 8 || ev.Untracked != 0 {
		t.Errorf("with events %+v, without %+v, untracked %d; want 2, 8 and 0", ev.WithEvents, ev.WithoutEvents, ev.Untracked)
	}
	if ev.Unmatched != (stats.RetransCounts{SYN: 1, RTO: 2}) || ev.ResetsReceived != 1 {
		t.Errorf("unmatched %+v, resets %d; want the SYN of the dial and the events after the last round trip", ev.Unmatched, ev.ResetsReceived)
	}
	if total := rec.Total(time.Second); total.Count != 10 {
		t.Errorf("%d round trips, want 10", total.Count)
	}
}

func TestRunEventsGeneration(t *testing.T) {
	// The generation changes once, during the 4th round trip: the cursor
	// reads after the dial (always), after the 4th round trip (changed),
	// and nowhere else.
	var roundTrips atomic.Int64
	fake := newFakeEvents(func(_, call int) (stats.Events, error) {
		if call >= 1 {
			return stats.Events{OtherRetrans: 1}, nil
		}
		return stats.Events{}, nil
	})
	fake.generation = func() uint64 {
		if roundTrips.Load() >= 4 {
			return 1
		}
		return 0
	}
	cfg := config(ProtocolTCP, FlavorPersistent)
	cfg.Connections = 1
	cfg.Events = &countingReader{EventReader: fake, roundTrips: &roundTrips}
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, echoServer(t, server.ProtocolTCP), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev := rec.Events()
	if ev.RoundTrips != 1 || ev.RetransRoundTrips.Other != 1 || ev.WithoutEvents.Count != 9 || ev.Unmatched.Total() != 0 {
		t.Fatalf("events = %+v, want the 4th round trip only", ev)
	}
	for key, calls := range fake.calls {
		if calls != 2 {
			t.Errorf("connection %s read %d times, want twice", key, calls)
		}
	}
}

// countingReader counts the round trips of a single connection: every take
// but the first (after the dial) follows one.
type countingReader struct {
	EventReader
	roundTrips *atomic.Int64
	takes      atomic.Int64
}

func (c *countingReader) Generation() (uint64, bool) {
	if c.takes.Add(1) > 1 {
		c.roundTrips.Add(1)
	}
	return c.EventReader.Generation()
}

func TestRunEventsEphemeral(t *testing.T) {
	// Every other connection retransmitted its SYN: the round trip
	// includes the dial.
	fake := newFakeEvents(func(conn, _ int) (stats.Events, error) {
		if conn%2 == 0 {
			return stats.Events{SYNRetrans: 1}, nil
		}
		return stats.Events{}, nil
	})
	cfg := config(ProtocolTCP, FlavorEphemeral)
	cfg.Events = fake
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, echoServer(t, server.ProtocolTCP), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev := rec.Events()
	if ev.RoundTrips != 5 || ev.RetransRoundTrips.SYN != 5 || ev.WithoutEvents.Count != 5 || ev.Unmatched.Total() != 0 {
		t.Fatalf("events = %+v, want 5 round trips with a SYN retransmission and 5 without", ev)
	}
}

func TestRunEventsUntracked(t *testing.T) {
	fake := newFakeEvents(func(int, int) (stats.Events, error) { return stats.Events{}, errors.New("not traced") })
	cfg := config(ProtocolTCP, FlavorPersistent)
	cfg.Events = fake
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, echoServer(t, server.ProtocolTCP), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Unknown, not zero: no round trip counts as one without events.
	if ev := rec.Events(); ev.Untracked != 20 || ev.WithoutEvents.Count != 0 || ev.WithEvents.Count != 0 {
		t.Fatalf("events = %+v, want 20 untracked round trips", ev)
	}
	// A connection whose first read failed is not read again.
	for key, calls := range fake.calls {
		if calls != 1 {
			t.Errorf("connection %s read %d times, want once", key, calls)
		}
	}
}

func TestRunEventsOfCutOffRequests(t *testing.T) {
	// The server never answers: the end of the run cuts off every request,
	// and the events of the connections belong to no round trip.
	fake := newFakeEvents(func(_, call int) (stats.Events, error) {
		if call == 0 {
			return stats.Events{}, nil
		}
		return stats.Events{RTORetrans: 2}, nil
	})
	cfg := config(ProtocolTCP, FlavorPersistent)
	cfg.Duration = 500 * time.Millisecond
	cfg.Events = fake
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, silentServer(t), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ev := rec.Events(); ev.Unmatched != (stats.RetransCounts{RTO: 4}) || ev.RoundTrips != 0 || ev.Untracked != 0 {
		t.Fatalf("events = %+v, want 2 unmatched timeout retransmissions per connection", ev)
	}
}

// fullListener listens on a free loopback port with an accept queue of one
// connection that it fills and never accepts: the kernel drops the SYNs of
// further connections, whose dials wait until they are cut off.
func fullListener(t *testing.T) string {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", sa.(*syscall.SockaddrInet4).Port)
	first, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	return addr
}

func TestRunEventsOfFailedDials(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the end of the run")
	}
	for _, tc := range []struct {
		name     string
		flavor   string
		wantSYNs int64
	}{
		// One connection, whose dial never completes.
		{"persistent", FlavorPersistent, 2},
		// rate × duration dials, none completes.
		{"ephemeral", FlavorEphemeral, 2 * 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every socket retransmitted its SYN twice.
			fake := newFakeEvents(func(int, int) (stats.Events, error) { return stats.Events{SYNRetrans: 2}, nil })
			cfg := config(ProtocolTCP, tc.flavor)
			cfg.Connections = 1
			cfg.Duration = 500 * time.Millisecond
			cfg.Events = fake
			rec := stats.NewRecorder()
			if err := Run(t.Context(), cfg, fullListener(t), rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if total := rec.Total(time.Second); total.Count != 0 {
				t.Fatalf("%d round trips, want none", total.Count)
			}
			// The events of the sockets the dialer closed were read through
			// their duplicates (or, when TCP Fast Open deferred the SYN,
			// at the close of the connection).
			if ev := rec.Events(); ev.Unmatched != (stats.RetransCounts{SYN: tc.wantSYNs}) || ev.Untracked != 0 {
				t.Fatalf("events = %+v, want %d unmatched SYN retransmissions", ev, tc.wantSYNs)
			}
		})
	}
}

func TestSameSocket(t *testing.T) {
	addr := echoServer(t, server.ProtocolTCP)
	a, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	raw, err := a.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var dup int
	var dupErr error
	if err := raw.Control(func(fd uintptr) { dup, dupErr = dupSocket(fd) }); err != nil || dupErr != nil {
		t.Fatal(err, dupErr)
	}
	defer closeFD(dup)
	if !sameSocket(dup, a) || sameSocket(dup, b) {
		t.Fatal("sameSocket does not tell the socket of a duplicate")
	}
}
