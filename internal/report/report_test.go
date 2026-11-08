package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yuuki/tcpulse/internal/stats"
)

var sample = stats.Summary{
	Count: 14799,
	Max:   13736 * time.Microsecond,
	Min:   223 * time.Microsecond,
	Mean:  530*time.Microsecond + 999*time.Nanosecond,
	P90:   501 * time.Microsecond,
	P95:   953 * time.Microsecond,
	P99:   5989 * time.Microsecond,
	Rate:  996,
}

// The text columns are a public format: these lines are those of tcpulse
// 0.8.
const (
	wantHeader = "PEER                 CNT        LAT_MAX(µs)     LAT_MIN(µs)     LAT_MEAN(µs)    LAT_90p(µs)     LAT_95p(µs)     LAT_99p(µs)     RATE(/s)  \n"
	wantLine   = "10.0.150.2:9200      14799      13736           223             530             501             953             5989            996.00    \n"
)

func TestHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf).Header(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != wantHeader {
		t.Fatalf("header:\n got %q\nwant %q", buf.String(), wantHeader)
	}
}

func TestLine(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf).Line(Result{Peer: "10.0.150.2:9200", Summary: sample}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != wantLine {
		t.Fatalf("line:\n got %q\nwant %q", buf.String(), wantLine)
	}
}

