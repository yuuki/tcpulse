// Package tstamp reads the kernel's software timestamps of a TCP connection
// (SO_TIMESTAMPING, Linux): when the last byte of a send request left the
// host, keyed by its offset in the stream (OPT_ID), and when the data that a
// read returned was received.
//
// See Documentation/networking/timestamping.rst in the kernel's sources:
// a transmit timestamp is that of the last byte of a send request (one
// write(2)), and the receive timestamp of a recvmsg(2) is that of the last
// packet it read.
package tstamp

import "errors"

// ErrNotReady is returned by Enable for a connection whose stream does not
// start at a known offset yet: not established (TCP Fast Open defers the
// handshake to the first write), or with bytes written and not acknowledged
// on a kernel without OPT_ID_TCP.
var ErrNotReady = errors.New("the connection's stream is not at a known offset")
