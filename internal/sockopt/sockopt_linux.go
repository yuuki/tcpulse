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
	"unsafe"

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

// SocketStatsSupported reports whether SocketDrops and AcceptQueue work on
// this platform.
const SocketStatsSupported = true

// skMemInfoDrops and skMemInfoVars are SK_MEMINFO_DROPS and SK_MEMINFO_VARS
// (linux/sock_diag.h): the index of the drop count in SO_MEMINFO, and the
// number of its values.
const (
	skMemInfoDrops = 8
	skMemInfoVars  = 9
)

// tcpListen is TCP_LISTEN (include/net/tcp_states.h).
const tcpListen = 10

// control runs f with the descriptor of c.
func control(c syscall.Conn, f func(fd int) error) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := raw.Control(func(fd uintptr) { ferr = f(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

// SocketDrops returns the packets the kernel discarded at a socket since it
// was created (SO_MEMINFO, Linux 4.12). For a TCP listener: the SYNs it
// refused and their retransmissions, the ACKs a full accept queue refused
// (twice each), the SYNs sent again for handshakes in progress (a lost
// SYN-ACK), resets of those handshakes, and packets a filter dropped. For a
// UDP socket: mostly the datagrams that did not fit in its receive buffer,
// and those dropped for memory pressure, a bad checksum, or by a filter.
func SocketDrops(c syscall.Conn) (uint32, error) {
	var mem [skMemInfoVars]uint32
	err := control(c, func(fd int) error {
		size := uint32(unsafe.Sizeof(mem))
		_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, uintptr(fd), unix.SOL_SOCKET, unix.SO_MEMINFO,
			uintptr(unsafe.Pointer(&mem)), uintptr(unsafe.Pointer(&size)), 0)
		if errno != 0 {
			return fmt.Errorf("getsockopt SO_MEMINFO: %w", errno)
		}
		if size < (skMemInfoDrops+1)*4 {
			return fmt.Errorf("SO_MEMINFO of %d bytes lacks the drops", size)
		}
		return nil
	})
	return mem[skMemInfoDrops], err
}

// AcceptQueue returns the connections waiting in the accept queue of a TCP
// listener, and the length of the queue (TCP_INFO of a listening socket:
// tcpi_unacked and tcpi_sacked).
func AcceptQueue(c syscall.Conn) (waiting, length uint32, err error) {
	err = control(c, func(fd int) error {
		info, err := unix.GetsockoptTCPInfo(fd, unix.IPPROTO_TCP, unix.TCP_INFO)
		if err != nil {
			return fmt.Errorf("getsockopt TCP_INFO: %w", err)
		}
		if info.State != tcpListen {
			return fmt.Errorf("not a listening socket (TCP state %d)", info.State)
		}
		waiting, length = info.Unacked, info.Sacked
		return nil
	})
	return waiting, length, err
}
