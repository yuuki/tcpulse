//go:build linux

package tstamp

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// prepare turns on the receive timestamps until the test ends.
func prepare(t *testing.T) {
	t.Helper()
	release, err := Prepare(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}

// echoPair returns a connected TCP client and the server side of it, with
// the receive timestamps on.
func echoPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	prepare(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err = net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err = ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

func TestTimestamps(t *testing.T) {
	client, server := echoPair(t)
	ts, err := Enable(client.(*net.TCPConn))
	if err != nil {
		t.Fatal(err)
	}
	for i, msg := range []string{"abc", "defgh", "ij"} {
		start := time.Now()
		// Two send requests for the second message: only the last byte of
		// the second is the message's.
		writes := []string{msg}
		if i == 1 {
			writes = []string{msg[:2], msg[2:]}
		}
		for _, w := range writes {
			if _, err := client.Write([]byte(w)); err != nil {
				t.Fatal(err)
			}
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(server, buf); err != nil {
			t.Fatal(err)
		}
		if _, err := server.Write(buf); err != nil {
			t.Fatal(err)
		}
		echo := make([]byte, len(msg))
		rx, err := ts.ReadFull(t.Context(), echo)
		if err != nil {
			t.Fatal(err)
		}
		end := time.Now()
		if string(echo) != msg || rx.Before(start) || rx.After(end) {
			t.Fatalf("message %d: echo %q received at %v, want %q between %v and %v", i, echo, rx, msg, start, end)
		}
	}
	if err := ts.Drain(); err != nil {
		t.Fatal(err)
	}
	// The last bytes of the four send requests: 2, 4 (the partial write), 7
	// and 9.
	var prev time.Time
	for _, key := range []uint32{2, 4, 7, 9} {
		tx, ok := ts.Transmitted(key)
		if !ok || tx.Before(prev) {
			t.Fatalf("transmit timestamp of byte %d: %v, %v (previous %v)", key, tx, ok, prev)
		}
		prev = tx
	}
	for _, key := range []uint32{0, 1, 3, 5, 6, 8} {
		if tx, ok := ts.Transmitted(key); ok {
			t.Errorf("byte %d, not the last of a send request, has a timestamp %v", key, tx)
		}
	}
	ts.Forget(7)
	if _, ok := ts.Transmitted(4); ok {
		t.Error("Forget(7) kept byte 4")
	}
	if _, ok := ts.Transmitted(9); !ok {
		t.Error("Forget(7) dropped byte 9")
	}
	// Nothing more is queued: Drain returns at once.
	if err := ts.Drain(); err != nil {
		t.Fatal(err)
	}
}

func TestReadFullEOF(t *testing.T) {
	client, server := echoPair(t)
	ts, err := Enable(client.(*net.TCPConn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	_ = server.Close()
	if _, err := ts.ReadFull(t.Context(), make([]byte, 3)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadFull of 3 bytes when 2 come: %v, want io.ErrUnexpectedEOF", err)
	}
	if _, err := ts.ReadFull(t.Context(), make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFull at the end: %v, want io.EOF", err)
	}
}

func TestReadFullDeadline(t *testing.T) {
	client, _ := echoPair(t)
	ts, err := Enable(client.(*net.TCPConn))
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := ts.ReadFull(t.Context(), make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("ReadFull past the deadline: %v, want os.ErrDeadlineExceeded", err)
	}
}

// TestEnableNotReady enables the timestamps of a connection still in its
// handshake: a SYN to a listener whose accept queue is full.
func TestEnableNotReady(t *testing.T) {
	lfd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(lfd)
	addr := &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}
	if err := unix.Bind(lfd, addr); err != nil {
		t.Fatal(err)
	}
	if err := unix.Listen(lfd, 0); err != nil {
		t.Fatal(err)
	}
	sa, err := unix.Getsockname(lfd)
	if err != nil {
		t.Fatal(err)
	}
	addr.Port = sa.(*unix.SockaddrInet4).Port
	// The first connection fills the accept queue; the second waits.
	for i := range 2 {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		f := os.NewFile(uintptr(fd), "client")
		defer f.Close()
		if err := unix.Connect(fd, addr); err != nil && !errors.Is(err, unix.EINPROGRESS) {
			t.Fatal(err)
		}
		if i == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if _, err := Enable(f); !errors.Is(err, ErrNotReady) {
			t.Fatalf("Enable during the handshake: %v, want ErrNotReady", err)
		}
	}
}

// FuzzEachMessage checks that control messages are decoded within their
// buffer, whatever the kernel's lengths.
func FuzzEachMessage(f *testing.F) {
	// A timestamp, then an extended error.
	oob := make([]byte, unix.CmsgSpace(48)+unix.CmsgSpace(sizeofSockExtendedErr))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
	h.Level, h.Type = unix.SOL_SOCKET, unix.SO_TIMESTAMPING_NEW
	h.SetLen(unix.CmsgLen(48))
	oob[unix.CmsgLen(0)] = 7
	h = (*unix.Cmsghdr)(unsafe.Pointer(&oob[unix.CmsgSpace(48)]))
	h.Level, h.Type = unix.SOL_IP, unix.IP_RECVERR
	h.SetLen(unix.CmsgLen(sizeofSockExtendedErr))
	f.Add(oob)
	f.Add([]byte{})
	f.Add(make([]byte, unix.SizeofCmsghdr))
	f.Fuzz(func(t *testing.T, oob []byte) {
		// Aligned, as the buffers of recvmsg are.
		buf := make([]uint64, (len(oob)+7)/8)
		aligned := unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(buf))), len(buf)*8)[:len(oob)]
		copy(aligned, oob)
		total := 0
		eachMessage(aligned, func(_, _ int32, data []byte) {
			total += len(data)
			if len(data) > len(aligned) {
				t.Fatalf("message of %d bytes in %d", len(data), len(aligned))
			}
			_ = software(data)
		})
		if total > len(aligned) {
			t.Fatalf("messages of %d bytes in %d", total, len(aligned))
		}
	})
}

// TestReadFullErrorEvent reads echoes while transmit timestamps are queued
// on a socket that is neither readable nor writable: a message larger than
// its send buffer. The poller then sees an error event alone (EPOLLERR) and
// would refuse to wait for the socket ("not pollable").
func TestReadFullErrorEvent(t *testing.T) {
	client, server := echoPair(t)
	if err := client.(*net.TCPConn).SetWriteBuffer(8192); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(server, server) }()
	ts, err := Enable(client.(*net.TCPConn))
	if err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, 32*1024)
	echo := make([]byte, len(msg))
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	for i := range 20 {
		written := make(chan error, 1)
		go func() {
			_, err := client.Write(msg)
			written <- err
		}()
		if _, err := ts.ReadFull(t.Context(), echo); err != nil {
			t.Fatalf("round trip %d: %v", i, err)
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
		if err := ts.Drain(); err != nil {
			t.Fatal(err)
		}
		if _, ok := ts.Transmitted(uint32((i+1)*len(msg) - 1)); !ok {
			t.Logf("round trip %d: no transmit timestamp of its last byte", i)
		}
	}
	if ts.fallbacks == 0 {
		t.Skip("the poller never refused to wait: the fallback did not run")
	}
}

