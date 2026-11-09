// Package report prints the results of a run, as aligned text columns or as
// JSON Lines.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/yuuki/tcpulse/internal/stats"
)

// Result is the summary of the round trips to a peer, or to every peer when
// the results are merged.
type Result struct {
	Peer    string
	Summary stats.Summary
	// Conns describes the TCP connections of the run when it read their
	// TCP_INFO (--tcp-info), and is nil otherwise.
	Conns *stats.ConnSummary
	// Events describes the kernel events of the round trips when the run
	// counted them (--kernel-events), and is nil otherwise.
	Events *stats.EventSummary
}

// MergedPeer is the peer name of the merged result of n peers.
func MergedPeer(n int) string {
	return fmt.Sprintf("merged(%d hosts)", n)
}

// Printer writes results to an io.Writer. Its methods may be called
// concurrently: each line is written whole.
type Printer struct {
	now func() time.Time

	mu sync.Mutex
	w  io.Writer
}

// NewPrinter returns a Printer that writes to w.
func NewPrinter(w io.Writer) *Printer {
	return &Printer{w: w, now: time.Now}
}

const (
	headerFormat = "%-20s %-10s %-15s %-15s %-15s %-15s %-15s %-15s %-10s\n"
	lineFormat   = "%-20s %-10d %-15d %-15d %-15d %-15d %-15d %-15d %-10.2f\n"
	// ReportSeparator precedes the results of the whole run in text output.
	ReportSeparator = "--- A result during total execution time ---"

	connLineFormat = "%-20s %-10d %-15d %-15d %-15d %-15d %-15d %-15d %-10d\n"
	// ConnSeparator precedes the TCP statistics of the connections in text
	// output.
	ConnSeparator = "--- TCP statistics of the connections (TCP_INFO) ---"

	eventHeaderFormat = "%-20s %-10s %-15s %-15s %-15s %-15s %-15s %-15s %-15s %-15s %-10s\n"
	eventLineFormat   = "%-20s %-10d %-15d %-15d %-15d %-15d %-15d %-15d %-15d %-15d %-10d\n"
	// EventSeparator precedes the kernel events of the round trips in text
	// output.
	EventSeparator = "--- Kernel events of the round trips (eBPF) ---"
)

// Header writes the column header of the text output.
func (p *Printer) Header() error {
	return p.printf(headerFormat,
		"PEER", "CNT", "LAT_MAX(µs)", "LAT_MIN(µs)", "LAT_MEAN(µs)",
		"LAT_90p(µs)", "LAT_95p(µs)", "LAT_99p(µs)", "RATE(/s)")
}

// Line writes one row of the text output.
func (p *Printer) Line(r Result) error {
	s := r.Summary
	return p.printf(lineFormat,
		r.Peer,
		s.Count,
		s.Max.Microseconds(),
		s.Min.Microseconds(),
		s.Mean.Microseconds(),
		s.P90.Microseconds(),
		s.P95.Microseconds(),
		s.P99.Microseconds(),
		s.Rate,
	)
}

// Report writes the results of the whole run as text: a separator line,
// then one row per result. The results that describe their TCP connections
// follow in a section of their own, with its own header, so that the rows
// above keep the columns of the header that starts the output.
func (p *Printer) Report(results []Result) error {
	if err := p.printf("%s\n", ReportSeparator); err != nil {
		return err
	}
	for _, r := range results {
		if err := p.Line(r); err != nil {
			return err
		}
	}
	if err := p.connReport(results); err != nil {
		return err
	}
	return p.eventReport(results)
}

func (p *Printer) eventReport(results []Result) error {
	header := false
	for _, r := range results {
		if r.Events == nil {
			continue
		}
		if !header {
			if err := p.printf("%s\n"+eventHeaderFormat, EventSeparator,
				"PEER", "EVENT_RTS", "SYN_RTX", "TFO_RTX", "RTO_RTX", "OTHER_RTX",
				"EVENT_99p(µs)", "CLEAN_99p(µs)", "RST_RECV", "UNMATCHED", "UNTRACKED"); err != nil {
				return err
			}
			header = true
		}
		e := r.Events
		if err := p.printf(eventLineFormat,
			r.Peer,
			e.RoundTrips,
			e.RetransRoundTrips.SYN,
			e.RetransRoundTrips.FastOpen,
			e.RetransRoundTrips.RTO,
			e.RetransRoundTrips.Other,
			e.WithEvents.P99.Microseconds(),
			e.WithoutEvents.P99.Microseconds(),
			e.ResetsReceived,
			e.Unmatched.Total(),
			e.Untracked,
		); err != nil {
			return err
		}
	}
	return nil
}

