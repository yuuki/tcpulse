package client

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuuki/tcpulse/internal/server"
	"github.com/yuuki/tcpulse/internal/stats"
)

var discard = slog.New(slog.DiscardHandler)

// echoServer serves protocol on a free loopback port until the test ends
// and returns its address.
func echoServer(t *testing.T, protocol string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := server.Listen(ctx, server.Config{Addrs: []string{"127.0.0.1:0"}, Protocol: protocol, Logger: discard})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	if protocol == server.ProtocolUDP {
		return srv.UDPAddrs()[0].String()
	}
	return srv.TCPAddrs()[0].String()
}

// closedPort returns a loopback address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func config(protocol, flavor string) Config {
	return Config{
		Protocol:     protocol,
		Flavor:       flavor,
		Connections:  2,
		Rate:         10,
		Duration:     time.Second,
		MessageBytes: 64,
		Logger:       discard,
	}
}

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protocol  string
		flavor    string
		wantCount int64
	}{
		// rate × duration × connections
		{"tcp persistent", ProtocolTCP, FlavorPersistent, 20},
		// rate × duration
		{"tcp ephemeral", ProtocolTCP, FlavorEphemeral, 10},
		{"udp", ProtocolUDP, FlavorPersistent, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := echoServer(t, tc.protocol)
			rec := stats.NewRecorder()
			if err := Run(t.Context(), config(tc.protocol, tc.flavor), addr, rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			total := rec.Total(time.Second)
			if total.Count != tc.wantCount {
				t.Fatalf("%d round trips, want %d", total.Count, tc.wantCount)
			}
			if total.Min <= 0 || total.Max < total.Min || total.P99 > total.Max {
				t.Fatalf("inconsistent latencies: %+v", total)
			}
		})
	}
}

func TestRunTCPInfo(t *testing.T) {
	if !TCPInfoSupported {
		t.Skip("TCP_INFO is read on Linux only")
	}
	for _, tc := range []struct {
		name      string
		flavor    string
		wantConns int64
		wantCount int64
	}{
		// One per connection, read when it ends.
		{"persistent", FlavorPersistent, 2, 20},
		// One per message.
		{"ephemeral", FlavorEphemeral, 10, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := echoServer(t, server.ProtocolTCP)
			cfg := config(ProtocolTCP, tc.flavor)
			cfg.TCPInfo = true
			rec := stats.NewRecorder()
			if err := Run(t.Context(), cfg, addr, rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			conns := rec.Conns()
			// Every message is at least one segment.
			if conns.Conns != tc.wantConns || conns.SegsOut < tc.wantCount || conns.RTTMax <= 0 {
				t.Fatalf("connections = %+v, want %d with segments and RTTs", conns, tc.wantConns)
			}
			if conns.RetransConns > conns.Conns || conns.FastOpenConns > conns.Conns || conns.RTTMean > conns.RTTMax {
				t.Fatalf("inconsistent connections: %+v", conns)
			}
			// Reading TCP_INFO loses no round trip.
			if total := rec.Total(time.Second); total.Count != tc.wantCount {
				t.Fatalf("%d round trips, want %d", total.Count, tc.wantCount)
			}
		})
	}
}

func TestRunTCPInfoOfUnansweredConnections(t *testing.T) {
	if !TCPInfoSupported {
		t.Skip("TCP_INFO is read on Linux only")
	}
	for _, tc := range []struct {
		name      string
		flavor    string
		wantConns int64
	}{
		{"persistent", FlavorPersistent, 2},
		// rate × duration
		{"ephemeral", FlavorEphemeral, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config(ProtocolTCP, tc.flavor)
			cfg.Duration = 500 * time.Millisecond
			cfg.TCPInfo = true
			rec := stats.NewRecorder()
			if err := Run(t.Context(), cfg, silentServer(t), rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			// The end of the run cuts off every request: no round trip,
			// but every connection is read.
			if total := rec.Total(time.Second); total.Count != 0 {
				t.Fatalf("%d round trips, want none", total.Count)
			}
			if conns := rec.Conns(); conns.Conns != tc.wantConns || conns.SegsOut < tc.wantConns {
				t.Fatalf("connections = %+v, want %d", conns, tc.wantConns)
			}
		})
	}
}

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

