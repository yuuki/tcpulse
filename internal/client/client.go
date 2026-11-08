// Package client implements the load generator of tcpulse: it sends
// messages to an echo server at a fixed rate and records how long each takes
// to come back.
package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"go.uber.org/ratelimit"
	"golang.org/x/sync/errgroup"

	"github.com/yuuki/tcpulse/internal/sockopt"
	"github.com/yuuki/tcpulse/internal/stats"
)

// Protocols.
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// Connection flavors of TCP.
const (
	// FlavorPersistent keeps Connections connections open and sends Rate
	// messages per second on each.
	FlavorPersistent = "persistent"
	// FlavorEphemeral opens Rate connections per second and sends one
	// message on each.
	FlavorEphemeral = "ephemeral"
)

// Config configures a run.
type Config struct {
	// Protocol is ProtocolTCP or ProtocolUDP.
	Protocol string
	// Flavor is FlavorPersistent or FlavorEphemeral; UDP ignores it and
	// sends every message from a new socket.
	Flavor string
	// Connections is the number of concurrent connections of
	// FlavorPersistent.
	Connections int
	// Rate is the number of messages per second per connection
	// (FlavorPersistent), or of connections per second (FlavorEphemeral and
	// UDP).
	Rate int
	// Duration is how long the run lasts.
	Duration time.Duration
	// MessageBytes is the size of every message.
	MessageBytes int
	// TCPInfo records in the Recorder what the kernel reports of every TCP
	// connection (TCP_INFO) when it is closed, once the clock of its last
	// round trip has stopped. It needs TCPInfoSupported; UDP ignores it.
	TCPInfo bool
	// Events, when not nil, reads the kernel events of every TCP connection
	// after each of its round trips, once the clock has stopped, and
	// records them in the Recorder with the round trip. UDP ignores it.
	Events EventReader
	// Breakdown splits every round trip of FlavorPersistent TCP into send,
	// network and receive time, by the kernel's timestamps of the last byte
	// of the message (SO_TIMESTAMPING), and records the parts in the
	// Recorder. It needs BreakdownSupported, and the receive timestamps of
	// the host turned on (PrepareBreakdown); FlavorEphemeral and UDP ignore
	// it.
	Breakdown bool
	// Logger receives the errors the run tolerates; nil means
	// slog.Default().
	Logger *slog.Logger
}

// Run puts load on the echo server at addr for cfg.Duration and records the
// round trip time of every message in rec. It returns nil when the run ends
// or ctx is canceled, and an error when a connection fails. The requests
// still in flight when the run ends are not recorded: the end of the run cut
// them off, and the echoes of lost UDP datagrams are waited for until then.
func Run(ctx context.Context, cfg Config, addr string, rec *stats.Recorder) error {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	r := &run{cfg: cfg, addr: addr, rec: rec, logger: cfg.Logger}
	r.bufPool.New = func() any {
		buf := make([]byte, cfg.MessageBytes)
		return &buf
	}

	var err error
	switch {
	case cfg.Protocol == ProtocolTCP && cfg.Flavor == FlavorPersistent:
		err = r.persistent(ctx)
	case cfg.Protocol == ProtocolTCP && cfg.Flavor == FlavorEphemeral:
		err = r.ephemeral(ctx)
	case cfg.Protocol == ProtocolUDP:
		err = r.udp(ctx)
	default:
		return fmt.Errorf("invalid protocol or flavor combination: %q, %q", cfg.Protocol, cfg.Flavor)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		r.logger.Warn("context canceled", "error", err)
		return nil
	}
	return err
}

// TCPInfoSupported reports whether Config.TCPInfo works on this platform
// (Linux).
const TCPInfoSupported = sockopt.TCPInfoSupported

// EventReader reads the kernel events counted on a TCP socket since it was
// opened (--kernel-events). It may be called concurrently.
type EventReader interface {
	// Generation returns a number that changes whenever an event is counted
	// on any socket, cheaply, and false when there is none. While it does
	// not change, ReadEvents returns the same events for every socket; an
	// event counted before it changes is seen by ReadEvents after it.
	Generation() (uint64, bool)
	// ReadEvents reads the events of the socket of descriptor fd, which may
	// be a duplicate of the descriptor of a connection.
	ReadEvents(fd int) (stats.Events, error)
}