func TestReport(t *testing.T) {
	var buf bytes.Buffer
	err := NewPrinter(&buf).Report([]Result{
		{Peer: "10.0.150.2:9200", Summary: sample},
		{Peer: MergedPeer(2), Summary: stats.Summary{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	if lines[0] != ReportSeparator {
		t.Errorf("first line = %q, want the separator", lines[0])
	}
	if lines[1]+"\n" != wantLine {
		t.Errorf("second line = %q, want %q", lines[1], wantLine)
	}
	if !strings.HasPrefix(lines[2], "merged(2 hosts)      0 ") {
		t.Errorf("third line = %q, want the merged peer with no round trip", lines[2])
	}
}

func TestJSONLines(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	p.now = func() time.Time { return time.Date(2025, 1, 7, 19, 30, 0, 0, time.FixedZone("JST", 9*3600)) }
	if err := p.JSONLines([]Result{
		{Peer: "10.0.150.2:9200", Summary: sample},
		{Peer: "10.0.150.2:9300", Summary: stats.Summary{Rate: 996.47}},
	}); err != nil {
		t.Fatal(err)
	}

	// The field names and their order are a public format.
	want := `{"peer":"10.0.150.2:9200","count":14799,"latency_max_us":13736,"latency_min_us":223,"latency_mean_us":530,"latency_90p_us":501,"latency_95p_us":953,"latency_99p_us":5989,"rate_per_sec":996,"timestamp":"2025-01-07T10:30:00Z"}
{"peer":"10.0.150.2:9300","count":0,"latency_max_us":0,"latency_min_us":0,"latency_mean_us":0,"latency_90p_us":0,"latency_95p_us":0,"latency_99p_us":0,"rate_per_sec":996.47,"timestamp":"2025-01-07T10:30:00Z"}
`
	if buf.String() != want {
		t.Fatalf("JSON Lines:\n got %s\nwant %s", buf.String(), want)
	}

	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var r JSONLinesResult
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("line %q is not a JSON object: %v", sc.Text(), err)
		}
	}
}

var sampleConns = stats.ConnSummary{
	Conns:         14799,
	RetransConns:  12,
	RetransSegs:   15,
	SegsOut:       44410,
	RTTMean:       187*time.Microsecond + 999*time.Nanosecond,
	RTTP99:        412 * time.Microsecond,
	RTTMax:        1003 * time.Microsecond,
	FastOpenConns: 14798,
}

func TestReportTCPInfo(t *testing.T) {
	var buf bytes.Buffer
	err := NewPrinter(&buf).Report([]Result{
		{Peer: "10.0.150.2:9200", Summary: sample, Conns: &sampleConns},
		{Peer: "10.0.150.2:9300", Conns: &stats.ConnSummary{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The rows of the run keep their columns; the TCP statistics follow
	// with their own header.
	want := ReportSeparator + "\n" +
		wantLine +
		"10.0.150.2:9300      0          0               0               0               0               0               0               0.00      \n" +
		ConnSeparator + "\n" +
		"PEER                 CONNS      RETRANS_CONNS   RETRANS_SEGS    SEGS_OUT        SRTT_MEAN(µs)   SRTT_99p(µs)    SRTT_MAX(µs)    TFO_CONNS \n" +
		"10.0.150.2:9200      14799      12              15              44410           187             412             1003            14798     \n" +
		"10.0.150.2:9300      0          0               0               0               0               0               0               0         \n"
	if buf.String() != want {
		t.Fatalf("report:\n got %q\nwant %q", buf.String(), want)
	}
}

func TestJSONLinesTCPInfo(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	p.now = func() time.Time { return time.Date(2025, 1, 7, 10, 30, 0, 0, time.UTC) }
	if err := p.JSONLines([]Result{{Peer: "10.0.150.2:9200", Summary: sample, Conns: &sampleConns}}); err != nil {
		t.Fatal(err)
	}
	// The fields of the run keep their names and order; tcp_info follows.
	want := `{"peer":"10.0.150.2:9200","count":14799,"latency_max_us":13736,"latency_min_us":223,"latency_mean_us":530,"latency_90p_us":501,"latency_95p_us":953,"latency_99p_us":5989,"rate_per_sec":996,"timestamp":"2025-01-07T10:30:00Z",` +
		`"tcp_info":{"connections":14799,"retrans_connections":12,"retrans_segments":15,"segments_out":44410,"srtt_mean_us":187,"srtt_99p_us":412,"srtt_max_us":1003,"fastopen_connections":14798}}` + "\n"
	if buf.String() != want {
		t.Fatalf("JSON Lines:\n got %s\nwant %s", buf.String(), want)
	}
}

var sampleEvents = stats.EventSummary{
	RoundTrips:        41,
	RetransRoundTrips: stats.RetransCounts{SYN: 30, FastOpen: 2, RTO: 6, Other: 4},
	WithEvents:        stats.LatencySummary{Count: 41, P99: 1023456 * time.Microsecond, Max: 3045 * time.Millisecond},
	WithoutEvents:     stats.LatencySummary{Count: 14758, P99: 812 * time.Microsecond, Max: 4108 * time.Microsecond},
	ResetsReceived:    3,
	Unmatched:         stats.RetransCounts{SYN: 7, RTO: 2},
	Untracked:         5,
}

func TestReportKernelEvents(t *testing.T) {
	var buf bytes.Buffer
	err := NewPrinter(&buf).Report([]Result{
		{Peer: "10.0.150.2:9200", Summary: sample, Conns: &sampleConns, Events: &sampleEvents},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The kernel events follow the TCP statistics, with their own header.
	_, events, ok := strings.Cut(buf.String(), EventSeparator+"\n")
	if !ok || !strings.Contains(buf.String(), ConnSeparator) {
		t.Fatalf("report:\n%s", buf.String())
	}
	want := "PEER                 EVENT_RTS  SYN_RTX         TFO_RTX         RTO_RTX         OTHER_RTX       EVENT_99p(µs)   CLEAN_99p(µs)   RST_RECV        UNMATCHED       UNTRACKED \n" +
		"10.0.150.2:9200      41         30              2               6               4               1023456         812             3               9               5         \n"
	if events != want {
		t.Fatalf("kernel events:\n got %q\nwant %q", events, want)
	}
}

func TestJSONLinesKernelEvents(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	p.now = func() time.Time { return time.Date(2025, 1, 7, 10, 30, 0, 0, time.UTC) }
	if err := p.JSONLines([]Result{{Peer: "10.0.150.2:9200", Summary: sample, Events: &sampleEvents}}); err != nil {
		t.Fatal(err)
	}
	// The fields of the run keep their names and order; kernel_events
	// follows (and tcp_info would come before it).
	want := `{"peer":"10.0.150.2:9200","count":14799,"latency_max_us":13736,"latency_min_us":223,"latency_mean_us":530,"latency_90p_us":501,"latency_95p_us":953,"latency_99p_us":5989,"rate_per_sec":996,"timestamp":"2025-01-07T10:30:00Z",` +
		`"kernel_events":{"round_trips_with_events":41,"round_trips_without_events":14758,"syn_retrans_round_trips":30,"fastopen_retrans_round_trips":2,"rto_retrans_round_trips":6,"other_retrans_round_trips":4,` +
		`"latency_99p_with_events_us":1023456,"latency_max_with_events_us":3045000,"latency_99p_without_events_us":812,"latency_max_without_events_us":4108,` +
		`"resets_received":3,"unmatched_syn_retrans":7,"unmatched_fastopen_retrans":0,"unmatched_rto_retrans":2,"unmatched_other_retrans":0,"untracked_round_trips":5}}` + "\n"
	if buf.String() != want {
		t.Fatalf("JSON Lines:\n got %s\nwant %s", buf.String(), want)
	}
}

var sampleBreakdown = stats.BreakdownSummary{
	RoundTrips: 14787, Missing: 7, OutOfOrder: 3, Negative: 2,
	Send:    stats.PartSummary{P50: 21 * time.Microsecond, P99: 81 * time.Microsecond},
	Network: stats.PartSummary{P50: 455 * time.Microsecond, P99: 5480 * time.Microsecond},
	Receive: stats.PartSummary{P50: 18 * time.Microsecond, P99: 4520 * time.Microsecond},
}

func TestReportBreakdown(t *testing.T) {
	var buf bytes.Buffer
	err := NewPrinter(&buf).Report([]Result{
		{Peer: "10.0.150.2:9200", Summary: sample, Events: &sampleEvents, Breakdown: &sampleBreakdown},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The breakdown follows the kernel events, with its own header.
	_, breakdown, ok := strings.Cut(buf.String(), BreakdownSeparator+"\n")
	if !ok || !strings.Contains(buf.String(), EventSeparator) {
		t.Fatalf("report:\n%s", buf.String())
	}
	want := "PEER                 SPLIT_RTS  MISSING    OUT_OF_ORDER NEGATIVE   SEND_50p(µs)    SEND_99p(µs)    NET_50p(µs)     NET_99p(µs)     RECV_50p(µs)    RECV_99p(µs)   \n" +
		"10.0.150.2:9200      14787      7          3            2          21              81              455             5480            18              4520           \n"
	if breakdown != want {
		t.Fatalf("breakdown:\n got %q\nwant %q", breakdown, want)
	}
}

func TestJSONLinesBreakdown(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	if err := p.JSONLines([]Result{{Peer: "10.0.150.2:9200", Summary: sample, Events: &sampleEvents, Breakdown: &sampleBreakdown}}); err != nil {
		t.Fatal(err)
	}
	// latency_breakdown ends the object, after kernel_events.
	want := `"untracked_round_trips":5},"latency_breakdown":{"round_trips":14787,"missing_timestamps":7,"out_of_order":3,"negative_parts":2,` +
		`"send_50p_us":21,"send_99p_us":81,"network_50p_us":455,"network_99p_us":5480,"receive_50p_us":18,"receive_99p_us":4520}}` + "\n"
	if !strings.HasSuffix(buf.String(), want) {
		t.Fatalf("JSON Lines: %s\nwant it to end with %s", buf.String(), want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestWriteErrors(t *testing.T) {
	p := NewPrinter(failingWriter{})
	for name, err := range map[string]error{
		"Header":          p.Header(),
		"Line":            p.Line(Result{Peer: "a:1"}),
		"Report":          p.Report([]Result{{Peer: "a:1"}}),
		"connReport":      NewPrinter(failingWriter{}).connReport([]Result{{Peer: "a:1", Conns: &stats.ConnSummary{}}}),
		"eventReport":     NewPrinter(failingWriter{}).eventReport([]Result{{Peer: "a:1", Events: &stats.EventSummary{}}}),
		"breakdownReport": NewPrinter(failingWriter{}).breakdownReport([]Result{{Peer: "a:1", Breakdown: &stats.BreakdownSummary{}}}),
		"JSONLines":       p.JSONLines([]Result{{Peer: "a:1"}}),
	} {
		if err == nil {
			t.Errorf("%s returned no error on a failing writer", name)
		}
	}
}
