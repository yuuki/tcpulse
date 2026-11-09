//go:build linux

package tstamp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Supported reports whether Enable can work on this platform.
const Supported = true

// flags asks for the software timestamps of the transmission of the last
// byte of every send request (taken by the driver, or the loopback device),
// keyed by the byte's offset (OPT_ID) and without the payload (OPT_TSONLY),
// and of reception. Enable adds OPT_ID_TCP where the kernel has it.
const flags = unix.SOF_TIMESTAMPING_TX_SOFTWARE | unix.SOF_TIMESTAMPING_RX_SOFTWARE |
	unix.SOF_TIMESTAMPING_SOFTWARE | unix.SOF_TIMESTAMPING_OPT_ID | unix.SOF_TIMESTAMPING_OPT_TSONLY

// tcpEstablished is TCP_ESTABLISHED (include/net/tcp_states.h).
const tcpEstablished = 1

// sizeofSockExtendedErr is the size of struct sock_extended_err.
const sizeofSockExtendedErr = int(unsafe.Sizeof(unix.SockExtendedErr{}))

// option returns the socket option that turns the timestamps on, which is
// also the type of their control messages: SO_TIMESTAMPING_NEW (Linux 5.1),
// whose timestamps have 64-bit fields on every architecture, or else, on
// 64-bit architectures, SO_TIMESTAMPING, whose layout is then the same.
func option(fd int, flags int) (int, error) {
	err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPING_NEW, flags)
	if !errors.Is(err, unix.ENOPROTOOPT) {
		return unix.SO_TIMESTAMPING_NEW, err
	}
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return 0, fmt.Errorf("%w: SO_TIMESTAMPING_NEW needs Linux 5.1 or later on 32-bit architectures", ErrNoTimestamping)
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, flags); err != nil {
		if errors.Is(err, unix.ENOPROTOOPT) {
			return 0, fmt.Errorf("%w: %w", ErrNoTimestamping, err)
		}
		return 0, err
	}
	return unix.SO_TIMESTAMPING, nil
}

// Conn reads the timestamps of a TCP connection. Its methods must not be
// called concurrently.
type Conn struct {
	raw syscall.RawConn
	// typ is the type of the control messages of the timestamps (option).
	typ int32
	// oob receives control messages; payload the (empty) payload of the
	// messages of the error queue.
	oob     []byte
	payload [1]byte
	// tx holds the first transmit timestamp of each key not yet forgotten.
	tx map[uint32]time.Time

	// The arguments and results of the recvmsg(2) calls, and the functions
	// that make them, built once: a read allocates nothing (unix.Recvmsg
	// would allocate the address of the peer).
	hdr        unix.Msghdr
	iov        unix.Iovec
	buf        []byte
	n, oobn    int
	err        error
	read       func(fd uintptr) bool // raw.Read: recvmsg of buf
	recv       func(fd uintptr)      // raw.Control: recvmsg of buf, without waiting
	errOOBN    int
	errFlags   int
	errErr     error
	recvErrors func(fd uintptr) // raw.Control: recvmsg of the error queue

	// fallbacks counts the reads that waited without Go's poller.
	fallbacks int
}

