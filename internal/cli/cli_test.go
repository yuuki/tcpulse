package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuuki/tcpulse/internal/client"
	"github.com/yuuki/tcpulse/internal/kevents"
	"github.com/yuuki/tcpulse/internal/report"
	"github.com/yuuki/tcpulse/internal/server"
)

func noEnvironment(string) (string, bool) { return "", false }

func environment(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

type result struct {
	code           int
	stdout, stderr string
}

func runMain(ctx context.Context, lookupEnv func(string) (string, bool), args ...string) result {
	var stdout, stderr bytes.Buffer
	code := Main(ctx, Env{
		Program:   "tcpulse",
		Args:      args,
		Stdout:    &stdout,
		Stderr:    &stderr,
		LookupEnv: lookupEnv,
		Version:   "1.2.3",
		Revision:  "abcdef0",
	})
	return result{code, stdout.String(), stderr.String()}
}

func TestParseDefaults(t *testing.T) {
	o, _, err := parse(nil, noEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	want := options{
		proto:         "tcp",
		interval:      5 * time.Second,
		flavor:        "persistent",
		connections:   10,
		rate:          100,
		duration:      10 * time.Second,
		messageBytes:  64,
		pprofAddr:     "localhost:6060",
		serveProtocol: "all",
		args:          []string{},
	}
	if o.proto != want.proto || o.interval != want.interval || o.flavor != want.flavor ||
		o.connections != want.connections || o.rate != want.rate || o.duration != want.duration ||
		o.messageBytes != want.messageBytes || o.pprofAddr != want.pprofAddr ||
		o.serveProtocol != want.serveProtocol || len(o.args) != 0 || o.client || o.server {
		t.Fatalf("defaults = %+v, want %+v", *o, want)
	}
}

func TestParseEnvironment(t *testing.T) {
	o, _, err := parse([]string{"--rate", "7", "host:1"}, environment(map[string]string{
		"TCPULSE_CLIENT":        "true",
		"TCPULSE_RATE":          "1000", // the command line takes precedence
		"TCPULSE_MESSAGE_BYTES": "1024",
		"TCPULSE_DURATION":      "3s",
		"TCPULSE_TCP_INFO":      "true",
		"TCPULSE_KERNEL_EVENTS": "1",
		"TCPULSE_FLAVOR":        "", // empty: ignored
		"TCPULSE_VERSION":       "true",
		"TCPULSE_HELP":          "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !o.client || o.rate != 7 || o.messageBytes != 1024 || o.duration != 3*time.Second || o.flavor != "persistent" || !o.tcpInfo || !o.kernelEvents {
		t.Fatalf("options = %+v", *o)
	}
	if o.showVersion || o.showHelp {
		t.Fatal("--version or --help was taken from the environment")
	}
	if len(o.args) != 1 || o.args[0] != "host:1" {
		t.Fatalf("args = %q", o.args)
	}
}

func TestParseInvalidEnvironment(t *testing.T) {
	o, _, err := parse(nil, environment(map[string]string{
		"TCPULSE_CONNECTIONS": "many",
		"TCPULSE_PROTOCOL":    "", // empty: ignored
	}))
	if err != nil {
		t.Fatalf("parse returned %v for an environment variable", err)
	}
	if err := o.envError(false); err == nil || !strings.Contains(err.Error(), "TCPULSE_CONNECTIONS") {
		t.Fatalf("envError(client) = %v, want an error naming TCPULSE_CONNECTIONS", err)
	}
	// --connections is a client option.
	if err := o.envError(true); err != nil {
		t.Fatalf("envError(server) = %v, want nil", err)
	}
}

func TestMainInvalidEnvironment(t *testing.T) {
	invalidRate := environment(map[string]string{"TCPULSE_RATE": "abc"})
	for _, tc := range []struct {
		name     string
		env      func(string) (string, bool)
		args     []string
		wantCode int
		want     string
	}{
		// --help and --version work whatever the environment.
		{"version", invalidRate, []string{"--version"}, exitOK, ""},
		{"help", invalidRate, []string{"--help"}, exitOK, ""},
		{"client option in client mode", invalidRate, []string{"-c", "host:1"}, exitUsage, "invalid TCPULSE_RATE"},
		// A variable of the other mode is ignored, as in tcpulse 0.8: the
		// server fails on its address, not on TCPULSE_INTERVAL.
		{"client option in server mode", environment(map[string]string{"TCPULSE_INTERVAL": "5"}), []string{"-s", "256.0.0.1:9100"}, exitError, "256.0.0.1:9100"},
		{"server option in client mode", environment(map[string]string{"TCPULSE_PROTOCOL": "x", "TCPULSE_RATE": "1.5"}), []string{"-c", "host:1"}, exitUsage, "invalid TCPULSE_RATE"},
		{"mode", environment(map[string]string{"TCPULSE_CLIENT": "maybe"}), []string{"host:1"}, exitUsage, "invalid TCPULSE_CLIENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runMain(t.Context(), tc.env, tc.args...)
			if r.code != tc.wantCode || !strings.Contains(r.stderr, tc.want) {
				t.Fatalf("exit %d, stderr %q; want exit %d and %q", r.code, r.stderr, tc.wantCode, tc.want)
			}
		})
	}
}

func TestEnvName(t *testing.T) {
	for option, want := range map[string]string{
		"rate":                    "TCPULSE_RATE",
		"message-bytes":           "TCPULSE_MESSAGE_BYTES",
		"merge-results-each-host": "TCPULSE_MERGE_RESULTS_EACH_HOST",
	} {
		if got := EnvName(option); got != want {
			t.Errorf("EnvName(%q) = %q, want %q", option, got, want)
		}
	}
}

func TestMainVersion(t *testing.T) {
	r := runMain(t.Context(), noEnvironment, "--version")
	if r.code != 0 || r.stdout != "tcpulse 1.2.3 (revision: abcdef0)\n" {
		t.Fatalf("--version: %+v", r)
	}
}

func TestMainHelp(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		r := runMain(t.Context(), noEnvironment, arg)
		if r.code != 0 || !strings.HasPrefix(r.stdout, "Usage: tcpulse [OPTIONS] <addresses...>") || r.stderr != "" {
			t.Fatalf("%s: %+v", arg, r)
		}
		// The option lines are those of tcpulse 0.8 (pflag 1.1 would print
		// "--client[=true|false]").
		for _, want := range []string{
			"\n  -c, --client                     run in client mode\n",
			"\n      --message-bytes int32        [client mode] TCP/UDP message size (bytes) (default 64)\n",
			"\n      --tcp-info                   [client mode] report TCP statistics of the connections read from the kernel (TCP_INFO, Linux only)\n",
			"\n      --kernel-events              [client mode] attribute kernel TCP events (retransmissions, resets) to the round trips with eBPF (Linux only, needs root or CAP_BPF and CAP_PERFMON)\n",
			"TCPULSE_MESSAGE_BYTES=1024",
			"Examples:",
		} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("%s: usage lacks %q", arg, want)
			}
		}
	}
}

func TestMainUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantCode int
		want     string
	}{
		{"unknown flag", []string{"--no-such-flag"}, exitUsage, "unknown flag: --no-such-flag"},
		{"invalid value", []string{"-c", "--rate", "fast"}, exitUsage, "invalid argument"},
		{"no mode", nil, exitError, "must specify either client (-c) or server (-s) mode"},
		{"both modes", []string{"-c", "-s"}, exitError, "cannot specify both"},
		{"no address", []string{"-c"}, exitError, "required addresses"},
		{"unknown protocol", []string{"-c", "--proto", "sctp", "host:1"}, exitError, `unexpected protocol "sctp"`},
		{"unknown flavor", []string{"-c", "--flavor", "bursty", "host:1"}, exitError, `unexpected connect flavor "bursty"`},
		{"two addresses files", []string{"-c", "--addrs-file", "a", "b"}, exitError, "must be one"},
		{"merge without show-only-results", []string{"-c", "--merge-results-each-host", "host:1"}, exitError, "requires --show-only-results"},
		{"no connection", []string{"-c", "--connections", "0", "host:1"}, exitError, "--connections must be at least 1"},
		{"no rate", []string{"-c", "--rate", "0", "host:1"}, exitError, "--rate must be at least 1"},
		{"negative rate", []string{"-c", "--flavor", "ephemeral", "--rate", "-5", "host:1"}, exitError, "--rate must be at least 1"},
		{"no duration", []string{"-c", "--duration", "0s", "host:1"}, exitError, "--duration must be positive"},
		{"no message", []string{"-c", "--rate", "1", "--duration", "900ms", "host:1"}, exitError, "sends no message"},
		{"too many messages", []string{"-c", "--rate", "2000000000", "--duration", "2000000h", "host:1"}, exitError, "too many messages"},
		{"no interval", []string{"-c", "--interval", "0s", "host:1"}, exitError, "--interval must be positive"},
		{"empty messages", []string{"-c", "--message-bytes", "0", "host:1"}, exitError, "--message-bytes must be at least 1"},
		{"oversized datagrams", []string{"-c", "--proto", "udp", "--message-bytes", "65508", "host:1"}, exitError, "at most 65507"},
		{"TCP_INFO of UDP", []string{"-c", "--proto", "udp", "--tcp-info", "host:1"}, exitError, "--tcp-info requires --proto tcp"},
		{"kernel events of UDP", []string{"-c", "--proto", "udp", "--kernel-events", "host:1"}, exitError, "--kernel-events requires --proto tcp"},
		{"unknown server protocol", []string{"-s", "--protocol", "sctp"}, exitError, `unexpected protocol "sctp"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runMain(t.Context(), noEnvironment, tc.args...)
			if r.code != tc.wantCode || !strings.Contains(r.stderr, tc.want) {
				t.Fatalf("exit %d, stderr %q; want exit %d and %q", r.code, r.stderr, tc.wantCode, tc.want)
			}
		})
	}
}

func TestReadAddrs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "addrs")
	if err := os.WriteFile(path, []byte("127.0.0.1:9100\n  10.0.0.1:9200\t[::1]:9300\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readAddrs(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "127.0.0.1:9100 10.0.0.1:9200 [::1]:9300" {
		t.Fatalf("readAddrs = %q", got)
	}
	if _, err := readAddrs(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("readAddrs of a missing file succeeded")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAddrs(empty); err == nil {
		t.Fatal("readAddrs of a file without addresses succeeded")
	}
}

// echoServers serves TCP and UDP on n loopback ports until the test ends.
func echoServers(t *testing.T, protocol string, n int) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := server.Listen(ctx, server.Config{
		Addrs:    slices.Repeat([]string{"127.0.0.1:0"}, n),
		Protocol: protocol,
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	var addrs []string
	listening := srv.TCPAddrs()
	if protocol == server.ProtocolUDP {
		listening = srv.UDPAddrs()
	}
	for _, a := range listening {
		addrs = append(addrs, a.String())
	}
	return addrs
}

func decodeJSONLines(t *testing.T, out string) []report.JSONLinesResult {
	t.Helper()
	var results []report.JSONLinesResult
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var r report.JSONLinesResult
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		results = append(results, r)
	}
	return results
}

func TestClientJSONLines(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protocol  string
		args      []string
		wantCount int64
	}{
		{"tcp persistent", server.ProtocolTCP, []string{"--connections", "2"}, 20},
		{"tcp ephemeral", server.ProtocolTCP, []string{"--flavor", "ephemeral"}, 10},
		{"udp", server.ProtocolUDP, []string{"--proto", "udp"}, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addrs := echoServers(t, tc.protocol, 2)
			args := append([]string{"-c", "--jsonlines", "--rate", "10", "--duration", "1s"}, tc.args...)
			r := runMain(t.Context(), noEnvironment, append(args, addrs...)...)
			if r.code != 0 {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			results := decodeJSONLines(t, r.stdout)
			if len(results) != len(addrs) {
				t.Fatalf("%d results, want %d:\n%s", len(results), len(addrs), r.stdout)
			}
			for i, res := range results {
				if res.Peer != addrs[i] || res.Count != tc.wantCount || res.LatencyMax < res.LatencyMin {
					t.Errorf("result %d = %+v, want peer %s with %d round trips", i, res, addrs[i], tc.wantCount)
				}
				if _, err := time.Parse(time.RFC3339, res.Timestamp); err != nil {
					t.Errorf("timestamp %q: %v", res.Timestamp, err)
				}
				if res.TCPInfo != nil || res.KernelEvents != nil {
					t.Errorf("result %d has tcp_info or kernel_events without their option", i)
				}
			}
		})
	}
}

func TestClientTCPInfo(t *testing.T) {
	addrs := echoServers(t, server.ProtocolTCP, 2)
	args := append([]string{"-c", "--jsonlines", "--tcp-info", "--connections", "2", "--rate", "10", "--duration", "1s"}, addrs...)
	r := runMain(t.Context(), noEnvironment, args...)
	if !client.TCPInfoSupported {
		if r.code != exitError || !strings.Contains(r.stderr, "--tcp-info is only supported on Linux") {
			t.Fatalf("exit %d, stderr %q; want --tcp-info refused", r.code, r.stderr)
		}
		return
	}
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	results := decodeJSONLines(t, r.stdout)
	if len(results) != len(addrs) {
		t.Fatalf("%d results, want %d:\n%s", len(results), len(addrs), r.stdout)
	}
	for i, res := range results {
		info := res.TCPInfo
		if res.Count != 20 || info == nil || info.Connections != 2 || info.SegmentsOut < 20 || info.SRTTMax < info.SRTTMean {
			t.Errorf("result %d = %+v, tcp_info %+v; want 20 round trips over 2 connections", i, res, info)
		}
	}
}

func TestClientTCPInfoMergedText(t *testing.T) {
	if !client.TCPInfoSupported {
		t.Skip("TCP_INFO is read on Linux only")
	}
	addrs := echoServers(t, server.ProtocolTCP, 2)
	args := append([]string{"-c", "--show-only-results", "--merge-results-each-host", "--tcp-info", "--flavor", "ephemeral", "--rate", "10", "--duration", "1s"}, addrs...)
	r := runMain(t.Context(), noEnvironment, args...)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) != 6 || lines[1] != report.ReportSeparator || lines[3] != report.ConnSeparator || !strings.HasPrefix(lines[4], "PEER                 CONNS ") {
		t.Fatalf("output:\n%s", r.stdout)
	}
	// One connection per message of each host.
	if fields := strings.Fields(lines[5]); fields[0] != "merged(2" || fields[2] != "20" {
		t.Fatalf("TCP statistics %q, want 20 connections of 2 hosts", lines[5])
	}
}

// canTrace reports whether this process may load the eBPF programs of
// --kernel-events, which tests normally may not (they run unprivileged).
func canTrace(t *testing.T) bool {
	t.Helper()
	if !kevents.Supported {
		return false
	}
	tr, err := kevents.Start()
	if err != nil {
		t.Logf("cannot load the eBPF programs: %v", err)
		return false
	}
	_ = tr.Close()
	return true
}

func TestClientKernelEvents(t *testing.T) {
	addrs := echoServers(t, server.ProtocolTCP, 2)
	args := append([]string{"-c", "--jsonlines", "--kernel-events", "--connections", "2", "--rate", "10", "--duration", "1s"}, addrs...)
	r := runMain(t.Context(), noEnvironment, args...)
	switch {
	case !kevents.Supported:
		if r.code != exitError || !strings.Contains(r.stderr, "--kernel-events is only supported on Linux on little-endian architectures") {
			t.Fatalf("exit %d, stderr %q; want --kernel-events refused", r.code, r.stderr)
		}
		return
	case !canTrace(t):
		// The failure to load is an error, with its reason, not a report
		// without events.
		if r.code != exitError || !strings.Contains(r.stderr, "--kernel-events: loading the eBPF programs") || r.stdout != "" {
			t.Fatalf("exit %d, stdout %q, stderr %q; want the load error", r.code, r.stdout, r.stderr)
		}
		return
	}
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	results := decodeJSONLines(t, r.stdout)
	if len(results) != len(addrs) {
		t.Fatalf("%d results, want %d:\n%s", len(results), len(addrs), r.stdout)
	}
	for i, res := range results {
		ev := res.KernelEvents
		// Every round trip is accounted for once: with events, or without.
		if ev == nil || res.Count != 20 || ev.UntrackedRoundTrips != 0 || ev.RoundTripsWithEvents+ev.RoundTripsWithoutEvents != res.Count {
			t.Errorf("result %d = %+v, kernel_events %+v; want 20 tracked round trips", i, res, ev)
		}
	}
}

func TestClientMergedResults(t *testing.T) {
	addrs := echoServers(t, server.ProtocolTCP, 3)
	args := append([]string{"-c", "--show-only-results", "--merge-results-each-host", "--connections", "1", "--rate", "10", "--duration", "1s"}, addrs...)
	r := runMain(t.Context(), noEnvironment, args...)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "PEER") || lines[1] != report.ReportSeparator {
		t.Fatalf("output:\n%s", r.stdout)
	}
	if fields := strings.Fields(lines[2]); fields[0] != "merged(3" || fields[1] != "hosts)" || fields[2] != "30" {
		t.Fatalf("merged line %q, want 30 round trips of 3 hosts", lines[2])
	}
}

func TestClientIntervalLines(t *testing.T) {
	addrs := echoServers(t, server.ProtocolTCP, 1)
	// Messages at 0, 100, ... 1900ms.
	r := runMain(t.Context(), noEnvironment, "-c", "--connections", "1", "--rate", "10", "--duration", "2s", "--interval", "600ms", addrs[0])
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	before, after, ok := strings.Cut(r.stdout, report.ReportSeparator+"\n")
	if !ok {
		t.Fatalf("no report in:\n%s", r.stdout)
	}
	intervals := strings.Split(strings.TrimSuffix(before, "\n"), "\n")[1:]
	if len(intervals) < 2 {
		t.Fatalf("%d interval lines, want at least 2:\n%s", len(intervals), r.stdout)
	}
	for _, line := range intervals {
		if fields := strings.Fields(line); fields[0] != addrs[0] || fields[1] == "0" {
			t.Errorf("interval line %q, want round trips to %s", line, addrs[0])
		}
	}
	if total := strings.Fields(after); total[0] != addrs[0] || total[1] != "20" {
		t.Fatalf("report %q, want 20 round trips to %s", after, addrs[0])
	}
}

func TestClientAddrsFile(t *testing.T) {
	addrs := echoServers(t, server.ProtocolTCP, 2)
	path := filepath.Join(t.TempDir(), "addrs")
	if err := os.WriteFile(path, []byte(strings.Join(addrs, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runMain(t.Context(), noEnvironment, "-c", "--addrs-file", "--jsonlines", "--connections", "1", "--rate", "5", "--duration", "1s", path)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if results := decodeJSONLines(t, r.stdout); len(results) != 2 || results[0].Peer != addrs[0] || results[1].Peer != addrs[1] {
		t.Fatalf("results:\n%s", r.stdout)
	}
}

func TestClientFromEnvironment(t *testing.T) {
	addrs := echoServers(t, server.ProtocolTCP, 1)
	r := runMain(t.Context(), environment(map[string]string{
		"TCPULSE_CLIENT":      "1",
		"TCPULSE_JSONLINES":   "true",
		"TCPULSE_CONNECTIONS": "1",
		"TCPULSE_RATE":        "5",
		"TCPULSE_DURATION":    "1s",
	}), addrs[0])
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if results := decodeJSONLines(t, r.stdout); len(results) != 1 || results[0].Count != 5 {
		t.Fatalf("results:\n%s", r.stdout)
	}
}

func TestClientConnectionRefused(t *testing.T) {
	addrs := echoServers(t, server.ProtocolUDP, 1) // nothing listens on TCP
	r := runMain(t.Context(), noEnvironment, "-c", "--duration", "1s", addrs[0])
	if r.code != exitError || !strings.Contains(r.stderr, "connection error") {
		t.Fatalf("exit %d, stderr %q; want a connection error", r.code, r.stderr)
	}
}

// syncBuffer is a bytes.Buffer that a test can read while Main writes it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServer(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var stdout syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- Main(ctx, Env{
			Program:   "tcpulse",
			Args:      []string{"-s", "--protocol", "tcp", "127.0.0.1:0"},
			Stdout:    &stdout,
			Stderr:    io.Discard,
			LookupEnv: noEnvironment,
		})
	}()
	for deadline := time.Now().Add(5 * time.Second); stdout.String() == "" && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if got := stdout.String(); got != "Listening at [\"127.0.0.1:0\"] ...\n" {
		t.Fatalf("stdout = %q", got)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop after its context was canceled")
	}
}

func TestServerListenError(t *testing.T) {
	r := runMain(t.Context(), noEnvironment, "-s", "256.0.0.1:9100")
	if r.code != exitError || !strings.Contains(r.stderr, "256.0.0.1:9100") {
		t.Fatalf("exit %d, stderr %q; want a listen error", r.code, r.stderr)
	}
}

func TestValidClientEdges(t *testing.T) {
	for _, args := range [][]string{
		// --connections only matters to persistent TCP connections.
		{"-c", "--flavor", "ephemeral", "--connections", "0", "host:1"},
		{"-c", "--proto", "udp", "--connections", "0", "--message-bytes", "65507", "host:1"},
		{"-c", "--rate", "1", "--duration", "1s", "--message-bytes", "1", "host:1"},
		{"-c", "--rate", "1000000000", "--duration", "1ns", "host:1"},
		// --interval only matters when interval lines are printed.
		{"-c", "--jsonlines", "--interval", "0s", "host:1"},
		{"-c", "--show-only-results", "--interval", "-1s", "host:1"},
	} {
		o, _, err := parse(args, noEnvironment)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateClient(o); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}

// FuzzParse checks that no command line or environment makes parse panic,
// and that the options validation accepts can generate load: client.Run
// would panic on a rate below 1, time.NewTicker on an interval that is not
// positive.
func FuzzParse(f *testing.F) {
	f.Add("-c --rate 0 host:1", "")
	f.Add("-c --proto udp host:1", "TCPULSE_MESSAGE_BYTES=65508")
	f.Add("-s --protocol udp 127.0.0.1:0", "TCPULSE_LISTEN_ADDRS_FILE=")
	f.Add("--connections=-1 --interval=0s -c a", "TCPULSE_FLAVOR=ephemeral\nTCPULSE_DURATION=1ns")
	f.Add("-c --rate 1 --duration 999ms a", "TCPULSE_RATE=x")
	f.Add("-c --tcp-info a", "TCPULSE_PROTO=udp")
	f.Add("-c --kernel-events a", "TCPULSE_PROTO=udp")
	f.Fuzz(func(t *testing.T, cmdline, env string) {
		vars := map[string]string{}
		for _, kv := range strings.Split(env, "\n") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				vars[k] = v
			}
		}
		o, _, err := parse(strings.Fields(cmdline), environment(vars))
		if err != nil {
			return
		}
		if o.envError(true) == nil && validateServer(o) == nil &&
			o.serveProtocol != "tcp" && o.serveProtocol != "udp" && o.serveProtocol != "all" {
			t.Fatalf("server protocol %q accepted", o.serveProtocol)
		}
		if o.envError(false) != nil || validateClient(o) != nil {
			return
		}
		if o.rate < 1 || o.duration <= 0 || o.messageBytes < 1 || len(o.args) == 0 {
			t.Fatalf("accepted options cannot generate load: %+v", *o)
		}
		if !o.jsonlines && !o.showOnlyResults && o.interval <= 0 {
			t.Fatalf("accepted interval lines every %v", o.interval)
		}
		// At least one message, computed exactly: rate × duration / 1s.
		messages := new(big.Int).Mul(big.NewInt(int64(o.rate)), big.NewInt(int64(o.duration)))
		if messages.Quo(messages, big.NewInt(int64(time.Second))).Sign() <= 0 {
			t.Fatalf("accepted a run without a message: %d/s for %v", o.rate, o.duration)
		}
		if o.proto == "tcp" && o.flavor == "persistent" && o.connections < 1 {
			t.Fatalf("accepted persistent run without connections: %+v", *o)
		}
		if o.proto == "udp" && o.messageBytes > maxUDPMessageBytes {
			t.Fatalf("accepted %d-byte datagrams", o.messageBytes)
		}
		if o.tcpInfo && (o.proto != "tcp" || !client.TCPInfoSupported) {
			t.Fatalf("accepted --tcp-info with --proto %s (supported: %v)", o.proto, client.TCPInfoSupported)
		}
		if o.kernelEvents && (o.proto != "tcp" || !kevents.Supported) {
			t.Fatalf("accepted --kernel-events with --proto %s (supported: %v)", o.proto, kevents.Supported)
		}
	})
}

// silentServer accepts TCP connections and never answers, until the test
// ends.
func silentServer(t *testing.T) string {
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
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestClientNoRoundTrip(t *testing.T) {
	answering := echoServers(t, server.ProtocolTCP, 1)[0]
	silent := silentServer(t)
	r := runMain(t.Context(), noEnvironment, "-c", "--jsonlines", "--connections", "1", "--rate", "5", "--duration", "1s", answering, silent)
	if r.code != exitError || !strings.Contains(r.stderr, "no round trip completed with "+silent) || strings.Contains(r.stderr, answering) {
		t.Fatalf("exit %d, stderr %q; want exit 1 naming %s only", r.code, r.stderr, silent)
	}
	// The report is printed all the same.
	results := decodeJSONLines(t, r.stdout)
	if len(results) != 2 || results[0].Count != 5 || results[1].Count != 0 {
		t.Fatalf("results:\n%s", r.stdout)
	}
}

func TestClientRateOverTheDuration(t *testing.T) {
	addr := echoServers(t, server.ProtocolTCP, 1)[0]
	// The single message is back long before the end of the second.
	r := runMain(t.Context(), noEnvironment, "-c", "--jsonlines", "--connections", "1", "--rate", "1", "--duration", "1s", addr)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if results := decodeJSONLines(t, r.stdout); len(results) != 1 || results[0].Count != 1 || results[0].Rate > 1 || results[0].Rate < 0.9 {
		t.Fatalf("results:\n%s\nwant 1 round trip at 1/s", r.stdout)
	}
}

func TestClientSameAddressTwice(t *testing.T) {
	addr := echoServers(t, server.ProtocolTCP, 1)[0]
	r := runMain(t.Context(), noEnvironment, "-c", "--jsonlines", "--connections", "1", "--rate", "10", "--duration", "1s", addr, addr)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	// Each occurrence puts its own load and counts its own round trips.
	if results := decodeJSONLines(t, r.stdout); len(results) != 2 || results[0].Count != 10 || results[1].Count != 10 {
		t.Fatalf("results:\n%s\nwant 10 round trips for each occurrence", r.stdout)
	}
}
