//go:build linux

package sockopt

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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

func (o *option) set(fd uintptr) { o.setValue(fd, o.value) }

// setValue sets the option to value rather than o.value.
func (o *option) setValue(fd uintptr, value int) {
	if err := unix.SetsockoptInt(int(fd), o.level, o.opt, value); err != nil {
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
	// queue of pending Fast Open requests (FastOpenQueue).
	fastOpen = &option{name: "TCP_FASTOPEN", level: unix.IPPROTO_TCP, opt: unix.TCP_FASTOPEN}
)

// defaultSomaxconn is net.core.somaxconn's default since Linux 5.4, for when
// the sysctl cannot be read.
const defaultSomaxconn = 4096

// FastOpenQueue returns the length of the queue of pending Fast Open
// requests of a listening socket: the connections that a SYN with data
// created and whose handshake is not over (they are in SYN_RECV until the
// client's last ACK, and one reset keeps its place for 60s). A request that
// does not fit falls back to a plain handshake, and the client sends its
// data again. The queue needs about the rate of new connections times the
// RTT of a handshake, which tcpulse cannot know: it is as long as the accept
// queue, net.core.somaxconn, as the kernel makes it itself with the 0x400
// bit of net.ipv4.tcp_fastopen. The sysctl is read rather than left to the
// kernel's cap (fastopen_queue_tune), which only Linux 4.4 and later apply.
//
// Without cookies (bit 0x200 of net.ipv4.tcp_fastopen, or a route's
// fastopen_no_cookie), spoofed SYNs with data fill this queue with
// connections that the server accepts and holds until their handshake
// times out, about 2 minutes: up to this many per listener.
var FastOpenQueue = sync.OnceValue(func() int {
	data, err := os.ReadFile("/proc/sys/net/core/somaxconn")
	if err != nil {
		return defaultSomaxconn
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 1 {
		return defaultSomaxconn
	}
	return min(n, math.MaxInt32)
})

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
		fastOpen.setValue(fd, FastOpenQueue())
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

// TCPInfoSupported reports whether ReadTCPInfo works on this platform.
const TCPInfoSupported = true

// tcpiOptSYNData is TCPI_OPT_SYN_DATA of tcpi_options (linux/tcp.h): the
// SYN-ACK acknowledged the data sent in the SYN.
const tcpiOptSYNData = 32

// ReadTCPInfo returns what the kernel reports of a TCP connection
// (getsockopt TCP_INFO).
func ReadTCPInfo(conn net.Conn) (TCPInfo, error) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return TCPInfo{}, fmt.Errorf("not a TCP connection: %T", conn)
	}
	raw, err := tcpConn.SyscallConn()
	if err != nil {
		return TCPInfo{}, fmt.Errorf("getting the socket of %s: %w", conn.RemoteAddr(), err)
	}
	var (
		info    *unix.TCPInfo
		infoErr error
	)
	if err := raw.Control(func(fd uintptr) {
		info, infoErr = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	}); err != nil {
		return TCPInfo{}, fmt.Errorf("reading TCP_INFO of %s: %w", conn.RemoteAddr(), err)
	}
	if infoErr != nil {
		return TCPInfo{}, fmt.Errorf("reading TCP_INFO of %s: %w", conn.RemoteAddr(), infoErr)
	}
	return TCPInfo{
		RetransSegs: info.Total_retrans,
		SegsOut:     info.Segs_out,
		RTT:         time.Duration(info.Rtt) * time.Microsecond,
		FastOpen:    info.Options&tcpiOptSYNData != 0,
	}, nil
}