// slowEchoes accepts connections on a loopback listener and, for each,
// reads a message of size bytes slowly through a small receive buffer, waits
// for wait, and echoes it: the client's reader waits while its send buffer
// is full, which makes the poller refuse to wait.
func slowEchoes(t *testing.T, size int, wait time.Duration) string {
	t.Helper()
	prepare(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			server, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer server.Close()
				_ = server.(*net.TCPConn).SetReadBuffer(4096)
				buf := make([]byte, size)
				for got := 0; got < len(buf); {
					n, err := server.Read(buf[got:min(got+2048, len(buf))])
					if err != nil {
						return
					}
					got += n
					time.Sleep(2 * time.Millisecond)
				}
				time.Sleep(wait)
				_, _ = server.Write(buf)
				_, _ = io.Copy(io.Discard, server)
			}()
		}
	}()
	return ln.Addr().String()
}

// slowClient dials addr with a small send buffer and turns its timestamps on.
func slowClient(t *testing.T, addr string) (net.Conn, *Conn, error) {
	conn, err := net.Dial("tcp4", addr)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.(*net.TCPConn).SetWriteBuffer(4096)
	ts, err := Enable(conn.(*net.TCPConn))
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, ts, nil
}

// TestReadFullFallbackDeadline checks that the deadline of the socket ends
// a read that waits without the poller.
func TestReadFullFallbackDeadline(t *testing.T) {
	addr := slowEchoes(t, 256*1024, 10*time.Second)
	conn, ts, err := slowClient(t, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	_ = conn.SetDeadline(start.Add(time.Second))
	go func() { _, _ = conn.Write(make([]byte, 256*1024)) }()
	_, err = ts.ReadFull(t.Context(), make([]byte, 256*1024))
	took := time.Since(start)
	if !errors.Is(err, os.ErrDeadlineExceeded) || took < time.Second || took > 2*time.Second {
		t.Fatalf("ReadFull: %v after %v, want the deadline's error after 1s", err, took)
	}
	if ts.fallbacks == 0 {
		t.Skip("the poller never refused to wait: the fallback did not run")
	}
}

// TestReadFullFallbackContext checks that the end of ctx ends a read at
// once, wherever it waits. As the client does, the end of ctx also sets the
// deadline of the socket, which interrupts a wait in the poller; ctx itself
// interrupts a wait without it.
func TestReadFullFallbackContext(t *testing.T) {
	addr := slowEchoes(t, 256*1024, 10*time.Second)
	conn, ts, err := slowClient(t, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() { _, _ = conn.Write(make([]byte, 256*1024)) }()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	defer context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })()
	start := time.Now()
	_, err = ts.ReadFull(ctx, make([]byte, 256*1024))
	took := time.Since(start)
	if ts.fallbacks == 0 {
		t.Skip("the poller never refused to wait: the fallback did not run")
	}
	// Within the time of a turn, far less than pollWait.
	ended := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded)
	if !ended || took < time.Second || took > time.Second+pollWait/2 {
		t.Fatalf("ReadFull: %v after %v, want the end of the context after 1s", err, took)
	}
}

