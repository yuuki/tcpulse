//go:build linux

package tstamp

import (
	"errors"
	"io"
	"net"
	"os"
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
		rx, err := ts.ReadFull(echo)
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
	if _, err := ts.ReadFull(make([]byte, 3)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadFull of 3 bytes when 2 come: %v, want io.ErrUnexpectedEOF", err)
	}
	if _, err := ts.ReadFull(make([]byte, 1)); !errors.Is(err, io.EOF) {
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
	if _, err := ts.ReadFull(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
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
		if _, err := ts.ReadFull(echo); err != nil {
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
	if _, err := ts.ReadFull(make([]byte, 8)); err != nil {
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
