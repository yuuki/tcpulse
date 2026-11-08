package kevents

import "errors"

// ErrNotTraced is returned by ReadEvents for a connection that the programs
// do not count: one that tcpulse did not open with connect(2) while the
// Tracer was running.
var ErrNotTraced = errors.New("connection not traced")
