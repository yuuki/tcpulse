//go:build !linux

package sockopt

import (
	"net"
	"syscall"
)

// DialControl does nothing outside Linux.
func DialControl(_, _ string, _ syscall.RawConn) error { return nil }

// ListenControl does nothing outside Linux.
func ListenControl(_, _ string, _ syscall.RawConn) error { return nil }

// SetQuickAck does nothing outside Linux.
func SetQuickAck(net.Conn) error { return nil }
