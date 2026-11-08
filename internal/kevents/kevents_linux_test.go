//go:build linux && (386 || amd64 || arm || arm64 || loong64 || mips64le || mipsle || ppc64le || riscv64)

package kevents

import (
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yuuki/tcpulse/internal/stats"
)

// startTracer starts a Tracer until the test ends, or skips the test when
// this process may not load eBPF programs (tests normally run unprivileged).
func startTracer(t *testing.T) *Tracer {
	t.Helper()
	tr, err := Start()
	if errors.Is(err, os.ErrPermission) {
		t.Skipf("loading eBPF programs needs root, or CAP_BPF and CAP_PERFMON: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return tr
}

// fullListener listens on a free loopback port with an accept queue of one
// connection that it does not accept: the kernel drops the SYNs of further
// connections until accept is called, so they retransmit them.
func fullListener(t *testing.T) (fd int, addr string) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := unix.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	sa, err := unix.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return fd, fmt.Sprintf("127.0.0.1:%d", sa.(*unix.SockaddrInet4).Port)
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readEvents(t *testing.T, tr *Tracer, conn net.Conn) stats.Events {
	t.Helper()
	ev, err := tr.ReadEvents(conn)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestTracerSYNRetransmissions(t *testing.T) {
	tr := startTracer(t)
	lfd, addr := fullListener(t)
	first := dial(t, addr) // fills the accept queue
	if ev := readEvents(t, tr, first); !ev.IsZero() {
		t.Fatalf("events of an accepted connection = %+v, want none", ev)
	}
	before, ok := tr.Generation()
	if !ok {
		t.Fatal("no generation: the programs' memory could not be mapped")
	}

	second := make(chan net.Conn, 1)
	go func() {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			t.Error(err)
		}
		second <- conn
	}()
	// The first SYN retransmission comes 1s after the SYN: free the queue
	// after it.
	time.Sleep(1300 * time.Millisecond)
	nfd, _, err := unix.Accept(lfd)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(nfd)
	conn := <-second
	if conn == nil {
		t.FailNow()
	}
	defer conn.Close()
	ev := readEvents(t, tr, conn)
	if ev.SYNRetrans < 1 || ev.FastOpenRetrans+ev.RTORetrans+ev.FastRetrans+ev.ResetsReceived != 0 {
		t.Fatalf("events = %+v, want SYN retransmissions only", ev)
	}
	// Other tests (or processes) may count events too: at least ours.
	if after, _ := tr.Generation(); after-before < uint64(ev.SYNRetrans) {
		t.Fatalf("generation went from %d to %d, want at least %d more", before, after, ev.SYNRetrans)
	}
}

func TestTracerResets(t *testing.T) {
	tr := startTracer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Error(err)
		}
		accepted <- conn
	}()
	conn := dial(t, ln.Addr().String())
	server := <-accepted
	if server == nil {
		t.FailNow()
	}
	// A server-side socket was not opened with connect(2).
	if _, err := tr.ReadEvents(server); !errors.Is(err, ErrNotTraced) {
		t.Errorf("ReadEvents of an accepted socket: %v, want ErrNotTraced", err)
	}
	// Closing with a zero linger time sends a reset.
	if err := server.(*net.TCPConn).SetLinger(0); err != nil {
		t.Fatal(err)
	}
	_ = server.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		ev := readEvents(t, tr, conn)
		if ev.ResetsReceived == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %+v, want one reset received", ev)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTracerNotTraced(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// Opened before the programs were attached.
	before := dial(t, ln.Addr().String())
	tr := startTracer(t)
	if _, err := tr.ReadEvents(before); !errors.Is(err, ErrNotTraced) {
		t.Errorf("ReadEvents of a connection opened before Start: %v, want ErrNotTraced", err)
	}
	udp, err := net.Dial("udp4", "127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if _, err := tr.ReadEvents(udp); !errors.Is(err, ErrNotTraced) {
		t.Errorf("ReadEvents of a UDP socket: %v, want ErrNotTraced", err)
	}
}
