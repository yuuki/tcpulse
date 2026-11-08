//go:build linux

package tstamp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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

// Conn reads the timestamps of a TCP connection. Its methods must not be
// called concurrently.
type Conn struct {
	raw syscall.RawConn
	// oob receives control messages; payload the (empty) payload of the
	// messages of the error queue.
	oob     []byte
	payload [1]byte
	// tx holds the first transmit timestamp of each key not yet forgotten.
	tx map[uint32]time.Time
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
	var serr error
	err = raw.Control(func(fd uintptr) { serr = enable(int(fd)) })
	if err == nil {
		err = serr
	}
	if err != nil {
		return nil, err
	}
	// Room for a timestamp, and the error with its offender address.
	oob := make([]byte, unix.CmsgSpace(int(unsafe.Sizeof(timestamps{})))+
		unix.CmsgSpace(sizeofSockExtendedErr+unix.SizeofSockaddrInet6))
	return &Conn{raw: raw, oob: oob, tx: map[uint32]time.Time{}}, nil
}

func enable(fd int) error {
	info, err := unix.GetsockoptTCPInfo(fd, unix.IPPROTO_TCP, unix.TCP_INFO)
	if err != nil {
		return fmt.Errorf("reading TCP_INFO: %w", err)
	}
	if info.State != tcpEstablished {
		return ErrNotReady
	}
	// With OPT_ID_TCP (Linux 6.2), the keys count from the next byte
	// written; without, from the first byte not acknowledged.
	err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPING_NEW, flags|unix.SOF_TIMESTAMPING_OPT_ID_TCP)
	if errors.Is(err, unix.EINVAL) {
		if info.Unacked != 0 || info.Notsent_bytes != 0 {
			return ErrNotReady
		}
		err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPING_NEW, flags)
	}
	if err != nil {
		return fmt.Errorf("setting SO_TIMESTAMPING: %w", err)
	}
	return nil
}

// timestamps is struct scm_timestamping64, the payload of SO_TIMESTAMPING_NEW
// control messages: the software timestamp, then two hardware ones.
type timestamps [3]struct{ sec, nsec int64 }

// ReadFull reads exactly len(buf) bytes from the connection with recvmsg(2),
// as io.ReadFull reads them, and returns the receive timestamp of the last
// call: that of the packet that held the last byte. It is the zero Time when
// the kernel gave none.
func (c *Conn) ReadFull(buf []byte) (time.Time, error) {
	var (
		got int
		rx  time.Time
	)
	for got < len(buf) {
		var (
			n, oobn int
			rerr    error
		)
		err := c.raw.Read(func(fd uintptr) bool {
			n, oobn, _, _, rerr = unix.Recvmsg(int(fd), buf[got:], c.oob, 0)
			return rerr != unix.EAGAIN
		})
		if err == nil {
			err = rerr
		}
		if err != nil {
			return time.Time{}, err
		}
		if n == 0 {
			if got == 0 {
				return time.Time{}, io.EOF
			}
			return time.Time{}, io.ErrUnexpectedEOF
		}
		got += n
		rx = time.Time{}
		eachMessage(c.oob[:oobn], func(level, typ int32, data []byte) {
			if level == unix.SOL_SOCKET && typ == unix.SO_TIMESTAMPING_NEW {
				rx = software(data)
			}
		})
	}
	return rx, nil
}

// Drain reads the transmit timestamps that the kernel queued (the error
// queue), without blocking. Of several timestamps of one byte (devices
// stacked, retransmissions), the first counts.
func (c *Conn) Drain() error {
	for {
		var (
			oobn  int
			rflag int
			rerr  error
		)
		err := c.raw.Control(func(fd uintptr) {
			_, oobn, rflag, _, rerr = unix.Recvmsg(int(fd), c.payload[:], c.oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
		})
		if err == nil {
			err = rerr
		}
		if errors.Is(err, unix.EAGAIN) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the transmit timestamps: %w", err)
		}
		if rflag&unix.MSG_CTRUNC != 0 {
			return errors.New("reading the transmit timestamps: control messages truncated")
		}
		var (
			ts      time.Time
			key     uint32
			stamped bool
		)
		eachMessage(c.oob[:oobn], func(level, typ int32, data []byte) {
			switch {
			case level == unix.SOL_SOCKET && typ == unix.SO_TIMESTAMPING_NEW:
				ts = software(data)
			case level == unix.SOL_IP && typ == unix.IP_RECVERR, level == unix.SOL_IPV6 && typ == unix.IPV6_RECVERR:
				if len(data) < sizeofSockExtendedErr {
					return
				}
				e := (*unix.SockExtendedErr)(unsafe.Pointer(&data[0]))
				if e.Errno == uint32(unix.ENOMSG) && e.Origin == unix.SO_EE_ORIGIN_TIMESTAMPING && e.Info == unix.SCM_TSTAMP_SND {
					key, stamped = e.Data, true
				}
			}
		})
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

// eachMessage calls f with every control message of oob.
func eachMessage(oob []byte, f func(level, typ int32, data []byte)) {
	for len(oob) >= unix.SizeofCmsghdr {
		h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
		// A length above the int range becomes negative, and is refused.
		l := int(h.Len) //nolint:gosec // see above
		if l < unix.CmsgLen(0) || l > len(oob) {
			return
		}
		f(h.Level, h.Type, oob[unix.CmsgLen(0):l])
		next := unix.CmsgSpace(l - unix.CmsgLen(0))
		if next > len(oob) {
			return
		}
		oob = oob[next:]
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
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPING_NEW,
		unix.SOF_TIMESTAMPING_RX_SOFTWARE|unix.SOF_TIMESTAMPING_SOFTWARE); err != nil {
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
		eachMessage(oob[:oobn], func(level, typ int32, data []byte) {
			if level == unix.SOL_SOCKET && typ == unix.SO_TIMESTAMPING_NEW && !software(data).IsZero() {
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