// errTolerated is returned by a request that failed in a way the run
// tolerates: the failure is logged, and the request is not recorded.
var errTolerated = errors.New("request failed")

type run struct {
	cfg     Config
	addr    string
	rec     *stats.Recorder
	logger  *slog.Logger
	bufPool sync.Pool
	// tcpInfoWarning logs once a connection whose TCP_INFO could not be
	// read.
	tcpInfoWarning sync.Once
	// eventWarning logs once a connection whose events could not be read.
	eventWarning sync.Once
	// breakdownWarning logs once a connection whose timestamps could not be
	// turned on or read.
	breakdownWarning sync.Once
}

// Messages returns how many messages a sender at rate per second sends
// during d: rate × d, rounded down. ok is false when the count does not fit
// in an int64 (or rate or d is negative).
func Messages(rate int, d time.Duration) (n int64, ok bool) {
	if rate < 0 || d < 0 {
		return 0, false
	}
	r := int64(rate)
	secs, frac := int64(d/time.Second), int64(d%time.Second)
	if r != 0 && secs > math.MaxInt64/r {
		return 0, false
	}
	// frac < 1e9, so r×frac fits whenever r < 2^33; a larger rate overflows
	// the whole count anyway once d is a second or more.
	if r != 0 && frac > math.MaxInt64/r {
		return 0, false
	}
	whole, part := r*secs, r*frac/int64(time.Second)
	if whole > math.MaxInt64-part {
		return 0, false
	}
	return whole + part, true
}

// messages returns how many messages each sender of the run sends; the
// command line validates that the count fits.
func (r *run) messages() int64 {
	n, _ := Messages(r.cfg.Rate, r.cfg.Duration)
	return n
}

// finish records a round trip that took elapsed, or returns the error of a
// failed request. A request that failed with errTolerated, or that the end
// of the run cut off, is neither recorded nor an error.
func (r *run) finish(ctx context.Context, elapsed time.Duration, err error) error {
	if err != nil {
		if errors.Is(err, errTolerated) || cutOff(ctx, err) {
			return nil
		}
		return err
	}
	r.rec.Record(elapsed)
	return nil
}

// cutOff reports whether err comes from the end of the run, ctx, rather than
// from the connection: either ctx is done, or err is a timeout (of a dial,
// or of the connection deadline, which is the deadline of ctx: see
// bindToRun) and the deadline of ctx has passed. The I/O can time out a
// moment before ctx reports it.
func cutOff(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		deadline, ok := ctx.Deadline()
		return ok && !time.Now().Before(deadline)
	}
	return false
}

// closeTCP closes a TCP connection, after recording what the kernel reports
// of it when the run asks for it: the events that none of its completed
// round trips took (events may be nil), and its TCP_INFO. It runs after the
// clock of the last round trip of the connection has stopped.
func (r *run) closeTCP(conn net.Conn, events *eventCursor) {
	r.recordUnmatched(events)
	events.close()
	if r.cfg.TCPInfo {
		r.recordTCPInfo(conn)
	}
	_ = conn.Close()
}

// eventCursor attributes the kernel events of a TCP connection to its round
// trips: take returns the events counted since its previous call. It reads
// them through fd, a duplicate of the descriptor of the connection's socket,
// which close closes.
type eventCursor struct {
	run  *run
	fd   int
	seen stats.Events
	// generation is the reader's generation before the latest read, and
	// read is set once there was one.
	generation uint64
	read       bool
	// broken is set once the events of the connection could not be read.
	broken bool
}

// dialTCP dials the target. When the run reads kernel events, it also
// returns the cursor of the connection, and records as unmatched the events
// of the dial attempts that did not become the connection: those of a dial
// that failed or was cut off (SYNs never answered, a reset), whose socket
// the dialer closes. A duplicate of the descriptor of every attempt, taken
// in the dialer's Control (one dup(2), during the dial), keeps them
// readable.
func (r *run) dialTCP(ctx context.Context, dialer *net.Dialer) (net.Conn, *eventCursor, error) {
	if r.cfg.Events == nil {
		conn, err := dialer.DialContext(ctx, "tcp", r.addr)
		return conn, nil, err
	}
	var held heldSockets
	d := *dialer
	d.Control = held.control(dialer.Control)
	conn, err := d.DialContext(ctx, "tcp", r.addr)
	return conn, r.adopt(&held, conn, err), err
}