func (f *fakeEvents) ReadEvents(conn net.Conn) (stats.Events, error) {
	key := conn.LocalAddr().String()
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
			ev.FastRetrans = 2 // during the 3rd round trip
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
	if ev.RoundTrips != 2 || ev.RetransRoundTrips != (stats.RetransCounts{Fast: 1, RTO: 1}) {
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
			return stats.Events{FastRetrans: 1}, nil
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
	if ev.RoundTrips != 1 || ev.RetransRoundTrips.Fast != 1 || ev.WithoutEvents.Count != 9 || ev.Unmatched.Total() != 0 {
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

func TestRunWithoutTCPInfo(t *testing.T) {
	addr := echoServer(t, server.ProtocolTCP)
	rec := stats.NewRecorder()
	if err := Run(t.Context(), config(ProtocolTCP, FlavorEphemeral), addr, rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if conns := rec.Conns(); conns != (stats.ConnSummary{}) {
		t.Fatalf("connections = %+v, want none recorded", conns)
	}
}

func TestRunStopsWhenCanceled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol string
		flavor   string
	}{
		{"tcp persistent", ProtocolTCP, FlavorPersistent},
		{"tcp ephemeral", ProtocolTCP, FlavorEphemeral},
		{"udp", ProtocolUDP, FlavorPersistent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := echoServer(t, tc.protocol)
			cfg := config(tc.protocol, tc.flavor)
			cfg.Duration = time.Minute
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()

			start := time.Now()
			if err := Run(ctx, cfg, addr, stats.NewRecorder()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("Run returned %v after its context was canceled", elapsed)
			}
		})
	}
}

func TestRunConnectionRefused(t *testing.T) {
	for _, flavor := range []string{FlavorPersistent, FlavorEphemeral} {
		t.Run(flavor, func(t *testing.T) {
			t.Parallel()
			if err := Run(t.Context(), config(ProtocolTCP, flavor), closedPort(t), stats.NewRecorder()); err == nil {
				t.Fatal("Run succeeded against a closed port")
			}
		})
	}
}

func TestRunInvalidConfig(t *testing.T) {
	if err := Run(t.Context(), config("sctp", FlavorPersistent), "127.0.0.1:1", stats.NewRecorder()); err == nil {
		t.Fatal("Run accepted an unknown protocol")
	}
	if err := Run(t.Context(), config(ProtocolTCP, "bursty"), "127.0.0.1:1", stats.NewRecorder()); err == nil {
		t.Fatal("Run accepted an unknown flavor")
	}
}

// slowEchoServer echoes every message it receives on TCP delay after it
// arrives, until the test ends.
func slowEchoServer(t *testing.T, size int, delay time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, size)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
					time.Sleep(delay)
					if _, err := conn.Write(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRunEndsDuringARequest(t *testing.T) {
	for _, flavor := range []string{FlavorPersistent, FlavorEphemeral} {
		t.Run(flavor, func(t *testing.T) {
			t.Parallel()
			cfg := config(ProtocolTCP, flavor)
			cfg.Connections = 1
			cfg.Rate = 4
			cfg.MessageBytes = 16
			// Messages at 0, 250, 500 and 750ms (persistent: as soon as the
			// previous echo is back); the last echo would arrive after the
			// end of the run at 1s.
			addr := slowEchoServer(t, cfg.MessageBytes, 300*time.Millisecond)
			rec := stats.NewRecorder()
			if err := Run(t.Context(), cfg, addr, rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := rec.Total(time.Second).Count; got != 3 {
				t.Fatalf("%d round trips, want 3: the last request was cut off", got)
			}
		})
	}
}

func TestRunLostDatagrams(t *testing.T) {
	// A UDP socket that never answers.
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	cfg := config(ProtocolUDP, FlavorPersistent)
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, pc.LocalAddr().String(), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rec.Total(time.Second).Count; got != 0 {
		t.Fatalf("%d round trips without an answer", got)
	}
}

// splitEchoServer echoes every message it receives on TCP in two halves,
// the second one delay after the first, until the test ends.
func splitEchoServer(t *testing.T, size int, delay time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, size)
				for {
					if _, err := io.ReadFull(conn, buf); err != nil {
						return
					}
					if _, err := conn.Write(buf[:size/2]); err != nil {
						return
					}
					time.Sleep(delay)
					if _, err := conn.Write(buf[size/2:]); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRunWaitsForTheWholeEcho(t *testing.T) {
	const delay = 100 * time.Millisecond
	for _, flavor := range []string{FlavorPersistent, FlavorEphemeral} {
		t.Run(flavor, func(t *testing.T) {
			t.Parallel()
			cfg := config(ProtocolTCP, flavor)
			cfg.Connections = 1
			cfg.Rate = 4
			cfg.Duration = 2 * time.Second
			cfg.MessageBytes = 1000
			addr := splitEchoServer(t, cfg.MessageBytes, delay)
			rec := stats.NewRecorder()
			if err := Run(t.Context(), cfg, addr, rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			total := rec.Total(time.Second)
			if total.Count != 8 {
				t.Fatalf("%d round trips, want 8", total.Count)
			}
			if total.Min < delay {
				t.Fatalf("fastest round trip took %v, before the whole echo (%v) could arrive", total.Min, delay)
			}
		})
	}
}

func TestRunLargeMessages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protocol  string
		flavor    string
		size      int
		wantCount int64
	}{
		{"tcp persistent", ProtocolTCP, FlavorPersistent, 1 << 20, 20},
		{"tcp ephemeral", ProtocolTCP, FlavorEphemeral, 1 << 20, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config(tc.protocol, tc.flavor)
			cfg.MessageBytes = tc.size
			rec := stats.NewRecorder()
			if err := Run(t.Context(), cfg, echoServer(t, tc.protocol), rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := rec.Total(time.Second).Count; got != tc.wantCount {
				t.Fatalf("%d round trips, want %d", got, tc.wantCount)
			}
		})
	}
}

// resettingServer reads a message of size on every TCP connection, then
// resets the connection instead of echoing it, until the test ends.
func resettingServer(t *testing.T, size int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.ReadFull(conn, make([]byte, size))
				_ = conn.(*net.TCPConn).SetLinger(0)
				_ = conn.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRunDoesNotRecordFailedRequests(t *testing.T) {
	cfg := config(ProtocolTCP, FlavorEphemeral)
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, resettingServer(t, cfg.MessageBytes), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rec.Total(time.Second).Count; got != 0 {
		t.Fatalf("%d reset requests recorded as round trips", got)
	}
}

func TestMessages(t *testing.T) {
	for _, tc := range []struct {
		rate     int
		duration time.Duration
		want     int64
		ok       bool
	}{
		{100, 10 * time.Second, 1000, true},
		{1, time.Second, 1, true},
		{1, 999 * time.Millisecond, 0, true},
		{100, 500 * time.Millisecond, 50, true},
		{3, 1500 * time.Millisecond, 4, true},
		{40000, 2 * time.Minute, 4800000, true},
		{math.MaxInt32, 24 * time.Hour, math.MaxInt32 * 86400, true},
		{0, time.Hour, 0, true},
		// 2^31 × about 7.2e9 s overflows int64.
		{2000000000, 2000000 * time.Hour, 0, false},
		{math.MaxInt32, math.MaxInt64, 0, false},
		{-1, time.Second, 0, false},
		{1, -time.Second, 0, false},
	} {
		got, ok := Messages(tc.rate, tc.duration)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Messages(%d, %v) = %d, %v; want %d, %v", tc.rate, tc.duration, got, ok, tc.want, tc.ok)
		}
	}
}

func FuzzMessages(f *testing.F) {
	f.Add(int64(100), int64(10*time.Second))
	f.Add(int64(math.MaxInt32), int64(math.MaxInt64))
	f.Add(int64(3), int64(1500*time.Millisecond))
	f.Fuzz(func(t *testing.T, rate, d int64) {
		if rate < 0 || rate > math.MaxInt32 {
			return
		}
		got, ok := Messages(int(rate), time.Duration(d))
		// Exact: rate × d / 1e9, rounded down.
		want := new(big.Int).Mul(big.NewInt(rate), big.NewInt(d))
		want.Quo(want, big.NewInt(int64(time.Second)))
		switch {
		case d < 0:
			if ok {
				t.Fatalf("Messages(%d, %d) accepted a negative duration", rate, d)
			}
		case want.IsInt64():
			if !ok || got != want.Int64() {
				t.Fatalf("Messages(%d, %d) = %d, %v; want %v", rate, d, got, ok, want)
			}
		default:
			if ok {
				t.Fatalf("Messages(%d, %d) = %d, want an overflow (%v)", rate, d, got, want)
			}
		}
	})
}

func TestRunSubsecondDuration(t *testing.T) {
	cfg := config(ProtocolTCP, FlavorPersistent)
	cfg.Rate = 4
	cfg.Duration = 1500 * time.Millisecond
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, echoServer(t, ProtocolTCP), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Messages at 0, 250, ... 1250ms on each connection.
	if got := rec.Total(cfg.Duration).Count; got != 12 {
		t.Fatalf("%d round trips, want 12 (2 connections × 4/s × 1.5s)", got)
	}
}

// lateContext has passed its deadline but does not report it yet, as a
// context does for a moment after the I/O of its connections timed out.
type lateContext struct {
	context.Context
	deadline time.Time
}

func (c lateContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (lateContext) Err() error                    { return nil }

func TestCutOff(t *testing.T) {
	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	var d net.Dialer
	_, dialTimeout := d.DialContext(expired, "tcp", closedPort(t))

	conn, err := net.Dial("tcp", echoServer(t, ProtocolTCP))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now())
	_, readTimeout := conn.Read(make([]byte, 1))

	late := lateContext{Context: t.Context(), deadline: time.Now().Add(-time.Millisecond)}
	early := lateContext{Context: t.Context(), deadline: time.Now().Add(time.Hour)}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"read timeout at the deadline", late, readTimeout, true},
		{"dial timeout at the deadline", late, dialTimeout, true},
		{"read timeout before the deadline", early, readTimeout, false},
		{"other error at the deadline", late, io.ErrUnexpectedEOF, false},
		{"any error after the end", expired, io.ErrUnexpectedEOF, true},
		{"any error during the run", t.Context(), io.ErrUnexpectedEOF, false},
	} {
		if got := cutOff(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: cutOff(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// silentServer accepts TCP connections and reads from them without ever
// answering, until the test ends.
func silentServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRunStopsWhenCanceledDuringARequest(t *testing.T) {
	blackhole, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Not defer: the parallel subtests run after this function returns.
	t.Cleanup(func() { _ = blackhole.Close() })

	for _, tc := range []struct {
		name     string
		protocol string
		flavor   string
		addr     string
	}{
		{"tcp persistent", ProtocolTCP, FlavorPersistent, silentServer(t)},
		{"tcp ephemeral", ProtocolTCP, FlavorEphemeral, silentServer(t)},
		{"udp", ProtocolUDP, FlavorPersistent, blackhole.LocalAddr().String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config(tc.protocol, tc.flavor)
			cfg.Duration = time.Minute
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()

			// Every request waits for an echo that never comes: canceling
			// the run must interrupt them, not wait for its end.
			start := time.Now()
			rec := stats.NewRecorder()
			if err := Run(ctx, cfg, tc.addr, rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("Run returned %v after its context was canceled at 300ms", elapsed)
			}
			if got := rec.Total(time.Second).Count; got != 0 {
				t.Fatalf("%d round trips without an echo", got)
			}
		})
	}
}

// TestRunMessagesLargerThanSocketBuffers sends messages that do not fit in
// the socket buffers of both sides, which only complete when the echo is
// read while the message is written. Serial and light: copying 16 MiB under
// the race detector is slow.
func TestRunMessagesLargerThanSocketBuffers(t *testing.T) {
	for _, flavor := range []string{FlavorPersistent, FlavorEphemeral} {
		t.Run(flavor, func(t *testing.T) {
			cfg := config(ProtocolTCP, flavor)
			cfg.Connections = 1
			cfg.Rate = 2
			cfg.Duration = 2 * time.Second
			cfg.MessageBytes = 16 << 20
			rec := stats.NewRecorder()
			if err := Run(t.Context(), cfg, echoServer(t, ProtocolTCP), rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := rec.Total(time.Second).Count; got != 4 {
				t.Fatalf("%d round trips, want 4", got)
			}
		})
	}
}