// Enable turns on the timestamps of the TCP connection c. The key of a
// transmit timestamp is the offset of its byte among those written after
// Enable: the first byte written next is 0. It returns ErrNotReady when the
// offsets are not known yet: Enable can be tried again later.
func Enable(c syscall.Conn) (*Conn, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var (
		typ  int
		serr error
	)
	err = raw.Control(func(fd uintptr) { typ, serr = enable(int(fd)) })
	if err == nil {
		err = serr
	}
	if err != nil {
		return nil, err
	}
	// Room for a timestamp, and the error with its offender address.
	oob := make([]byte, unix.CmsgSpace(int(unsafe.Sizeof(timestamps{})))+
		unix.CmsgSpace(sizeofSockExtendedErr+unix.SizeofSockaddrInet6))
	tc := &Conn{raw: raw, typ: int32(typ), oob: oob, tx: map[uint32]time.Time{}} //nolint:gosec // a socket option
	tc.read = func(fd uintptr) bool {
		tc.n, tc.oobn, _, tc.err = tc.recvmsg(fd, tc.buf, 0)
		return !errors.Is(tc.err, unix.EAGAIN)
	}
	tc.recv = func(fd uintptr) {
		tc.n, tc.oobn, _, tc.err = tc.recvmsg(fd, tc.buf, unix.MSG_DONTWAIT)
	}
	tc.recvErrors = func(fd uintptr) {
		_, tc.errOOBN, tc.errFlags, tc.errErr = tc.recvmsg(fd, tc.payload[:], unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
	}
	return tc, nil
}

// recvmsg calls recvmsg(2) on fd, into p and c.oob, without the address of
// the peer, with the header kept in c.
func (c *Conn) recvmsg(fd uintptr, p []byte, flags int) (n, oobn, rflags int, err error) {
	c.iov.Base = unsafe.SliceData(p)
	c.iov.SetLen(len(p))
	c.hdr.Iov = &c.iov
	c.hdr.SetIovlen(1)
	c.hdr.Control = unsafe.SliceData(c.oob)
	c.hdr.SetControllen(len(c.oob))
	c.hdr.Flags = 0
	r, _, errno := unix.Syscall(unix.SYS_RECVMSG, fd, uintptr(unsafe.Pointer(&c.hdr)), uintptr(flags))
	if errno != 0 {
		return 0, 0, 0, errno
	}
	// The kernel's lengths are at most those of the buffers.
	return int(r), int(c.hdr.Controllen), int(c.hdr.Flags), nil //nolint:gosec // see above
}

func enable(fd int) (int, error) {
	info, err := unix.GetsockoptTCPInfo(fd, unix.IPPROTO_TCP, unix.TCP_INFO)
	if err != nil {
		return 0, fmt.Errorf("reading TCP_INFO: %w", err)
	}
	if info.State != tcpEstablished {
		return 0, ErrNotReady
	}
	// With OPT_ID_TCP (Linux 6.2), the keys count from the next byte
	// written; without, from the first byte not acknowledged.
	typ, err := option(fd, flags|unix.SOF_TIMESTAMPING_OPT_ID_TCP)
	if errors.Is(err, unix.EINVAL) {
		if info.Unacked != 0 || info.Notsent_bytes != 0 {
			return 0, ErrNotReady
		}
		typ, err = option(fd, flags)
	}
	if err != nil {
		return 0, fmt.Errorf("setting SO_TIMESTAMPING: %w", err)
	}
	return typ, nil
}

// timestamps is struct scm_timestamping64, the payload of SO_TIMESTAMPING_NEW
// control messages: the software timestamp, then two hardware ones.
type timestamps [3]struct{ sec, nsec int64 }

// ReadFull reads exactly len(buf) bytes from the connection with recvmsg(2),
// as io.ReadFull reads them, and returns the receive timestamp of the last
// call: that of the packet that held the last byte. It is the zero Time when
// the kernel gave none. The deadline of the socket ends it, as it ends a
// read; so does ctx, at once, when the read waits without Go's poller.
func (c *Conn) ReadFull(ctx context.Context, buf []byte) (time.Time, error) {
	var (
		got int
		rx  time.Time
	)
	defer func() { c.buf = nil }()
	for got < len(buf) {
		c.buf = buf[got:]
		err := c.raw.Read(c.read)
		switch {
		case notPollable(err):
			// Until the poller sees the socket readable or writable, it
			// refuses to wait: wait and read without it.
			if err = c.recvWithoutPoller(ctx); errors.Is(err, unix.EAGAIN) {
				continue
			}
			// Data that came after the deadline of the socket belongs to no
			// round trip: the poller, which checks the deadline before its
			// error event, tells whether it passed.
			if err == nil {
				if derr := c.raw.Read(done); derr != nil && !notPollable(derr) {
					err = derr
				}
			}
		case err == nil:
			err = c.err
		}
		if err != nil {
			return time.Time{}, err
		}
		if c.n == 0 {
			if got == 0 {
				return time.Time{}, io.EOF
			}
			return time.Time{}, io.ErrUnexpectedEOF
		}
		got += c.n
		rx = c.received(c.oob[:c.oobn])
	}
	return rx, nil
}

// done is a function for raw.Read that reads nothing: raw.Read then only
// checks the socket's deadline.
func done(uintptr) bool { return true }

// received returns the receive timestamp of the control messages oob, or
// the zero Time.
func (c *Conn) received(oob []byte) time.Time {
	var rx time.Time
	for {
		level, typ, data, rest, ok := nextMessage(oob)
		if !ok {
			return rx
		}
		if level == unix.SOL_SOCKET && typ == c.typ {
			rx = software(data)
		}
		oob = rest
	}
}

// notPollable reports whether err is the poller's "not pollable"
// (internal/poll.ErrNotPollable, which is not exported). The poller returns
// it, without waiting, after it saw an error event alone on the socket:
// a transmit timestamp queued while the socket was neither readable nor
// writable (an echo not yet back, a send buffer full), which signals
// EPOLLERR. It keeps returning it until it sees the socket readable or
// writable. Past the socket's deadline, it returns the deadline's error
// instead.
func notPollable(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err.Error() == "not pollable" {
			return true
		}
	}
	return false
}

