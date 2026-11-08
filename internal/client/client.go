// Package client implements the load generator of tcpulse: it sends
// messages to an echo server at a fixed rate and records how long each takes
// to come back.
package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
// or ctx is canceled, and an error when a connection fails.
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

// measure records how long f takes, if it succeeds.
func (r *run) measure(f func() error) error {
	start := time.Now()
	if err := f(); err != nil {
		return err
	}
	r.rec.Record(time.Since(start))
	return nil
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
				if err := r.measure(func() error {
					fill(msg)
					if _, err := conn.Write(msg); err != nil {
						return fmt.Errorf("writing to connection: %w", err)
					}
					if _, err := conn.Read(msg); err != nil {
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
			return r.measure(func() error {
				return r.ephemeralRequest(ctx, &dialer)
			})
		})
	}
	return eg.Wait()
}

func (r *run) ephemeralRequest(ctx context.Context, dialer *net.Dialer) error {
	conn, err := dialer.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		if errors.Is(err, syscall.ETIMEDOUT) {
			r.logger.Warn("connection timeout", "addr", r.addr)
			return nil
		}
		return fmt.Errorf("dialing %q: %w", r.addr, err)
	}
	defer func() { _ = conn.Close() }()
	setDeadline(ctx, conn)

	if err := sockopt.SetQuickAck(conn); err != nil {
		return fmt.Errorf("setting quick ack: %w", err)
	}

	msgp := r.bufPool.Get().(*[]byte)
	defer r.bufPool.Put(msgp)
	msg := *msgp
	fill(msg)

	if _, err := conn.Write(msg); err != nil {
		if errors.Is(err, syscall.EINPROGRESS) {
			r.logger.Warn("write in progress", "addr", r.addr)
			return nil
		}
		return fmt.Errorf("writing to connection: %w", err)
	}
	if _, err := conn.Read(msg); err != nil {
		if errors.Is(err, syscall.ECONNRESET) {
			r.logger.Warn("connection reset", "addr", r.addr)
			return nil
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
			return r.measure(func() error {
				return r.udpRequest(ctx, &dialer)
			})
		})
	}
	return eg.Wait()
}

func (r *run) udpRequest(ctx context.Context, dialer *net.Dialer) error {
	conn, err := dialer.DialContext(ctx, "udp4", r.addr)
	if err != nil {
		return fmt.Errorf("dialing UDP %q: %w", r.addr, err)
	}
	defer func() { _ = conn.Close() }()
	setDeadline(ctx, conn)

	msgp := r.bufPool.Get().(*[]byte)
	defer r.bufPool.Put(msgp)
	msg := *msgp
	fill(msg)

	if _, err := conn.Write(msg); err != nil {
		return fmt.Errorf("writing to UDP connection: %w", err)
	}
	if _, err := conn.Read(msg); err != nil {
		return fmt.Errorf("reading from UDP connection: %w", err)
	}
	return nil
}
