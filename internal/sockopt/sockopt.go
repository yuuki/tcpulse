// Package sockopt sets the socket options tcpulse tunes on Linux: TCP Fast
// Open, SO_REUSEPORT and TCP_QUICKACK, and reads what the kernel reports of a
// TCP connection (TCP_INFO). On other platforms its functions do nothing, or
// report that they are not supported.
package sockopt

import "time"

// TCPInfo is what the kernel reports of a TCP connection.
type TCPInfo struct {
	// RetransSegs is the number of segments the connection retransmitted,
	// SYN included (tcpi_total_retrans).
	RetransSegs uint32
	// SegsOut is the number of segments the connection sent,
	// retransmissions included (tcpi_segs_out). It is 0 before Linux 4.2,
	// whose TCP_INFO is shorter.
	SegsOut uint32
	// RTT is the kernel's smoothed round-trip time (tcpi_rtt), or 0 when it
	// has no RTT sample yet (for instance a SYN sent with Fast Open and
	// never answered).
	RTT time.Duration
	// FastOpen reports that the data sent in the SYN was acknowledged (TCP
	// Fast Open, TCPI_OPT_SYN_DATA).
	FastOpen bool
}
