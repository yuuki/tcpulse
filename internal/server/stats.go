package server

import (
	"context"
	"net"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yuuki/tcpulse/internal/sockopt"
)

// StatsSupported reports whether Config.Stats works on this platform (Linux).
const StatsSupported = sockopt.SocketStatsSupported

// queueSampleInterval is how often the accept queues are sampled.
const queueSampleInterval = 10 * time.Millisecond

// ListenerStats describes a listening socket over the life of a Server
// (Config.Stats). A value the kernel could not report is -1.
type ListenerStats struct {
	// Protocol is ProtocolTCP or ProtocolUDP.
	Protocol string
	Addr     net.Addr
	// Received counts the TCP connections accepted, or the UDP datagrams
	// received.
	Received int64
	// Drops counts what the kernel dropped for the socket: for TCP, the
	// SYNs and ACKs of handshakes the listener could not take because its
	// SYN or accept queue was full; for UDP, the datagrams that did not fit
	// in the receive buffer.
	Drops int64
	// QueueMax is the most connections seen waiting in the accept queue
	// (sampled every queueSampleInterval), and QueueLen the length of the
	// queue. Both are -1 for UDP.
	QueueMax int64
	QueueLen int64
}

// socketStats accumulates the ListenerStats of a socket. The sampler and the
// snapshot at close update the queue concurrently.
type socketStats struct {
	received atomic.Int64
	queueMax atomic.Int64
	queueLen atomic.Int64
	// Set by snapshot, before the socket is closed.
	drops int64
}

func newSocketStats() *socketStats {
	st := &socketStats{drops: -1}
	st.queueMax.Store(-1)
	st.queueLen.Store(-1)
	return st
}

// sampleQueue records the accept queue of ln if it is the longest seen.
func (st *socketStats) sampleQueue(ln syscall.Conn) {
	waiting, length, err := sockopt.AcceptQueue(ln)
	if err != nil {
		return
	}
	st.queueLen.Store(int64(length))
	for {
		seen := st.queueMax.Load()
		if int64(waiting) <= seen || st.queueMax.CompareAndSwap(seen, int64(waiting)) {
			return
		}
	}
}

// sampleQueues samples the accept queues of the TCP listeners until ctx is
// done.
func (s *Server) sampleQueues(ctx context.Context) {
	ticker := time.NewTicker(queueSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i, ln := range s.tcp {
				if sc, ok := ln.(syscall.Conn); ok {
					s.tcpStats[i].sampleQueue(sc)
				}
			}
		}
	}
}

// snapshot reads what the kernel counted for the sockets. It runs before
// they are closed, which loses the counts.
func (s *Server) snapshot() {
	for i, ln := range s.tcp {
		if sc, ok := ln.(syscall.Conn); ok {
			st := s.tcpStats[i]
			st.sampleQueue(sc)
			if drops, err := sockopt.SocketDrops(sc); err == nil {
				st.drops = int64(drops)
			}
		}
	}
	for i, pc := range s.udp {
		if sc, ok := pc.(syscall.Conn); ok {
			if drops, err := sockopt.SocketDrops(sc); err == nil {
				s.udpStats[i].drops = int64(drops)
			}
		}
	}
}

// Stats returns the statistics of the listening sockets, TCP first, once
// Serve has returned, and nil without Config.Stats.
func (s *Server) Stats() []ListenerStats {
	if !s.stats {
		return nil
	}
	var stats []ListenerStats
	for i, ln := range s.tcp {
		st := s.tcpStats[i]
		stats = append(stats, ListenerStats{
			Protocol: ProtocolTCP,
			Addr:     ln.Addr(),
			Received: st.received.Load(),
			Drops:    st.drops,
			QueueMax: st.queueMax.Load(),
			QueueLen: st.queueLen.Load(),
		})
	}
	for i, pc := range s.udp {
		st := s.udpStats[i]
		stats = append(stats, ListenerStats{
			Protocol: ProtocolUDP,
			Addr:     pc.LocalAddr(),
			Received: st.received.Load(),
			Drops:    st.drops,
			QueueMax: -1,
			QueueLen: -1,
		})
	}
	return stats
}
