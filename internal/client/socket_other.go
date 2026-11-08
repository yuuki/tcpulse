//go:build !unix

package client

import (
	"errors"
	"net"
)

// dupSocket is not supported on this platform, which has no kernel events.
func dupSocket(uintptr) (int, error) { return -1, errors.ErrUnsupported }

// sameSocket is not supported on this platform.
func sameSocket(int, net.Conn) bool { return false }

// closeFD does nothing on this platform.
func closeFD(int) {}
