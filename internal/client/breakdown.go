package client

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/yuuki/tcpulse/internal/tstamp"
)

// BreakdownSupported reports whether Config.Breakdown works on this platform
// (Linux).
const BreakdownSupported = tstamp.Supported

// splitter splits the round trips of a persistent TCP connection into send,
// network and receive time, by the kernel's timestamps of the last byte of
// each message: when it left the host, and when its echo was received
// (--latency-breakdown). A nil splitter does nothing.
//
// The keys of the transmit timestamps count the bytes written since the
// timestamps were turned on: right after the dial, or, when TCP Fast Open
// deferred the handshake to the first message, after the first round trip.
type splitter struct {
	run  *run
	conn net.Conn
	// ts reads the timestamps once they are on; broken is set when they
	// cannot be.
	ts     *tstamp.Conn
	broken bool
	// written counts the bytes written since the timestamps were turned on.
	written uint32
	// pending is the latest round trip, until its transmit timestamp is
	// read or given up.
	pending *timedRoundTrip
}

// timedRoundTrip is a completed round trip: when it started and ended, when
// the last byte of its echo was received (zero without a timestamp), and the
// key of the last byte of its message.
type timedRoundTrip struct {
	start, end, received time.Time
	last                 uint32
}

func (r *run) newSplitter(conn net.Conn) *splitter {
	s := &splitter{run: r, conn: conn}
	s.enable()
	return s
}

// enable turns the timestamps on, if the connection allows it yet.
func (s *splitter) enable() {
	ts, err := tstamp.Enable(s.conn.(syscall.Conn))
	switch {
	case err == nil:
		s.ts = ts
	case errors.Is(err, tstamp.ErrNotReady):
		// Tried again after the next round trip.
	default:
		s.broken = true
		s.warn("could not turn on the timestamps of a connection", err)
	}
}

func (s *splitter) warn(msg string, err error) {
	s.run.breakdownWarning.Do(func() {
		s.run.logger.Warn(msg, "addr", s.run.addr, "error", err)
	})
}

// timed reports whether the round trips read the receive timestamps.
func (s *splitter) timed() bool { return s != nil && s.ts != nil }

// now returns the system's time (CLOCK_REALTIME, the clock of the kernel's
// timestamps) right after the clock of a round trip stopped, or the zero
// Time for a nil splitter.
//
// A round trip's start is a time.Now, which reads the system's time just
// before the monotonic time it is timed with; its end is read here, after.
// Both bracket the round trip, so that a part can only be longer by the time
// between the readings, never negative: a thread preempted between them
// delays one by microseconds. The end cannot be the start plus the round
// trip's time: that would be early by the delay between the two readings of
// the start.
func (s *splitter) now() time.Time {
	if s == nil {
		return time.Time{}
	}
	return time.Now()
}

// roundTrip splits a completed round trip of a message of n bytes, once its
// clock has stopped. The transmit timestamp of its last byte may not be
// there yet: the round trip waits for the next one, or for the close.
func (s *splitter) roundTrip(start, end, received time.Time, n int) {
	if s == nil {
		return
	}
	if s.ts == nil {
		s.run.rec.RecordMissingBreakdown()
		if !s.broken {
			s.enable()
		}
		return
	}
	s.drain()
	// The previous round trip has waited for all of this one.
	s.settle()
	// The keys wrap around at 2^32, as the kernel's do (OPT_ID).
	s.written += uint32(n) //nolint:gosec // see above
	s.pending = &timedRoundTrip{start: start, end: end, received: received, last: s.written - 1}
	if _, ok := s.ts.Transmitted(s.pending.last); ok || received.IsZero() {
		s.settle()
	}
}

// close settles the last round trip, when the connection ends.
func (s *splitter) close() {
	if s == nil || s.ts == nil {
		return
	}
	s.drain()
	s.settle()
}

func (s *splitter) drain() {
	if err := s.ts.Drain(); err != nil {
		s.warn("could not read the transmit timestamps of a connection", err)
	}
}

// settle records the pending round trip: its breakdown when it has both
// timestamps and no negative part, or else what it lacks. Nothing stands in
// for a missing timestamp.
func (s *splitter) settle() {
	p := s.pending
	if p == nil {
		return
	}
	s.pending = nil
	sent, ok := s.ts.Transmitted(p.last)
	s.ts.Forget(p.last)
	if !ok || p.received.IsZero() {
		s.run.rec.RecordMissingBreakdown()
		return
	}
	send, network, receive := sent.Sub(p.start), p.received.Sub(sent), p.end.Sub(p.received)
	if send < 0 || network < 0 || receive < 0 {
		s.run.rec.RecordNegativeBreakdown()
		return
	}
	s.run.rec.RecordBreakdown(send, network, receive)
}

// exchangeTimestamped is exchange, reading the echo with ts, and returns
// the receive timestamp of its last byte (zero when the kernel gave none).
func exchangeTimestamped(conn net.Conn, ts *tstamp.Conn, msg, echo []byte) (time.Time, error) {
	if len(msg) <= concurrentEchoBytes {
		if _, err := conn.Write(msg); err != nil {
			return time.Time{}, fmt.Errorf("writing to connection: %w", err)
		}
		received, err := ts.ReadFull(echo)
		if err != nil {
			return time.Time{}, fmt.Errorf("reading from connection: %w", err)
		}
		return received, nil
	}

	written := make(chan error, 1)
	go func() {
		_, err := conn.Write(msg)
		written <- err
	}()
	received, readErr := ts.ReadFull(echo)
	if readErr != nil {
		// Unblock the write: the connection is done.
		_ = conn.SetDeadline(time.Now())
	}
	if err := <-written; err != nil {
		return time.Time{}, fmt.Errorf("writing to connection: %w", err)
	}
	if readErr != nil {
		return time.Time{}, fmt.Errorf("reading from connection: %w", readErr)
	}
	return received, nil
}