// pollWait is how long recvWithoutPoller waits for data before it lets the
// poller check the deadline of the socket: a deadline set otherwise than by
// the ctx of ReadFull ends the read at most that late.
const pollWait = 100 * time.Millisecond

// recvWithoutPoller reads the transmit timestamps queued, whose error event
// would wake the wait at once, waits up to pollWait for the socket, with the
// waiter of the process, or until ctx is done, and reads what it can without
// waiting. It returns EAGAIN when nothing came.
func (c *Conn) recvWithoutPoller(ctx context.Context) error {
	c.fallbacks++
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Drain(); err != nil {
		return err
	}
	w, err := processWaiter()
	if err != nil {
		return err
	}
	if err := w.wait(ctx, c.raw, pollWait); err != nil {
		return err
	}
	if err := c.raw.Control(c.recv); err != nil {
		return err
	}
	return c.err
}

// waiter waits for sockets to be readable, for the readers that Go's poller
// refuses to wait for (notPollable), with an epoll instance of its own: one
// goroutine, and so one thread, waits for them all, where poll(2) would
// block a thread per reader. The instance is level-triggered, so that data
// already there, or an error, wakes a wait at once.
type waiter struct {
	epfd  int
	mu    sync.Mutex
	woken map[int32]chan struct{}
}

// processWaiter returns the waiter of the process, started the first time
// a read needs it.
var processWaiter = sync.OnceValues(func() (*waiter, error) {
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("creating an epoll instance: %w", err)
	}
	w := &waiter{epfd: epfd, woken: map[int32]chan struct{}{}}
	go w.run()
	return w, nil
})

