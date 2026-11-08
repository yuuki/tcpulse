// Package report prints the results of a run, as aligned text columns or as
// JSON Lines.
package report

import (
	"encoding/json"
	"fmt"
	"io"
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
	return p.connReport(results)
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
			Peer:        r.Peer,
			Count:       s.Count,
			LatencyMax:  s.Max.Microseconds(),
			LatencyMin:  s.Min.Microseconds(),
			LatencyMean: s.Mean.Microseconds(),
			Latency90p:  s.P90.Microseconds(),
			Latency95p:  s.P95.Microseconds(),
			Latency99p:  s.P99.Microseconds(),
			Rate:        s.Rate,
			Timestamp:   timestamp,
			TCPInfo:     jsonLinesTCPInfo(r.Conns),
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
