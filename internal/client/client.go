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

// errTolerated is returned by a request that failed in a way the run
// tolerates: the failure is logged, and the request is not recorded.
var errTolerated = errors.New("request failed")

type run struct {
	cfg     Config
	addr    string
	rec     *stats.Recorder
	logger  *slog.Logger
	bufPool sync.Pool
}

// messages returns how many messages a sender at cfg.Rate per second sends
// during cfg.Duration.
func (r *run) messages() int64 {
	return int64(r.cfg.Rate) * int64(r.cfg.Duration.Seconds())
}

// measure records how long f takes, if it succeeds. The message must be
// ready before: generating it is not part of the round trip. A request that
// failed with errTolerated, or that the end of the run cut off, is neither
// recorded nor an error.
func (r *run) measure(ctx context.Context, f func() error) error {
	start := time.Now()
	if err := f(); err != nil {
		if errors.Is(err, errTolerated) || cutOff(ctx, err) {
			return nil
		}
		return err
	}
	r.rec.Record(time.Since(start))
	return nil
}

// cutOff reports whether err comes from the end of the run, ctx, rather than
// from the connection: either ctx is done, or err is the I/O timeout of the
// connection deadline, which is the deadline of ctx (see setDeadline), and
// that deadline has passed. The I/O can time out a moment before ctx
// reports it.
func cutOff(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		deadline, ok := ctx.Deadline()
		return ok && !time.Now().Before(deadline)
	}
	return false
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

// setDeadline makes the I/O on conn give up when ctx ends.
func setDeadline(ctx context.Context, conn net.Conn) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
}

func (r *run) persistent(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Duration)
	defer cancel()

	dialer := net.Dialer{Control: sockopt.DialControl}
	eg, ctx := errgroup.WithContext(ctx)
	for range r.cfg.Connections {
		eg.Go(func() error {
			conn, err := dialer.DialContext(ctx, "tcp", r.addr)
			if err != nil {
				if cutOff(ctx, err) {
					return nil
				}
				return fmt.Errorf("dialing %q: %w", r.addr, err)
			}
			defer func() { _ = conn.Close() }()
			setDeadline(ctx, conn)

			msg := make([]byte, r.cfg.MessageBytes)
			limiter := ratelimit.New(r.cfg.Rate)
			for range r.messages() {
				if err := waitLimit(ctx, limiter); err != nil {
					return nil
				}
				fill(msg)
				if err := r.measure(ctx, func() error {
					if _, err := conn.Write(msg); err != nil {
						return fmt.Errorf("writing to connection: %w", err)
					}
					// The echo of a message larger than a segment, or than
					// the server's buffer, arrives in several reads.
					if _, err := io.ReadFull(conn, msg); err != nil {
						return fmt.Errorf("reading from connection: %w", err)
					}
					return nil
				}); err != nil {
					return err
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
			fill(*msgp)
			return r.measure(ctx, func() error {
				return r.ephemeralRequest(ctx, &dialer, *msgp)
			})
		})
	}
	return eg.Wait()
}

func (r *run) ephemeralRequest(ctx context.Context, dialer *net.Dialer, msg []byte) error {
	conn, err := dialer.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		if errors.Is(err, syscall.ETIMEDOUT) {
			r.logger.Warn("connection timeout", "addr", r.addr)
			return errTolerated
		}
		return fmt.Errorf("dialing %q: %w", r.addr, err)
	}
	defer func() { _ = conn.Close() }()
	setDeadline(ctx, conn)

	if err := sockopt.SetQuickAck(conn); err != nil {
		return fmt.Errorf("setting quick ack: %w", err)
	}

	if _, err := conn.Write(msg); err != nil {
		if errors.Is(err, syscall.EINPROGRESS) {
			r.logger.Warn("write in progress", "addr", r.addr)
			return errTolerated
		}
		return fmt.Errorf("writing to connection: %w", err)
	}
	if _, err := io.ReadFull(conn, msg); err != nil {
		if errors.Is(err, syscall.ECONNRESET) {
			r.logger.Warn("connection reset", "addr", r.addr)
			return errTolerated
		}
		return fmt.Errorf("reading from connection: %w", err)
	}
	return nil
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
			return r.measure(ctx, func() error {
				return r.udpRequest(ctx, &dialer, *msgp)
			})
		})
	}
	return eg.Wait()
}

func (r *run) udpRequest(ctx context.Context, dialer *net.Dialer, msg []byte) error {
	conn, err := dialer.DialContext(ctx, "udp4", r.addr)
	if err != nil {
		return fmt.Errorf("dialing UDP %q: %w", r.addr, err)
	}
	defer func() { _ = conn.Close() }()
	setDeadline(ctx, conn)

	if _, err := conn.Write(msg); err != nil {
		return fmt.Errorf("writing to UDP connection: %w", err)
	}
	if _, err := conn.Read(msg); err != nil {
		return fmt.Errorf("reading from UDP connection: %w", err)
	}
	return nil
}
