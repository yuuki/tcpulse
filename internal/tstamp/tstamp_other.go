//go:build !linux

package tstamp

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// Supported reports whether Enable can work on this platform: it cannot.
const Supported = false

// Conn is not supported on this platform.
type Conn struct{}

// Enable is not supported on this platform.
func Enable(syscall.Conn) (*Conn, error) { return nil, errors.ErrUnsupported }

// ReadFull is not supported on this platform.
func (*Conn) ReadFull(context.Context, []byte) (time.Time, error) {
	return time.Time{}, errors.ErrUnsupported
}

// Drain is not supported on this platform.
func (*Conn) Drain() error { return errors.ErrUnsupported }

// Transmitted returns nothing on this platform.
func (*Conn) Transmitted(uint32) (time.Time, bool) { return time.Time{}, false }

// Forget does nothing on this platform.
func (*Conn) Forget(uint32) {}

// Prepare is not supported on this platform.
func Prepare(time.Duration) (func(), error) { return nil, errors.ErrUnsupported }

// OutOfOrder is not supported on this platform.
func (*Conn) OutOfOrder() (uint32, bool, error) { return 0, false, errors.ErrUnsupported }
