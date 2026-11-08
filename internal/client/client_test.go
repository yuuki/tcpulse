package client

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/yuuki/tcpulse/internal/server"
	"github.com/yuuki/tcpulse/internal/stats"
)

var discard = slog.New(slog.DiscardHandler)

// echoServer serves protocol on a free loopback port until the test ends
// and returns its address.
func echoServer(t *testing.T, protocol string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := server.Listen(ctx, server.Config{Addrs: []string{"127.0.0.1:0"}, Protocol: protocol, Logger: discard})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	if protocol == server.ProtocolUDP {
		return srv.UDPAddrs()[0].String()
	}
	return srv.TCPAddrs()[0].String()
}

// closedPort returns a loopback address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func config(protocol, flavor string) Config {
	return Config{
		Protocol:     protocol,
		Flavor:       flavor,
		Connections:  2,
		Rate:         10,
		Duration:     time.Second,
		MessageBytes: 64,
		Logger:       discard,
	}
}

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protocol  string
		flavor    string
		wantCount int64
	}{
		// rate × duration × connections
		{"tcp persistent", ProtocolTCP, FlavorPersistent, 20},
		// rate × duration
		{"tcp ephemeral", ProtocolTCP, FlavorEphemeral, 10},
		{"udp", ProtocolUDP, FlavorPersistent, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := echoServer(t, tc.protocol)
			rec := stats.NewRecorder()
			if err := Run(t.Context(), config(tc.protocol, tc.flavor), addr, rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			total := rec.Total()
			if total.Count != tc.wantCount {
				t.Fatalf("%d round trips, want %d", total.Count, tc.wantCount)
			}
			if total.Min <= 0 || total.Max < total.Min || total.P99 > total.Max {
				t.Fatalf("inconsistent latencies: %+v", total)
			}
		})
	}
}

func TestRunStopsWhenCanceled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol string
		flavor   string
	}{
		{"tcp persistent", ProtocolTCP, FlavorPersistent},
		{"tcp ephemeral", ProtocolTCP, FlavorEphemeral},
		{"udp", ProtocolUDP, FlavorPersistent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := echoServer(t, tc.protocol)
			cfg := config(tc.protocol, tc.flavor)
			cfg.Duration = time.Minute
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()

			start := time.Now()
			if err := Run(ctx, cfg, addr, stats.NewRecorder()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("Run returned %v after its context was canceled", elapsed)
			}
		})
	}
}

func TestRunConnectionRefused(t *testing.T) {
	for _, flavor := range []string{FlavorPersistent, FlavorEphemeral} {
		t.Run(flavor, func(t *testing.T) {
			t.Parallel()
			if err := Run(t.Context(), config(ProtocolTCP, flavor), closedPort(t), stats.NewRecorder()); err == nil {
				t.Fatal("Run succeeded against a closed port")
			}
		})
	}
}

func TestRunInvalidConfig(t *testing.T) {
	if err := Run(t.Context(), config("sctp", FlavorPersistent), "127.0.0.1:1", stats.NewRecorder()); err == nil {
		t.Fatal("Run accepted an unknown protocol")
	}
	if err := Run(t.Context(), config(ProtocolTCP, "bursty"), "127.0.0.1:1", stats.NewRecorder()); err == nil {
		t.Fatal("Run accepted an unknown flavor")
	}
}

// slowEchoServer echoes every message it receives on TCP delay after it
// arrives, until the test ends.
func slowEchoServer(t *testing.T, size int, delay time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, size)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
					time.Sleep(delay)
					if _, err := conn.Write(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRunEndsDuringARequest(t *testing.T) {
	for _, flavor := range []string{FlavorPersistent, FlavorEphemeral} {
		t.Run(flavor, func(t *testing.T) {
			t.Parallel()
			cfg := config(ProtocolTCP, flavor)
			cfg.Connections = 1
			cfg.Rate = 4
			cfg.MessageBytes = 16
			// Messages at 0, 250, 500 and 750ms (persistent: as soon as the
			// previous echo is back); the last echo would arrive after the
			// end of the run at 1s.
			addr := slowEchoServer(t, cfg.MessageBytes, 300*time.Millisecond)
			rec := stats.NewRecorder()
			if err := Run(t.Context(), cfg, addr, rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := rec.Total().Count; got != 3 {
				t.Fatalf("%d round trips, want 3: the last request was cut off", got)
			}
		})
	}
}

func TestRunLostDatagrams(t *testing.T) {
	// A UDP socket that never answers.
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	cfg := config(ProtocolUDP, FlavorPersistent)
	rec := stats.NewRecorder()
	if err := Run(t.Context(), cfg, pc.LocalAddr().String(), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rec.Total().Count; got != 0 {
		t.Fatalf("%d round trips without an answer", got)
	}
}