func (w *waiter) run() {
	events := make([]unix.EpollEvent, 128)
	for {
		n, err := unix.EpollWait(w.epfd, events, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			// The waits then end at their timeout, and the reads retry.
			return
		}
		w.mu.Lock()
		for _, ev := range events[:n] {
			if ch, ok := w.woken[ev.Fd]; ok {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
		w.mu.Unlock()
	}
}

// wait waits up to timeout, or until ctx is done, for the socket of raw to
// be readable, or to have an error or a hangup.
func (w *waiter) wait(ctx context.Context, raw syscall.RawConn, timeout time.Duration) error {
	ch := make(chan struct{}, 1)
	var (
		key  int32
		cerr error
	)
	err := raw.Control(func(fd uintptr) {
		key = int32(fd) //nolint:gosec // a file descriptor
		w.mu.Lock()
		w.woken[key] = ch
		w.mu.Unlock()
		ev := unix.EpollEvent{Events: unix.EPOLLIN | unix.EPOLLONESHOT, Fd: key}
		cerr = unix.EpollCtl(w.epfd, unix.EPOLL_CTL_ADD, int(fd), &ev)
		if errors.Is(cerr, unix.EEXIST) {
			cerr = unix.EpollCtl(w.epfd, unix.EPOLL_CTL_MOD, int(fd), &ev)
		}
	})
	if err == nil {
		err = cerr
	}
	defer func() {
		_ = raw.Control(func(fd uintptr) { _ = unix.EpollCtl(w.epfd, unix.EPOLL_CTL_DEL, int(fd), nil) })
		w.mu.Lock()
		delete(w.woken, key)
		w.mu.Unlock()
	}()
	if err != nil {
		return fmt.Errorf("waiting for the socket: %w", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
	case <-ctx.Done():
	}
	return nil
}

// OutOfOrder returns the number of packets that the connection received out
// of order since it was opened (tcpi_rcv_ooopack, Linux 5.4), or false when
// the kernel does not count them.
func (c *Conn) OutOfOrder() (uint32, bool, error) {
	var (
		info unix.TCPInfo
		gerr error
	)
	size := uint32(unsafe.Sizeof(info))
	err := c.raw.Control(func(fd uintptr) {
		_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, fd, unix.IPPROTO_TCP, unix.TCP_INFO,
			uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&size)), 0)
		if errno != 0 {
			gerr = errno
		}
	})
	if err == nil {
		err = gerr
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading TCP_INFO: %w", err)
	}
	if uintptr(size) < unsafe.Offsetof(info.Rcv_ooopack)+unsafe.Sizeof(info.Rcv_ooopack) {
		return 0, false, nil
	}
	return info.Rcv_ooopack, true, nil
}

// Drain reads the transmit timestamps that the kernel queued (the error
// queue), without blocking. Of several timestamps of one byte (devices
// stacked, retransmissions), the first counts.
func (c *Conn) Drain() error {
	for {
		err := c.raw.Control(c.recvErrors)
		if err == nil {
			err = c.errErr
		}
		if errors.Is(err, unix.EAGAIN) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the transmit timestamps: %w", err)
		}
		if c.errFlags&unix.MSG_CTRUNC != 0 {
			return errors.New("reading the transmit timestamps: control messages truncated")
		}
		var (
			ts      time.Time
			key     uint32
			stamped bool
		)
		for oob := c.oob[:c.errOOBN]; ; {
			level, typ, data, rest, ok := nextMessage(oob)
			if !ok {
				break
			}
			oob = rest
			switch {
			case level == unix.SOL_SOCKET && typ == c.typ:
				ts = software(data)
			case level == unix.SOL_IP && typ == unix.IP_RECVERR, level == unix.SOL_IPV6 && typ == unix.IPV6_RECVERR:
				if len(data) < sizeofSockExtendedErr {
					continue
				}
				e := (*unix.SockExtendedErr)(unsafe.Pointer(&data[0]))
				if e.Errno == uint32(unix.ENOMSG) && e.Origin == unix.SO_EE_ORIGIN_TIMESTAMPING && e.Info == unix.SCM_TSTAMP_SND {
					key, stamped = e.Data, true
				}
			}
		}
		if !stamped || ts.IsZero() {
			continue
		}
		if _, ok := c.tx[key]; !ok {
			c.tx[key] = ts
		}
	}
}

// Transmitted returns the transmit timestamp of the byte key, among those
// Drain read and Forget kept.
func (c *Conn) Transmitted(key uint32) (time.Time, bool) {
	ts, ok := c.tx[key]
	return ts, ok
}

// Forget drops the timestamps of the bytes up to key, included. Keys wrap
// around after 4 GiB: a key is up to key when it is at most 2 GiB before.
func (c *Conn) Forget(key uint32) {
	for k := range c.tx {
		if int32(k-key) <= 0 { //nolint:gosec // the difference of two keys, modulo 2^32
			delete(c.tx, k)
		}
	}
}

