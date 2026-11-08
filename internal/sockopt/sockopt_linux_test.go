//go:build linux

package sockopt

import (
	"io"
	"net"
	"os"
	"testing"
	"time"

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

// rawListener listens on a free loopback port with backlog and returns a
// net.Listener of it, which accepts nothing until asked.
func rawListener(t *testing.T, backlog int) net.Listener {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := unix.Listen(fd, backlog); err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), "listener")
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestAcceptQueue(t *testing.T) {
	ln := rawListener(t, 5)
	for range 3 {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	waiting, length, err := AcceptQueue(ln.(*net.TCPListener))
	if err != nil {
		t.Fatal(err)
	}
	if waiting != 3 || length != 5 {
		t.Fatalf("accept queue = %d of %d, want 3 of 5", waiting, length)
	}
	if drops, err := SocketDrops(ln.(*net.TCPListener)); err != nil || drops != 0 {
		t.Fatalf("drops = %d, %v; want none", drops, err)
	}

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := AcceptQueue(conn.(*net.TCPConn)); err == nil {
		t.Error("AcceptQueue of a connected socket returned no error")
	}
}

func TestSocketDropsListener(t *testing.T) {
	// A backlog of 0 holds one connection: the SYNs of the next ones are
	// dropped while it waits.
	ln := rawListener(t, 0)
	first, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	if conn, err := d.Dial("tcp", ln.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("the dial succeeded, want its SYN dropped")
	}
	drops, err := SocketDrops(ln.(*net.TCPListener))
	if err != nil {
		t.Fatal(err)
	}
	if drops < 1 {
		t.Fatalf("drops of the full listener = %d, want at least 1", drops)
	}
}

func TestSocketDropsUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	// The smallest receive buffer the kernel allows, flooded and never read.
	if err := pc.(*net.UDPConn).SetReadBuffer(1); err != nil {
		t.Fatal(err)
	}
	sender, err := net.Dial("udp4", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	msg := make([]byte, 1024)
	for range 200 {
		if _, err := sender.Write(msg); err != nil {
			t.Fatal(err)
		}
	}
	drops, err := SocketDrops(pc.(*net.UDPConn))
	if err != nil {
		t.Fatal(err)
	}
	if drops < 1 {
		t.Fatalf("drops of the flooded UDP socket = %d, want some", drops)
	}
}
