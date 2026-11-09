//go:build linux

package sockopt

import (
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// echoListener echoes every TCP connection on a free loopback port until the
// test ends.
func echoListener(t *testing.T) string {
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
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestReadTCPInfo(t *testing.T) {
	conn, err := net.Dial("tcp", echoListener(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg := []byte("hello")
	for range 3 {
		if _, err := conn.Write(msg); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, msg); err != nil {
			t.Fatal(err)
		}
	}

	info, err := ReadTCPInfo(conn)
	if err != nil {
		t.Fatal(err)
	}
	// At least the SYN and the three messages; the RTT of the handshake at
	// least.
	if info.SegsOut < 4 || info.RTT <= 0 {
		t.Fatalf("TCP_INFO = %+v, want at least 4 segments sent and an RTT", info)
	}
	if info.RetransSegs > info.SegsOut {
		t.Fatalf("TCP_INFO = %+v: more retransmitted segments than sent", info)
	}
}

func TestReadTCPInfoErrors(t *testing.T) {
	udp, err := net.Dial("udp4", "127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if _, err := ReadTCPInfo(udp); err == nil {
		t.Error("ReadTCPInfo of a UDP socket returned no error")
	}

	closed, err := net.Dial("tcp", echoListener(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if _, err := ReadTCPInfo(closed); err == nil {
		t.Error("ReadTCPInfo of a closed connection returned no error")
	}
}

func TestListenControlFastOpenQueue(t *testing.T) {
	lc := net.ListenConfig{Control: ListenControl}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	raw, err := ln.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var (
		qlen   int
		optErr error
	)
	if err := raw.Control(func(fd uintptr) {
		qlen, optErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_FASTOPEN)
	}); err != nil || optErr != nil {
		t.Fatal(err, optErr)
	}
	// The queue is as long as net.core.somaxconn.
	data, err := os.ReadFile("/proc/sys/net/core/somaxconn")
	if err != nil {
		t.Fatal(err)
	}
	somaxconn, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if want := FastOpenQueue(); qlen != want || want != somaxconn {
		t.Fatalf("Fast Open queue of the listener = %d, want %d (somaxconn %d)", qlen, want, somaxconn)
	}
}
