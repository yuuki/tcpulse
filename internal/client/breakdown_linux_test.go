//go:build linux

package client

import (
	"testing"
	"time"

	"github.com/yuuki/tcpulse/internal/server"
	"github.com/yuuki/tcpulse/internal/stats"
)

func TestRunBreakdown(t *testing.T) {
	for _, size := range []int{64, 3 * concurrentEchoBytes} {
		cfg := config(ProtocolTCP, FlavorPersistent)
		cfg.MessageBytes = size
		cfg.Breakdown = true
		rec := stats.NewRecorder()
		if err := Run(t.Context(), cfg, echoServer(t, server.ProtocolTCP), rec); err != nil {
			t.Fatalf("Run: %v", err)
		}
		total, b := rec.Total(time.Second), rec.Breakdown()
		// Every round trip counts once. The first round trip of a
		// connection that TCP Fast Open deferred the handshake to has no
		// timestamps: the stream offsets are not known before it.
		if total.Count != 20 || b.RoundTrips+b.Missing+b.Negative != total.Count ||
			b.Missing > int64(cfg.Connections) || b.Negative != 0 {
			t.Fatalf("%d bytes: %d round trips, breakdown %+v", size, total.Count, b)
		}
		t.Logf("%d bytes: %d round trips, max %v, breakdown %+v", size, total.Count, total.Max, b)
		// The parts of a round trip add up to it.
		if b.Send.P50 <= 0 || b.Network.P50 <= 0 || b.Receive.P50 < 0 ||
			b.Send.P50+b.Network.P50+b.Receive.P50 > total.Max+total.Max/64 {
			t.Fatalf("%d bytes: parts %+v, round trips %+v", size, b, total)
		}
	}
}