// heldSockets holds a duplicate of the descriptor of every socket a dial
// attempts to connect; the attempts may run concurrently (dual-stack
// fallback).
type heldSockets struct {
	mu  sync.Mutex
	fds []int
	err error
}

func (h *heldSockets) control(next func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		if next != nil {
			if err := next(network, address, c); err != nil {
				return err
			}
		}
		return c.Control(func(fd uintptr) {
			dup, err := dupSocket(fd)
			h.mu.Lock()
			defer h.mu.Unlock()
			if err != nil {
				h.err = err
				return
			}
			h.fds = append(h.fds, dup)
		})
	}
}

// adopt sorts the sockets of a dial: the descriptor of the connection's
// socket goes to its cursor, nil when the dial failed. The events of the
// other attempts are recorded as unmatched, and their descriptors closed.
// After a dial that succeeded at its only attempt, it makes no system call.
func (r *run) adopt(held *heldSockets, conn net.Conn, err error) *eventCursor {
	var cursor *eventCursor
	if err == nil {
		cursor = &eventCursor{run: r, fd: -1}
	}
	for _, fd := range held.fds {
		if cursor != nil && cursor.fd < 0 && (len(held.fds) == 1 || sameSocket(fd, conn)) {
			cursor.fd = fd
			continue
		}
		if ev, err := r.cfg.Events.ReadEvents(fd); err == nil && !ev.IsZero() {
			r.rec.RecordUnmatched(ev)
		}
		closeFD(fd)
	}
	if cursor != nil && cursor.fd < 0 {
		cursor.broken = true
		r.eventWarning.Do(func() {
			r.logger.Warn("could not keep the socket of a connection to read its kernel events", "addr", r.addr, "error", held.err)
		})
	}
	return cursor
}

// close closes the descriptor of the cursor.
func (c *eventCursor) close() {
	if c != nil && c.fd >= 0 {
		closeFD(c.fd)
		c.fd = -1
	}
}

// take returns the events counted on the connection since the previous
// call, or false when they cannot be read.
func (c *eventCursor) take() (stats.Events, bool) {
	if c == nil || c.broken || c.fd < 0 {
		return stats.Events{}, false
	}
	// The generation is read before the events: an event counted after it
	// is read changes it, and the next take reads the events again.
	gen, ok := c.run.cfg.Events.Generation()
	if ok && c.read && gen == c.generation {
		return stats.Events{}, true
	}
	ev, err := c.run.cfg.Events.ReadEvents(c.fd)
	if err != nil {
		c.broken = true
		c.run.eventWarning.Do(func() {
			c.run.logger.Warn("could not read the kernel events of a connection", "addr", c.run.addr, "error", err)
		})
		return stats.Events{}, false
	}
	d := ev.Sub(c.seen)
	c.seen, c.generation, c.read = ev, gen, true
	return d, true
}

// recordEvents records the events of a round trip that took elapsed, once
// its clock has stopped. A round trip whose events cannot be read is counted
// as untracked, never as one without events.
func (r *run) recordEvents(events *eventCursor, elapsed time.Duration) {
	if events == nil {
		return
	}
	if d, ok := events.take(); ok {
		r.rec.RecordEvents(elapsed, d)
	} else {
		r.rec.RecordUntracked()
	}
}

// recordUnmatched records the events of the connection that no completed
// round trip takes.
func (r *run) recordUnmatched(events *eventCursor) {
	if d, ok := events.take(); ok && !d.IsZero() {
		r.rec.RecordUnmatched(d)
	}
}

