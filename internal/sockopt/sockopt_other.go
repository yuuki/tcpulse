//go:build !linux

package sockopt

import (
	"errors"
	"net"
	"syscall"
)

// DialControl does nothing outside Linux.
func DialControl(_, _ string, _ syscall.RawConn) error { return nil }

// ListenControl does nothing outside Linux.
func ListenControl(_, _ string, _ syscall.RawConn) error { return nil }

// SetQuickAck does nothing outside Linux.
func SetQuickAck(net.Conn) error { return nil }

// TCPInfoSupported reports whether ReadTCPInfo works on this platform.
const TCPInfoSupported = false

// ReadTCPInfo is not supported outside Linux.
func ReadTCPInfo(net.Conn) (TCPInfo, error) { return TCPInfo{}, errors.ErrUnsupported }
