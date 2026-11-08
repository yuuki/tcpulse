//go:build linux && (386 || amd64 || arm || arm64 || loong64 || mips64le || mipsle || ppc64le || riscv64)

package kevents

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"

	"github.com/yuuki/tcpulse/internal/stats"
)

// startTracer starts a Tracer until the test ends, or skips the test when
// this process may not load eBPF programs (tests normally run unprivileged).
func startTracer(t *testing.T) *Tracer {
	t.Helper()
	return startTracerWith(t, Options{Events: true})
}

// startTracerWith starts a Tracer of opts, as startTracer.
func startTracerWith(t *testing.T, opts Options) *Tracer {
	t.Helper()
	tr, err := Start(opts)
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

// readConn reads the events of the socket of conn.
func readConn(tr *Tracer, conn net.Conn) (stats.Events, error) {
	raw, err := conn.(syscall.Conn).SyscallConn()
	if err != nil {
		return stats.Events{}, err
	}
	var (
		ev      stats.Events
		readErr error
	)
	if err := raw.Control(func(fd uintptr) { ev, readErr = tr.ReadEvents(int(fd)) }); err != nil {
		return stats.Events{}, err
	}
	return ev, readErr
}

func readEvents(t *testing.T, tr *Tracer, conn net.Conn) stats.Events {
	t.Helper()
	ev, err := readConn(tr, conn)
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
	if ev.SYNRetrans < 1 || ev.FastOpenRetrans+ev.RTORetrans+ev.OtherRetrans+ev.ResetsReceived != 0 {
		t.Fatalf("events = %+v, want SYN retransmissions only", ev)
	}
	// Other tests (or processes) may count events too: at least ours.
	if after, _ := tr.Generation(); after-before < uint64(ev.SYNRetrans) {
		t.Fatalf("generation went from %d to %d, want at least %d more", before, after, ev.SYNRetrans)
	}
}

func TestTracerFailedDial(t *testing.T) {
	tr := startTracer(t)
	_, addr := fullListener(t)
	dial(t, addr) // fills the accept queue

	// Keep a duplicate of the socket, as the client does: the dialer closes
	// its own descriptor when the dial fails.
	dup := -1
	d := net.Dialer{
		Timeout: 1500 * time.Millisecond, // after the first SYN retransmission (1s)
		Control: func(_, _ string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				var err error
				if dup, err = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0); err != nil {
					t.Error(err)
				}
			})
		},
	}
	if conn, err := d.Dial("tcp", addr); err == nil {
		_ = conn.Close()
		t.Fatal("the dial succeeded, want a timeout")
	}
	if dup < 0 {
		t.FailNow()
	}
	defer unix.Close(dup)
	ev, err := tr.ReadEvents(dup)
	if err != nil {
		t.Fatal(err)
	}
	if ev.SYNRetrans < 1 || ev.FastOpenRetrans+ev.RTORetrans+ev.OtherRetrans != 0 {
		t.Fatalf("events of the failed dial = %+v, want SYN retransmissions only", ev)
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
	if _, err := readConn(tr, server); !errors.Is(err, ErrNotTraced) {
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
	if _, err := readConn(tr, before); !errors.Is(err, ErrNotTraced) {
		t.Errorf("ReadEvents of a connection opened before Start: %v, want ErrNotTraced", err)
	}
	udp, err := net.Dial("udp4", "127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if _, err := readConn(tr, udp); !errors.Is(err, ErrNotTraced) {
		t.Errorf("ReadEvents of a UDP socket: %v, want ErrNotTraced", err)
	}
}

// kernelSpec returns the kernel's BTF, or skips the test without it.
func kernelSpec(t *testing.T) *btf.Spec {
	t.Helper()
	kernel, err := btf.LoadKernelSpec()
	if err != nil {
		t.Skipf("no BTF: %v", err)
	}
	return kernel
}

func TestDropProgram(t *testing.T) {
	kernel := kernelSpec(t)
	params, err := tracepointParams(kernel, "kfree_skb")
	if err != nil {
		t.Fatal(err)
	}
	prog, names, err := dropProgram(kernel)
	if len(params) < 3 {
		if err == nil {
			t.Fatalf("dropProgram on a kfree_skb without reason = %q, want an error", prog)
		}
		t.Skipf("Linux 5.17 or later: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{3: keventsProgKeventsDrop, 4: keventsProgKeventsDropRx}[len(params)]
	if prog != want {
		t.Errorf("dropProgram = %q for %d parameters, want %q", prog, len(params), want)
	}
	// Linux 5.17 has these two first; there is no reason 0.
	if names[2] != "NOT_SPECIFIED" || names[3] != "NO_SOCKET" {
		t.Errorf("reasons 2 and 3 = %q, %q, want NOT_SPECIFIED, NO_SOCKET", names[2], names[3])
	}
	if name, ok := names[0]; ok {
		t.Errorf("reason 0 = %q, want none", name)
	}
	for v, name := range names {
		if name == "" || name == "MAX" || strings.HasPrefix(name, dropReasonPrefix) {
			t.Errorf("reason %d = %q", v, name)
		}
	}
}

// dropReason returns the value of the drop reason name, or skips the test
// when the kernel does not have it.
func dropReason(t *testing.T, tr *Tracer, name string) uint32 {
	t.Helper()
	for v, n := range tr.DropReasons() {
		if n == name {
			return v
		}
	}
	t.Skipf("the kernel has no drop reason %s", name)
	return 0
}

// TestTracerDrops counts a packet that the kernel drops on the receive path
// of a connection: data that arrives after it was shut down for reading,
// which TCP drops and answers with a reset.
func TestTracerDrops(t *testing.T) {
	tr := startTracer(t)
	if err := tr.DropsError(); err != nil {
		t.Skip(err)
	}
	if tr.drop != keventsProgKeventsDropRx {
		t.Skip("the receive path of TCP passes the socket to kfree_skb since Linux 6.11")
	}
	reason := dropReason(t, tr, "TCP_ABORT_ON_DATA")

	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	ln, err := lc.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn := dial(t, ln.Addr().String())
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if ev := readEvents(t, tr, conn); !ev.IsZero() {
		t.Fatalf("events of a new connection = %+v, want none", ev)
	}

	raw, err := conn.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var shutErr error
	if err := raw.Control(func(fd uintptr) { shutErr = unix.Shutdown(int(fd), unix.SHUT_RDWR) }); err != nil {
		t.Fatal(err)
	}
	if shutErr != nil {
		t.Fatal(shutErr)
	}
	// The server reads the FIN, then sends data the client no longer takes.
	if _, err := server.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("server read: %v, want EOF", err)
	}
	if _, err := server.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		ev := readEvents(t, tr, conn)
		if ev.DroppedPackets() > 0 {
			if ev.Drops[0] != (stats.DropCount{Reason: reason, Count: 1}) || ev.DroppedPackets() != 1 {
				t.Fatalf("drops = %+v, other %d, want one of reason TCP_ABORT_ON_DATA (%d)", ev.Drops, ev.OtherDrops, reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %+v, want a drop", ev)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runqBucket mirrors runq_bucket of kevents.bpf.c.
func runqBucket(ns uint64) int {
	if ns < runqSub {
		return int(ns)
	}
	e := bits.Len64(ns) - 1
	return (e-runqSubBits+1)<<runqSubBits | int(ns>>(e-runqSubBits))&(runqSub-1)
}

func TestRunqBuckets(t *testing.T) {
	buckets := len(keventsKeventsRunq{}.Buckets)
	if b := runqBucket(math.MaxUint64); b != buckets-1 || runqBucketMax(b) != math.MaxUint64 {
		t.Fatalf("the largest wait is in bucket %d up to %d, want the last one, %d", b, runqBucketMax(b), buckets-1)
	}
	// Every bucket starts right after the previous one, and is at most 1/8
	// of its values wide.
	for b := 1; b < buckets; b++ {
		lo, hi := runqBucketMax(b-1)+1, runqBucketMax(b)
		if runqBucket(lo) != b || runqBucket(hi) != b {
			t.Fatalf("bucket %d is [%d, %d], but they fall in %d and %d", b, lo, hi, runqBucket(lo), runqBucket(hi))
		}
		if hi-lo > lo/8 {
			t.Fatalf("bucket %d is [%d, %d]: wider than 1/8", b, lo, hi)
		}
	}
}

func TestSummarizeRunQueue(t *testing.T) {
	var r keventsKeventsRunq
	// 98 waits of 1µs, one of 2ms and one of 50ms.
	r.Buckets[runqBucket(1000)] = 98
	r.Buckets[runqBucket(2_000_000)] = 1
	r.Buckets[runqBucket(50_000_000)] = 1
	r.Waits, r.TotalNs, r.MaxNs, r.Preempted = 100, 98_000+52_000_000, 50_000_000, 7
	s := summarizeRunQueue(&r)
	if s.Waits != 100 || s.Preempted != 7 || s.Total != 52098*time.Microsecond || s.Max != 50*time.Millisecond {
		t.Fatalf("summary = %+v", s)
	}
	// Rounded up, within 1/8; the 99th is the 99th of 100 waits.
	if s.P50 < time.Microsecond || s.P50 > 1125*time.Nanosecond {
		t.Errorf("P50 = %v, want 1µs within 1/8", s.P50)
	}
	if s.P99 < 2*time.Millisecond || s.P99 > 2250*time.Microsecond {
		t.Errorf("P99 = %v, want 2ms within 1/8", s.P99)
	}
	if s := summarizeRunQueue(&keventsKeventsRunq{}); s != (stats.RunQueueSummary{}) {
		t.Errorf("summary of nothing = %+v, want zero", s)
	}
}

// TestTracerRunQueue makes more threads of the process runnable than there
// are CPUs, so that they wait for one.
func TestTracerRunQueue(t *testing.T) {
	tr := startTracerWith(t, Options{RunQueue: true})
	if _, err := tr.ReadEvents(0); err == nil {
		t.Error("ReadEvents without Events succeeded")
	}
	threads := 4 * runtime.NumCPU()
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(threads))
	// The threads spin: they never block, so they wait only for a CPU.
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range threads {
		wg.Go(func() {
			runtime.LockOSThread()
			for !stop.Load() {
			}
		})
	}
	time.Sleep(300 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	s, err := tr.RunQueue()
	if err != nil {
		t.Fatal(err)
	}
	if s.Waits == 0 || s.Preempted == 0 || s.Max == 0 {
		t.Fatalf("run queue = %+v, want waits and preemptions", s)
	}
	if s.P50 > s.P99 || s.P99 > s.Max || s.Max > s.Total {
		t.Fatalf("run queue = %+v, want P50 <= P99 <= Max <= Total", s)
	}
	// The programs and the decoding agree on the buckets: the longest wait
	// is in the last bucket that has waits.
	var perCPU []keventsKeventsRunq
	if err := tr.runq.Lookup(uint32(0), &perCPU); err != nil {
		t.Fatal(err)
	}
	last := -1
	for _, c := range perCPU {
		for b, n := range c.Buckets {
			if n > 0 {
				last = max(last, b)
			}
		}
	}
	maxNs := uint64(s.Max.Nanoseconds())
	if last < 1 || maxNs <= runqBucketMax(last-1) || maxNs > runqBucketMax(last) {
		t.Fatalf("longest wait %dns, last bucket with waits %d", maxNs, last)
	}
}