// recordTCPInfo records the TCP_INFO of conn. A connection whose TCP_INFO
// cannot be read is not recorded: the report counts the connections it
// describes.
func (r *run) recordTCPInfo(conn net.Conn) {
	info, err := sockopt.ReadTCPInfo(conn)
	if err != nil {
		r.tcpInfoWarning.Do(func() {
			r.logger.Warn("could not read TCP_INFO", "addr", r.addr, "error", err)
		})
		return
	}
	r.rec.RecordConn(stats.Conn(info))
}

// fill puts random bytes in a message.
func fill(msg []byte) {
	_, _ = rand.Read(msg) // never fails since Go 1.24
}

// waitLimit waits for the next slot of rl, or for ctx to end.
func waitLimit(ctx context.Context, rl ratelimit.Limiter) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		rl.Take()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// bindToRun makes the I/O on conn give up when the run ends: at the deadline
// of ctx, or at once when ctx is canceled (Ctrl-C, another peer failing).
// The returned function releases conn from ctx.
func bindToRun(ctx context.Context, conn net.Conn) (release func() bool) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
}

// concurrentEchoBytes is the size above which a TCP message is written while
// its echo is read. The server writes back as it reads, so when a message
// does not fit in the socket buffers of both sides, writing all of it before
// reading would block the client and the server on each other.
const concurrentEchoBytes = 64 * 1024

// exchange writes msg on conn and reads its whole echo into echo, which may
// be msg itself when msg is at most concurrentEchoBytes long. The echo of a
// message larger than a segment, or than the server's buffer, arrives in
// several reads.
func exchange(conn net.Conn, msg, echo []byte) error {
	if len(msg) <= concurrentEchoBytes {
		if _, err := conn.Write(msg); err != nil {
			return fmt.Errorf("writing to connection: %w", err)
		}
		if _, err := io.ReadFull(conn, echo); err != nil {
			return fmt.Errorf("reading from connection: %w", err)
		}
		return nil
	}

	written := make(chan error, 1)
	go func() {
		_, err := conn.Write(msg)
		written <- err
	}()
	_, readErr := io.ReadFull(conn, echo)
	if readErr != nil {
		// Unblock the write: the connection is done. The write then fails
		// because of the deadline: the read's error is the one to report.
		_ = conn.SetDeadline(time.Now())
		if err := <-written; err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			return fmt.Errorf("reading from connection: %w (and writing: %w)", readErr, err)
		}
		return fmt.Errorf("reading from connection: %w", readErr)
	}
	if err := <-written; err != nil {
		return fmt.Errorf("writing to connection: %w", err)
	}
	return nil
}

// echoBuffer returns the buffer exchange reads the echo of msg into.
func echoBuffer(msg []byte) []byte {
	if len(msg) <= concurrentEchoBytes {
		return msg
	}
	return make([]byte, len(msg))
}

func (r *run) persistent(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Duration)
	defer cancel()

	dialer := net.Dialer{Control: sockopt.DialControl}
	eg, ctx := errgroup.WithContext(ctx)
	for range r.cfg.Connections {
		eg.Go(func() error {
			conn, events, err := r.dialTCP(ctx, &dialer)
			if err != nil {
				if cutOff(ctx, err) {
					return nil
				}
				return fmt.Errorf("dialing %q: %w", r.addr, err)
			}
			defer r.closeTCP(conn, events)
			defer bindToRun(ctx, conn)()
			// The dial is not timed: the events of a handshake it waited
			// for belong to no round trip. With TCP Fast Open the SYN
			// leaves with the first message, and its events with the first
			// round trip.
			r.recordUnmatched(events)

			var split *splitter
			if r.cfg.Breakdown {
				split = r.newSplitter(conn)
				defer split.close()
			}

			msg := make([]byte, r.cfg.MessageBytes)
			echo := echoBuffer(msg)
			limiter := ratelimit.New(r.cfg.Rate)
			for range r.messages() {
				if err := waitLimit(ctx, limiter); err != nil {
					return nil
				}
				fill(msg)
				var (
					received time.Time
					err      error
				)
				timed := split.timed()
				start := time.Now()
				if timed {
					received, err = exchangeTimestamped(conn, split.ts, msg, echo)
				} else {
					err = exchange(conn, msg, echo)
				}
				elapsed := time.Since(start)
				end := split.now()
				if err == nil {
					r.recordEvents(events, elapsed)
					split.roundTrip(start, end, received, len(msg))
				}
				if err := r.finish(ctx, elapsed, err); err != nil {
					return err
				}
				if err != nil {
					// Cut off by the end of the run.
					return nil
				}
			}
			return nil
		})
	}
	return eg.Wait()
}