func (p *Printer) connReport(results []Result) error {
	header := false
	for _, r := range results {
		if r.Conns == nil {
			continue
		}
		if !header {
			if err := p.printf("%s\n"+headerFormat, ConnSeparator,
				"PEER", "CONNS", "RETRANS_CONNS", "RETRANS_SEGS", "SEGS_OUT",
				"SRTT_MEAN(µs)", "SRTT_99p(µs)", "SRTT_MAX(µs)", "TFO_CONNS"); err != nil {
				return err
			}
			header = true
		}
		c := r.Conns
		if err := p.printf(connLineFormat,
			r.Peer,
			c.Conns,
			c.RetransConns,
			c.RetransSegs,
			c.SegsOut,
			c.RTTMean.Microseconds(),
			c.RTTP99.Microseconds(),
			c.RTTMax.Microseconds(),
			c.FastOpenConns,
		); err != nil {
			return err
		}
	}
	return nil
}

// JSONLinesResult is the JSON object of a result in JSON Lines output.
type JSONLinesResult struct {
	Peer        string  `json:"peer"`
	Count       int64   `json:"count"`
	LatencyMax  int64   `json:"latency_max_us"`
	LatencyMin  int64   `json:"latency_min_us"`
	LatencyMean int64   `json:"latency_mean_us"`
	Latency90p  int64   `json:"latency_90p_us"`
	Latency95p  int64   `json:"latency_95p_us"`
	Latency99p  int64   `json:"latency_99p_us"`
	Rate        float64 `json:"rate_per_sec"`
	Timestamp   string  `json:"timestamp"`
	// TCPInfo is only there when the run read the TCP_INFO of its
	// connections (--tcp-info).
	TCPInfo *JSONLinesTCPInfo `json:"tcp_info,omitempty"`
	// KernelEvents is only there when the run counted the kernel events of
	// its round trips (--kernel-events).
	KernelEvents *JSONLinesKernelEvents `json:"kernel_events,omitempty"`
}

// JSONLinesKernelEvents is the JSON object of the kernel events of the round
// trips of a result.
type JSONLinesKernelEvents struct {
	RoundTripsWithEvents      int64 `json:"round_trips_with_events"`
	RoundTripsWithoutEvents   int64 `json:"round_trips_without_events"`
	SYNRetransRoundTrips      int64 `json:"syn_retrans_round_trips"`
	FastOpenRetransRoundTrips int64 `json:"fastopen_retrans_round_trips"`
	RTORetransRoundTrips      int64 `json:"rto_retrans_round_trips"`
	OtherRetransRoundTrips    int64 `json:"other_retrans_round_trips"`
	Latency99pWithEvents      int64 `json:"latency_99p_with_events_us"`
	LatencyMaxWithEvents      int64 `json:"latency_max_with_events_us"`
	Latency99pWithoutEvents   int64 `json:"latency_99p_without_events_us"`
	LatencyMaxWithoutEvents   int64 `json:"latency_max_without_events_us"`
	ResetsReceived            int64 `json:"resets_received"`
	UnmatchedSYNRetrans       int64 `json:"unmatched_syn_retrans"`
	UnmatchedFastOpenRetrans  int64 `json:"unmatched_fastopen_retrans"`
	UnmatchedRTORetrans       int64 `json:"unmatched_rto_retrans"`
	UnmatchedOtherRetrans     int64 `json:"unmatched_other_retrans"`
	UntrackedRoundTrips       int64 `json:"untracked_round_trips"`
}

func jsonLinesKernelEvents(e *stats.EventSummary) *JSONLinesKernelEvents {
	if e == nil {
		return nil
	}
	return &JSONLinesKernelEvents{
		RoundTripsWithEvents:      e.RoundTrips,
		RoundTripsWithoutEvents:   e.WithoutEvents.Count,
		SYNRetransRoundTrips:      e.RetransRoundTrips.SYN,
		FastOpenRetransRoundTrips: e.RetransRoundTrips.FastOpen,
		RTORetransRoundTrips:      e.RetransRoundTrips.RTO,
		OtherRetransRoundTrips:    e.RetransRoundTrips.Other,
		Latency99pWithEvents:      e.WithEvents.P99.Microseconds(),
		LatencyMaxWithEvents:      e.WithEvents.Max.Microseconds(),
		Latency99pWithoutEvents:   e.WithoutEvents.P99.Microseconds(),
		LatencyMaxWithoutEvents:   e.WithoutEvents.Max.Microseconds(),
		ResetsReceived:            e.ResetsReceived,
		UnmatchedSYNRetrans:       e.Unmatched.SYN,
		UnmatchedFastOpenRetrans:  e.Unmatched.FastOpen,
		UnmatchedRTORetrans:       e.Unmatched.RTO,
		UnmatchedOtherRetrans:     e.Unmatched.Other,
		UntrackedRoundTrips:       e.Untracked,
	}
}

