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

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestWriteErrors(t *testing.T) {
	p := NewPrinter(failingWriter{})
	for name, err := range map[string]error{
		"Header":    p.Header(),
		"Line":      p.Line(Result{Peer: "a:1"}),
		"Report":    p.Report([]Result{{Peer: "a:1"}}),
		"JSONLines": p.JSONLines([]Result{{Peer: "a:1"}}),
	} {
		if err == nil {
			t.Errorf("%s returned no error on a failing writer", name)
		}
	}
}