func (r *run) ephemeral(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Duration)
	defer cancel()

	dialer := net.Dialer{Control: sockopt.DialControl}
	limiter := ratelimit.New(r.cfg.Rate)
	eg, ctx := errgroup.WithContext(ctx)
	for range r.messages() {
		if err := waitLimit(ctx, limiter); err != nil {
			break
		}
		eg.Go(func() error {
			msgp := r.bufPool.Get().(*[]byte)
			defer r.bufPool.Put(msgp)
			msg, echo := *msgp, *msgp
			if len(msg) > concurrentEchoBytes {
				echop := r.bufPool.Get().(*[]byte)
				defer r.bufPool.Put(echop)
				echo = *echop
			}
			fill(msg)
			elapsed, err := r.ephemeralRequest(ctx, &dialer, msg, echo)
			return r.finish(ctx, elapsed, err)
		})
	}
	return eg.Wait()
}

// ephemeralRequest connects, sends msg, reads its echo and returns how long
// that took; the connection is closed after the clock stops (the deferred
// calls run after the result is evaluated).
func (r *run) ephemeralRequest(ctx context.Context, dialer *net.Dialer, msg, echo []byte) (time.Duration, error) {
	start := time.Now()
	// The round trip includes the dial: the events of the connection are
	// all its own.
	conn, events, err := r.dialTCP(ctx, dialer)
	if err != nil {
		if errors.Is(err, syscall.ETIMEDOUT) {
			r.logger.Warn("connection timeout", "addr", r.addr)
			return 0, errTolerated
		}
		return 0, fmt.Errorf("dialing %q: %w", r.addr, err)
	}
	defer r.closeTCP(conn, events)
	defer bindToRun(ctx, conn)()

	if err := sockopt.SetQuickAck(conn); err != nil {
		return 0, fmt.Errorf("setting quick ack: %w", err)
	}

	if err := exchange(conn, msg, echo); err != nil {
		switch {
		case errors.Is(err, syscall.EINPROGRESS):
			r.logger.Warn("write in progress", "addr", r.addr)
			return 0, errTolerated
		case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
			r.logger.Warn("connection reset", "addr", r.addr)
			return 0, errTolerated
		}
		return 0, err
	}
	elapsed := time.Since(start)
	r.recordEvents(events, elapsed)
	return elapsed, nil
}

func (r *run) udp(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Duration)
	defer cancel()

	var dialer net.Dialer
	limiter := ratelimit.New(r.cfg.Rate)
	eg, ctx := errgroup.WithContext(ctx)
	for range r.messages() {
		if err := waitLimit(ctx, limiter); err != nil {
			break
		}
		eg.Go(func() error {
			msgp := r.bufPool.Get().(*[]byte)
			defer r.bufPool.Put(msgp)
			fill(*msgp)
			elapsed, err := r.udpRequest(ctx, &dialer, *msgp)
			return r.finish(ctx, elapsed, err)
		})
	}
	return eg.Wait()
}

// udpRequest sends msg from a new socket, reads its echo and returns how long
// that took; the socket is closed after the clock stops.
func (r *run) udpRequest(ctx context.Context, dialer *net.Dialer, msg []byte) (time.Duration, error) {
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "udp4", r.addr)
	if err != nil {
		return 0, fmt.Errorf("dialing UDP %q: %w", r.addr, err)
	}
	defer func() { _ = conn.Close() }()
	defer bindToRun(ctx, conn)()

	if _, err := conn.Write(msg); err != nil {
		return 0, fmt.Errorf("writing to UDP connection: %w", err)
	}
	if _, err := conn.Read(msg); err != nil {
		return 0, fmt.Errorf("reading from UDP connection: %w", err)
	}
	return time.Since(start), nil
}
