package server

import (
	"context"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestListenPlainTCP(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv, err := Listen(ctx, Config{Addrs: []string{"127.0.0.1:0"}, Protocol: ProtocolTCP, Logger: discard})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.close()
	raw, err := srv.tcp[0].(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var (
		proto  int
		optErr error
	)
	if err := raw.Control(func(fd uintptr) {
		proto, optErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PROTOCOL)
	}); err != nil || optErr != nil {
		t.Fatal(err, optErr)
	}
	// Not IPPROTO_MPTCP, which Go uses by default where the kernel has it.
	if proto != unix.IPPROTO_TCP {
		t.Fatalf("protocol of the TCP listener = %d, want IPPROTO_TCP (%d)", proto, unix.IPPROTO_TCP)
	}
}