// threads returns the number of threads of the process.
func threads(t *testing.T) int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "Threads:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("no thread count")
	return 0
}

// TestFallbackThreads checks that many readers waiting without the poller
// do not block a thread each.
func TestFallbackThreads(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for 200 slow echoes")
	}
	const readers = 200
	addr := slowEchoes(t, 256*1024, time.Second)
	var (
		wg        sync.WaitGroup
		fallbacks atomic.Int64
	)
	for range readers {
		wg.Go(func() {
			conn, ts, err := slowClient(t, addr)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
			go func() { _, _ = conn.Write(make([]byte, 256*1024)) }()
			if _, err := ts.ReadFull(t.Context(), make([]byte, 256*1024)); err != nil {
				t.Error(err)
			}
			fallbacks.Add(int64(ts.fallbacks))
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	peak := 0
	for tick := time.NewTicker(50 * time.Millisecond); ; {
		select {
		case <-tick.C:
			peak = max(peak, threads(t))
			continue
		case <-done:
			tick.Stop()
		}
		break
	}
	if fallbacks.Load() == 0 {
		t.Skip("the poller never refused to wait: the fallback did not run")
	}
	if peak > readers/4 {
		t.Fatalf("%d threads at most for %d readers waiting without the poller (%d turns)", peak, readers, fallbacks.Load())
	}
	t.Logf("%d threads at most, %d turns without the poller", peak, fallbacks.Load())
}

func TestReadFullAllocations(t *testing.T) {
	client, server := echoPair(t)
	ts, err := Enable(client.(*net.TCPConn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write(make([]byte, 1000)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := ts.ReadFull(t.Context(), buf); err != nil {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(100, func() {
		if _, err := ts.ReadFull(t.Context(), buf); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("ReadFull allocates %v times per call", n)
	}
}

func TestOutOfOrder(t *testing.T) {
	client, server := echoPair(t)
	ts, err := Enable(client.(*net.TCPConn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write([]byte("in order")); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.ReadFull(t.Context(), make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	n, ok, err := ts.OutOfOrder()
	if err != nil {
		t.Fatal(err)
	}
	if ok && n != 0 {
		t.Fatalf("%d packets out of order over loopback, want 0", n)
	}
}
