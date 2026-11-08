package client

import (
	"context"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net"
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
			// Every connection sent at least its SYN and its messages.
			if conns.Conns != tc.wantConns || conns.SegsOut < 2*tc.wantConns || conns.RTTMax <= 0 {
				t.Fatalf("connections = %+v, want %d with segments and RTTs", conns, tc.wantConns)
			}
			if conns.RetransConns > conns.Conns || conns.FastOpenConns > conns.Conns || conns.RTTMean > conns.RTTMax {
				t.Fatalf("inconsistent connections: %+v", conns)
			}
			// Reading TCP_INFO changes nothing to the round trips.
			if total := rec.Total(time.Second); total.Count != tc.wantCount {
				t.Fatalf("%d round trips, want %d", total.Count, tc.wantCount)
			}
		})
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
