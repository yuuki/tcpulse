package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

var discard = slog.New(slog.DiscardHandler)

// start serves protocol on a free loopback port until the test ends.
func start(t *testing.T, protocol string) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := Listen(ctx, Config{Addrs: []string{"127.0.0.1:0"}, Protocol: protocol, Logger: discard})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after its context was canceled")
		}
	})
	return srv
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func roundTrip(t *testing.T, conn net.Conn, msg []byte) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("echo of %d bytes differs from the message", len(msg))
	}
}

func TestTCPEcho(t *testing.T) {
	srv := start(t, ProtocolTCP)
	conn, err := net.Dial("tcp", srv.TCPAddrs()[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, size := range []int{1, 64, tcpBufferSize, tcpBufferSize + 1, 1 << 20} {
		roundTrip(t, conn, randomBytes(size))
	}
}

func TestTCPEchoManyConnections(t *testing.T) {
	srv := start(t, ProtocolTCP)
	addr := srv.TCPAddrs()[0].String()
	conns := make([]net.Conn, 20)
	for i := range conns {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conns[i] = conn
	}
	for range 3 {
		for _, conn := range conns {
			roundTrip(t, conn, randomBytes(128))
		}
	}
}

func TestUDPEcho(t *testing.T) {
	srv := start(t, ProtocolUDP)
	conn, err := net.Dial("udp4", srv.UDPAddrs()[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// 65507 bytes is the largest UDP payload over IPv4, but macOS sends at
	// most net.inet.udp.maxdgram bytes, 9216 by default.
	sizes := []int{1, 64, 1400, 4097, 9000, 65507}
	if runtime.GOOS == "darwin" {
		sizes = sizes[:len(sizes)-1]
	}
	for _, size := range sizes {
		msg := randomBytes(size)
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(msg); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, size+1)
		n, err := conn.Read(got)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got[:n], msg) {
			t.Fatalf("echo of a %d-byte datagram is %d bytes and differs", size, n)
		}
	}
}

func TestListenProtocols(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		tcp, udp int
	}{
		{ProtocolTCP, 1, 0},
		{ProtocolUDP, 0, 1},
		{ProtocolAll, 1, 1},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			srv := start(t, tc.protocol)
			if got := len(srv.TCPAddrs()); got != tc.tcp {
				t.Errorf("%d TCP listeners, want %d", got, tc.tcp)
			}
			if got := len(srv.UDPAddrs()); got != tc.udp {
				t.Errorf("%d UDP sockets, want %d", got, tc.udp)
			}
		})
	}
}

func TestListenMultipleAddresses(t *testing.T) {
	srv, err := Listen(t.Context(), Config{
		Addrs:    []string{"127.0.0.1:0", "127.0.0.1:0"},
		Protocol: ProtocolAll,
		Logger:   discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.close()
	if len(srv.TCPAddrs()) != 2 || len(srv.UDPAddrs()) != 2 {
		t.Fatalf("listening on %v and %v, want two addresses each", srv.TCPAddrs(), srv.UDPAddrs())
	}
}

func TestListenError(t *testing.T) {
	_, err := Listen(t.Context(), Config{
		Addrs:    []string{"127.0.0.1:0", "256.0.0.1:0"},
		Protocol: ProtocolTCP,
		Logger:   discard,
	})
	if err == nil {
		t.Fatal("listening on an invalid address succeeded")
	}
}

func TestServeClosesConnectionsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := Listen(ctx, Config{Addrs: []string{"127.0.0.1:0"}, Protocol: ProtocolAll, Logger: discard})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	conn, err := net.Dial("tcp", srv.TCPAddrs()[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	roundTrip(t, conn, []byte("hello"))

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context was canceled")
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection still open after the server stopped")
	}
}

type acceptResult struct {
	conn net.Conn
	err  error
}

// fakeListener returns the connections and errors sent to it.
type fakeListener struct {
	results chan acceptResult
	closed  chan struct{}
	once    sync.Once
}

func newFakeListener() *fakeListener {
	return &fakeListener{results: make(chan acceptResult), closed: make(chan struct{})}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	select {
	case r := <-l.results:
		return r.conn, r.err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *fakeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func TestServeTCPSurvivesFailures(t *testing.T) {
	ln := newFakeListener()
	s := &Server{logger: discard}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.serveTCP(ctx, ln, newSocketStats()) }()

	for _, err := range []error{
		&net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)},
		// A network error of the connection being accepted (accept(2)).
		&net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EPROTO)},
	} {
		select {
		case ln.results <- acceptResult{err: err}:
		case err := <-done:
			t.Fatalf("serveTCP returned %v", err)
		}
	}

	// A connection whose socket options cannot be set (net.Pipe is not
	// TCP) is still served on Linux, and so are the following ones.
	for range 2 {
		local, remote := net.Pipe()
		select {
		case ln.results <- acceptResult{conn: remote}:
		case err := <-done:
			t.Fatalf("serveTCP returned %v", err)
		}
		roundTrip(t, local, []byte("still serving"))
		_ = local.Close()
	}

	cancel()
	_ = ln.Close()
	if err := <-done; err != nil {
		t.Fatalf("serveTCP returned %v after the server stopped", err)
	}
}