// nextMessage returns the first control message of oob, and the messages
// after it, or false when oob holds none, or one cut short.
func nextMessage(oob []byte) (level, typ int32, data, rest []byte, ok bool) {
	if len(oob) < unix.SizeofCmsghdr {
		return 0, 0, nil, nil, false
	}
	h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
	// A length above the int range becomes negative, and is refused.
	l := int(h.Len) //nolint:gosec // see above
	if l < unix.CmsgLen(0) || l > len(oob) {
		return 0, 0, nil, nil, false
	}
	next := min(unix.CmsgSpace(l-unix.CmsgLen(0)), len(oob))
	return h.Level, h.Type, oob[unix.CmsgLen(0):l], oob[next:], true
}

// eachMessage calls f with every control message of oob.
func eachMessage(oob []byte, f func(level, typ int32, data []byte)) {
	for {
		level, typ, data, rest, ok := nextMessage(oob)
		if !ok {
			return
		}
		f(level, typ, data)
		oob = rest
	}
}

// software returns the software timestamp of an SO_TIMESTAMPING_NEW
// control message, or the zero Time when it has none.
func software(data []byte) time.Time {
	if len(data) < 16 {
		return time.Time{}
	}
	// struct __kernel_timespec: two signed 64-bit integers.
	sec := int64(binary.NativeEndian.Uint64(data[0:8]))   //nolint:gosec // see above
	nsec := int64(binary.NativeEndian.Uint64(data[8:16])) //nolint:gosec // see above
	if sec == 0 && nsec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, nsec)
}

// Prepare turns on the receive timestamps of the host and waits until
// packets get them. The kernel stamps received packets only while a socket
// asks for it, and turns that on a moment after the first one does
// (net_enable_timestamp, through a work queue): the packets received in
// between have no timestamp. Prepare asks with a UDP socket on the loopback
// device, sends datagrams to it until one is stamped, and keeps it open
// until release is called.
func Prepare(timeout time.Duration) (release func(), err error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening a UDP socket: %w", err)
	}
	defer func() {
		if err != nil {
			_ = unix.Close(fd)
		}
	}()
	loopback := &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}
	if err := unix.Bind(fd, loopback); err != nil {
		return nil, fmt.Errorf("binding a UDP socket to the loopback device: %w", err)
	}
	self, err := unix.Getsockname(fd)
	if err != nil {
		return nil, err
	}
	if err := unix.Connect(fd, self); err != nil {
		return nil, fmt.Errorf("connecting a UDP socket to itself: %w", err)
	}
	typ, err := option(fd, unix.SOF_TIMESTAMPING_RX_SOFTWARE|unix.SOF_TIMESTAMPING_SOFTWARE)
	if err != nil {
		return nil, fmt.Errorf("setting SO_TIMESTAMPING: %w", err)
	}
	wait := unix.NsecToTimeval((10 * time.Millisecond).Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &wait); err != nil {
		return nil, err
	}
	oob := make([]byte, unix.CmsgSpace(int(unsafe.Sizeof(timestamps{}))))
	var buf [1]byte
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if _, err := unix.Write(fd, buf[:]); err != nil {
			return nil, fmt.Errorf("sending to the loopback device: %w", err)
		}
		_, oobn, _, _, err := unix.Recvmsg(fd, buf[:], oob, 0)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("receiving from the loopback device: %w", err)
		}
		stamped := false
		eachMessage(oob[:oobn], func(level, t int32, data []byte) {
			if level == unix.SOL_SOCKET && int(t) == typ && !software(data).IsZero() {
				stamped = true
			}
		})
		if stamped {
			return func() { _ = unix.Close(fd) }, nil
		}
		time.Sleep(time.Millisecond)
	}
	return nil, fmt.Errorf("received packets got no timestamp within %v", timeout)
}
