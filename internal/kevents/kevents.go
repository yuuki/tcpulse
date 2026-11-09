package kevents

import "errors"

// ErrNotTraced is returned by ReadEvents for a connection that the programs
// do not count: one that tcpulse did not open with connect(2) while the
// Tracer was running.
var ErrNotTraced = errors.New("connection not traced")

// Options says what a Tracer counts.
type Options struct {
	// Events counts the kernel events of the TCP connections that the
	// process opens (ReadEvents).
	Events bool
	// RunQueue measures how long the threads of the process wait for a CPU
	// (RunQueue).
	RunQueue bool
}