func TestServeTCPStopsOnPermanentError(t *testing.T) {
	ln := newFakeListener()
	s := &Server{logger: discard}
	done := make(chan error, 1)
	go func() { done <- s.serveTCP(t.Context(), ln, newSocketStats()) }()

	// A connection is open when the listener fails: it must not keep the
	// listener from returning.
	local, remote := net.Pipe()
	defer local.Close()
	ln.results <- acceptResult{conn: remote}
	roundTrip(t, local, []byte("open"))

	ln.results <- acceptResult{err: &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EINVAL)}}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serveTCP ignored a permanent accept error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveTCP did not return after a permanent accept error while a connection was open")
	}
	_ = local.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := local.Read(make([]byte, 1)); err == nil {
		t.Fatal("the connection is still open after the listener failed")
	}
}

func TestServerStats(t *testing.T) {
	if !StatsSupported {
		t.Skip("listener statistics are Linux only")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv, err := Listen(ctx, Config{Addrs: []string{"127.0.0.1:0"}, Protocol: ProtocolAll, Logger: discard, Stats: true})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	msg := []byte("hello")
	for range 3 {
		conn, err := net.Dial("tcp", srv.TCPAddrs()[0].String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(msg); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, make([]byte, len(msg))); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}
	udp, err := net.Dial("udp4", srv.UDPAddrs()[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	for range 5 {
		if _, err := udp.Write(msg); err != nil {
			t.Fatal(err)
		}
		if _, err := udp.Read(make([]byte, len(msg))); err != nil {
			t.Fatal(err)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	stats := srv.Stats()
	if len(stats) != 2 {
		t.Fatalf("stats = %+v, want a TCP and a UDP socket", stats)
	}
	tcp, udpStats := stats[0], stats[1]
	if tcp.Protocol != ProtocolTCP || tcp.Received != 3 || tcp.Drops != 0 || tcp.QueueLen < 1 || tcp.QueueMax < 0 || tcp.QueueMax > tcp.QueueLen {
		t.Errorf("TCP stats = %+v, want 3 connections, no drop and a sampled queue", tcp)
	}
	if udpStats.Protocol != ProtocolUDP || udpStats.Received != 5 || udpStats.Drops != 0 || udpStats.QueueMax != -1 || udpStats.QueueLen != -1 {
		t.Errorf("UDP stats = %+v, want 5 datagrams and no drop", udpStats)
	}
}

func TestServerWithoutStats(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	srv, err := Listen(ctx, Config{Addrs: []string{"127.0.0.1:0"}, Protocol: ProtocolTCP, Logger: discard})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stats := srv.Stats(); stats != nil {
		t.Fatalf("stats without Config.Stats = %+v", stats)
	}
}
