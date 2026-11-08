//go:build linux

package sockopt

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// option is a socket option whose failure is logged once per process: the
// options are optimizations, and a kernel that lacks one would otherwise log
// an error for every connection.
type option struct {
	name    string
	level   int
	opt     int
	value   int
	warning sync.Once
}

func (o *option) set(fd uintptr) {
	if err := unix.SetsockoptInt(int(fd), o.level, o.opt, o.value); err != nil {
		o.warning.Do(func() {
			slog.Warn("could not set socket option", "option", o.name, "error", err)
		})
	}
}

var (
	reusePort = &option{name: "SO_REUSEPORT", level: unix.SOL_SOCKET, opt: unix.SO_REUSEPORT, value: 1}
	// Client side: send the first message in the SYN.
	fastOpenConnect = &option{name: "TCP_FASTOPEN_CONNECT", level: unix.IPPROTO_TCP, opt: unix.TCP_FASTOPEN_CONNECT, value: 1}
	// Server side: accept data in the SYN; the value is the length of the
	// queue of pending Fast Open requests.
	fastOpen = &option{name: "TCP_FASTOPEN", level: unix.IPPROTO_TCP, opt: unix.TCP_FASTOPEN, value: 1}
)

// DialControl is a net.Dialer Control function that enables SO_REUSEPORT and
// TCP_FASTOPEN_CONNECT.
func DialControl(_, _ string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		reusePort.set(fd)
		fastOpenConnect.set(fd)
	})
}

// ListenControl is a net.ListenConfig Control function that enables
// SO_REUSEPORT and TCP_FASTOPEN.
func ListenControl(_, _ string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		reusePort.set(fd)
		fastOpen.set(fd)
	})
}

var quickAck = &option{name: "TCP_QUICKACK", level: unix.IPPROTO_TCP, opt: unix.TCP_QUICKACK, value: 1}

// SetQuickAck enables TCP_QUICKACK on a TCP connection.
func SetQuickAck(conn net.Conn) error {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("not a TCP connection: %T", conn)
	}
	raw, err := tcpConn.SyscallConn()
	if err != nil {
		return fmt.Errorf("getting the socket of %s: %w", conn.RemoteAddr(), err)
	}
	if err := raw.Control(quickAck.set); err != nil {
		return fmt.Errorf("setting TCP_QUICKACK on %s: %w", conn.RemoteAddr(), err)
	}
	return nil
}
