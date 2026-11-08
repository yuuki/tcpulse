//go:build linux

package sockopt

import (
	"io"
	"net"
	"testing"
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
