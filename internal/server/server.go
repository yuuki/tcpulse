// Package server implements the echo server of tcpulse: it writes back
// every TCP byte stream and every UDP datagram it receives.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/yuuki/tcpulse/internal/sockopt"
)

// Protocols the server can listen on.
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
	ProtocolAll = "all"
)

const (
	tcpBufferSize = 4 * 1024
	udpBufferSize = 4 * 1024
	// acceptRetryDelay is how long the server waits after a timeout from
	// accept before trying again.
	acceptRetryDelay = time.Second
)

var tcpBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, tcpBufferSize)
		return &buf
	},
}

var udpBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, udpBufferSize)
		return &buf
	},
}

// Config configures a Server.
type Config struct {
	// Addrs are the host:port addresses to listen on.
	Addrs []string
	// Protocol is ProtocolTCP, ProtocolUDP or ProtocolAll.
	Protocol string
	// Logger receives the errors of individual connections; nil means
	// slog.Default().
	Logger *slog.Logger
}

// Server is an echo server bound to its addresses.
type Server struct {
	logger *slog.Logger
	tcp    []net.Listener
	udp    []net.PacketConn
}

// Listen binds every address of cfg, for TCP, UDP or both. The listeners are
// open when it returns; Serve closes them.
func Listen(ctx context.Context, cfg Config) (*Server, error) {
	s := &Server{logger: cfg.Logger}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	tcp := cfg.Protocol == ProtocolTCP || cfg.Protocol == ProtocolAll
	udp := cfg.Protocol == ProtocolUDP || cfg.Protocol == ProtocolAll

	if tcp {
		lc := net.ListenConfig{Control: sockopt.ListenControl}
		for _, addr := range cfg.Addrs {
			ln, err := lc.Listen(ctx, "tcp", addr)
			if err != nil {
				s.close()
				return nil, fmt.Errorf("listen %q error: %w", addr, err)
			}
			s.tcp = append(s.tcp, ln)
		}
	}
	if udp {
		var lc net.ListenConfig
		for _, addr := range cfg.Addrs {
			pc, err := lc.ListenPacket(ctx, "udp4", addr)
			if err != nil {
				s.close()
				return nil, fmt.Errorf("listen %q error: %w", addr, err)
			}
			s.udp = append(s.udp, pc)
		}
	}
	return s, nil
}

// TCPAddrs returns the addresses the TCP listeners are bound to.
func (s *Server) TCPAddrs() []net.Addr {
	addrs := make([]net.Addr, 0, len(s.tcp))
	for _, ln := range s.tcp {
		addrs = append(addrs, ln.Addr())
	}
	return addrs
}

// UDPAddrs returns the addresses the UDP sockets are bound to.
func (s *Server) UDPAddrs() []net.Addr {
	addrs := make([]net.Addr, 0, len(s.udp))
	for _, pc := range s.udp {
		addrs = append(addrs, pc.LocalAddr())
	}
	return addrs
}

func (s *Server) close() {
	for _, ln := range s.tcp {
		_ = ln.Close()
	}
	for _, pc := range s.udp {
		_ = pc.Close()
	}
}

// Serve echoes until ctx is done or a listener fails, then closes the
// listeners and the connections and returns once every handler has
// finished. It returns nil when ctx ends it.
func (s *Server) Serve(ctx context.Context) error {
	defer s.close()
	eg, ctx := errgroup.WithContext(ctx)
	stop := context.AfterFunc(ctx, s.close)
	defer stop()

	for _, ln := range s.tcp {
		eg.Go(func() error {
			if err := s.serveTCP(ctx, ln); err != nil {
				s.logger.Error("TCP server error", "error", err)
				return err
			}
			return nil
		})
	}
	for _, pc := range s.udp {
		eg.Go(func() error {
			if err := s.serveUDP(ctx, pc); err != nil {
				s.logger.Error("UDP server error", "error", err)
				return err
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	s.logger.Info("Server shutdown complete")
	return nil
}

func (s *Server) serveTCP(ctx context.Context, ln net.Listener) error {
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				s.logger.Warn("timeout error accepting TCP connection", "addr", ln.Addr(), "error", err)
				time.Sleep(acceptRetryDelay)
				continue
			}
			return fmt.Errorf("accepting TCP connection: %w", err)
		}

		if err := sockopt.SetQuickAck(conn); err != nil {
			_ = conn.Close()
			return fmt.Errorf("setting quick ack: %w", err)
		}

		handlers.Go(func() {
			// Closing the connection when the server stops ends the handler.
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			if err := echo(conn); err != nil && ctx.Err() == nil {
				s.logger.Error("connection handler error", "remote_addr", conn.RemoteAddr(), "error", err)
			}
		})
	}
}

// echo writes back what it reads from conn until the peer closes it.
func echo(conn net.Conn) error {
	defer func() { _ = conn.Close() }()

	bufp := tcpBufPool.Get().(*[]byte)
	defer tcpBufPool.Put(bufp)
	buf := *bufp

	for {
		n, err := conn.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("reading from %q: %w", conn.RemoteAddr(), err)
		}
		if _, err := conn.Write(buf[:n]); err != nil {
			return fmt.Errorf("writing to %q: %w", conn.RemoteAddr(), err)
		}
	}
}

func (s *Server) serveUDP(ctx context.Context, pc net.PacketConn) error {
	var writers sync.WaitGroup
	defer writers.Wait()
	for {
		bufp := udpBufPool.Get().(*[]byte)
		n, remote, err := pc.ReadFrom(*bufp)
		if err != nil {
			udpBufPool.Put(bufp)
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.logger.Error("UDP read error", "error", err)
			continue
		}

		writers.Go(func() {
			defer udpBufPool.Put(bufp)
			if _, err := pc.WriteTo((*bufp)[:n], remote); err != nil && ctx.Err() == nil {
				s.logger.Error("UDP write error", "remote_addr", remote, "error", err)
			}
		})
	}
}