// JSONLinesTCPInfo is the JSON object of the TCP statistics of the
// connections of a result.
type JSONLinesTCPInfo struct {
	Connections         int64 `json:"connections"`
	RetransConnections  int64 `json:"retrans_connections"`
	RetransSegments     int64 `json:"retrans_segments"`
	SegmentsOut         int64 `json:"segments_out"`
	SRTTMean            int64 `json:"srtt_mean_us"`
	SRTT99p             int64 `json:"srtt_99p_us"`
	SRTTMax             int64 `json:"srtt_max_us"`
	FastOpenConnections int64 `json:"fastopen_connections"`
}

func jsonLinesTCPInfo(c *stats.ConnSummary) *JSONLinesTCPInfo {
	if c == nil {
		return nil
	}
	return &JSONLinesTCPInfo{
		Connections:         c.Conns,
		RetransConnections:  c.RetransConns,
		RetransSegments:     c.RetransSegs,
		SegmentsOut:         c.SegsOut,
		SRTTMean:            c.RTTMean.Microseconds(),
		SRTT99p:             c.RTTP99.Microseconds(),
		SRTTMax:             c.RTTMax.Microseconds(),
		FastOpenConnections: c.FastOpenConns,
	}
}

// JSONLines writes the results of the whole run as JSON Lines, one object
// per result, all with the same timestamp.
func (p *Printer) JSONLines(results []Result) error {
	timestamp := p.now().UTC().Format(time.RFC3339)
	p.mu.Lock()
	defer p.mu.Unlock()
	enc := json.NewEncoder(p.w)
	for _, r := range results {
		s := r.Summary
		if err := enc.Encode(JSONLinesResult{
			Peer:         r.Peer,
			Count:        s.Count,
			LatencyMax:   s.Max.Microseconds(),
			LatencyMin:   s.Min.Microseconds(),
			LatencyMean:  s.Mean.Microseconds(),
			Latency90p:   s.P90.Microseconds(),
			Latency95p:   s.P95.Microseconds(),
			Latency99p:   s.P99.Microseconds(),
			Rate:         s.Rate,
			Timestamp:    timestamp,
			TCPInfo:      jsonLinesTCPInfo(r.Conns),
			KernelEvents: jsonLinesKernelEvents(r.Events),
		}); err != nil {
			return fmt.Errorf("writing JSON Lines: %w", err)
		}
	}
	return nil
}

func (p *Printer) printf(format string, args ...any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := fmt.Fprintf(p.w, format, args...); err != nil {
		return fmt.Errorf("writing results: %w", err)
	}
	return nil
}

// Listener is the statistics of a listening socket of the server
// (--listener-stats). A value the kernel could not report, or that does not
// apply (the accept queue of a UDP socket), is negative and printed as "-".
type Listener struct {
	Addr     string
	Protocol string
	// Received counts the TCP connections accepted, or the UDP datagrams
	// received.
	Received int64
	// Drops counts what the kernel dropped for the socket.
	Drops int64
	// QueueMax is the most connections seen waiting in the accept queue,
	// and QueueLen the length of the queue.
	QueueMax int64
	QueueLen int64
}

// ListenerSeparator precedes the statistics of the listening sockets that
// the server prints when it stops.
const ListenerSeparator = "--- Statistics of the listening sockets ---"

const listenerFormat = "%-20s %-10s %-15s %-15s %-15s %-10s\n"

// Listeners writes the statistics of the listening sockets: a separator, a
// header, and a line per socket.
func (p *Printer) Listeners(rows []Listener) error {
	if err := p.printf("%s\n"+listenerFormat, ListenerSeparator,
		"LISTENER", "PROTO", "RECEIVED", "DROPS", "QUEUE_MAX", "QUEUE_LEN"); err != nil {
		return err
	}
	for _, r := range rows {
		if err := p.printf(listenerFormat, r.Addr, r.Protocol,
			count(r.Received), count(r.Drops), count(r.QueueMax), count(r.QueueLen)); err != nil {
			return err
		}
	}
	return nil
}

// count formats a count, or "-" for an unknown one.
func count(n int64) string {
	if n < 0 {
		return "-"
	}
	return strconv.FormatInt(n, 10)
}
