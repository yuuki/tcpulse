// Package cli implements the tcpulse command: it parses the command line and
// the environment, then runs the echo server or the load generator.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/yuuki/tcpulse/internal/client"
	"github.com/yuuki/tcpulse/internal/kevents"
	"github.com/yuuki/tcpulse/internal/report"
	"github.com/yuuki/tcpulse/internal/server"
	"github.com/yuuki/tcpulse/internal/stats"
)

// Exit statuses.
const (
	exitOK    = 0
	exitError = 1
	// exitUsage reports a command line that cannot be parsed.
	exitUsage = 2
)

// Env is what Main takes from the process.
type Env struct {
	// Program is the name of the command in the usage text.
	Program string
	// Args are the command line arguments, without the program name.
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	// LookupEnv returns the value of an environment variable (os.LookupEnv).
	LookupEnv func(string) (string, bool)
	// Version and Revision are printed by --version.
	Version  string
	Revision string
}

// Main runs tcpulse until it is done or ctx is canceled, and returns the exit
// status.
func Main(ctx context.Context, env Env) int {
	logger := slog.New(slog.NewTextHandler(env.Stderr, nil))

	o, fs, err := parse(env.Args, env.LookupEnv)
	if err != nil {
		fmt.Fprintf(env.Stderr, "Error: %v\n", err)
		fmt.Fprintf(env.Stderr, "Run '%s --help' for usage.\n", env.Program)
		return exitUsage
	}
	if o.showVersion {
		fmt.Fprintf(env.Stdout, "tcpulse %s (revision: %s)\n", env.Version, env.Revision)
		return exitOK
	}
	if o.showHelp {
		usage(env.Stdout, env.Program, fs)
		return exitOK
	}
	if err := o.envError(o.server && !o.client); err != nil {
		fmt.Fprintf(env.Stderr, "Error: %v\n", err)
		return exitUsage
	}

	switch {
	case o.client && o.server:
		fmt.Fprintf(env.Stderr, "Error: cannot specify both client (-c) and server (-s) modes\n")
		return exitError
	case !o.client && !o.server:
		fmt.Fprintf(env.Stderr, "Error: must specify either client (-c) or server (-s) mode\n")
		usage(env.Stderr, env.Program, fs)
		return exitError
	}

	if o.server {
		err = runServer(ctx, o, env.Stdout, logger)
	} else {
		err = runClient(ctx, o, env.Stdout, logger)
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "Error: %v\n", err)
		return exitError
	}
	return exitOK
}

func validateServer(o *options) error {
	switch o.serveProtocol {
	case server.ProtocolTCP, server.ProtocolUDP, server.ProtocolAll:
		return nil
	default:
		return fmt.Errorf("unexpected protocol %q", o.serveProtocol)
	}
}

func runServer(ctx context.Context, o *options, stdout io.Writer, logger *slog.Logger) error {
	if err := validateServer(o); err != nil {
		return err
	}
	addrs := o.args
	if len(addrs) == 0 {
		addrs = []string{defaultListenAddr}
	}
	if o.listenAddrsFile != "" {
		var err error
		if addrs, err = readAddrs(o.listenAddrsFile); err != nil {
			return fmt.Errorf("reading addresses from file: %w", err)
		}
	}

	srv, err := server.Listen(ctx, server.Config{
		Addrs:    addrs,
		Protocol: o.serveProtocol,
		Logger:   logger,
	})
	if err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	fmt.Fprintf(stdout, "Listening at %q ...\n", addrs)
	return srv.Serve(ctx)
}

func validateClient(o *options) error {
	switch o.flavor {
	case client.FlavorPersistent, client.FlavorEphemeral:
	default:
		return fmt.Errorf("unexpected connect flavor %q", o.flavor)
	}
	switch o.proto {
	case client.ProtocolTCP, client.ProtocolUDP:
	default:
		return fmt.Errorf("unexpected protocol %q", o.proto)
	}
	if len(o.args) < 1 {
		return errors.New("required addresses")
	}
	if o.addrsFile && len(o.args) != 1 {
		return errors.New("the number of addresses file must be one")
	}
	if o.mergeResultsEachHost && !o.showOnlyResults {
		return errors.New("--merge-results-each-host flag requires --show-only-results flag")
	}
	// Each of these would make the run panic or send nothing.
	if o.proto == client.ProtocolTCP && o.flavor == client.FlavorPersistent && o.connections < 1 {
		return fmt.Errorf("--connections must be at least 1, got %d", o.connections)
	}
	if o.rate < 1 {
		return fmt.Errorf("--rate must be at least 1, got %d", o.rate)
	}
	if o.duration <= 0 {
		return fmt.Errorf("--duration must be positive, got %v", o.duration)
	}
	switch n, ok := client.Messages(int(o.rate), o.duration); {
	case !ok:
		return fmt.Errorf("--rate %d for --duration %v makes too many messages to count", o.rate, o.duration)
	case n < 1:
		return fmt.Errorf("--rate %d for --duration %v sends no message", o.rate, o.duration)
	}
	// The interval only paces the interval lines.
	if printsIntervals(o) && o.interval <= 0 {
		return fmt.Errorf("--interval must be positive, got %v", o.interval)
	}
	if o.messageBytes < 1 {
		return fmt.Errorf("--message-bytes must be at least 1, got %d", o.messageBytes)
	}
	if o.proto == client.ProtocolUDP && o.messageBytes > maxUDPMessageBytes {
		return fmt.Errorf("--message-bytes must be at most %d with --proto udp, got %d", maxUDPMessageBytes, o.messageBytes)
	}
	return validateKernelOptions(o)
}

// validateKernelOptions rejects the options that read the kernel's view of
// TCP connections (TCP_INFO, eBPF) where there is none: with UDP, or on a
// platform other than Linux.
func validateKernelOptions(o *options) error {
	for _, opt := range []struct {
		name      string
		set       bool
		supported bool
		platforms string
	}{
		{"--tcp-info", o.tcpInfo, client.TCPInfoSupported, "Linux"},
		{"--kernel-events", o.kernelEvents, kevents.Supported, "Linux on little-endian architectures"},
		{"--latency-breakdown", o.latencyBreakdown, client.BreakdownSupported, "Linux"},
	} {
		switch {
		case !opt.set:
		case o.proto != client.ProtocolTCP:
			return fmt.Errorf("%s requires --proto tcp", opt.name)
		case !opt.supported:
			return fmt.Errorf("%s is only supported on %s", opt.name, opt.platforms)
		}
	}
	// The stream offsets of the timestamps are known on a connection that
	// carries one message after another.
	if o.latencyBreakdown && o.flavor != client.FlavorPersistent {
		return errors.New("--latency-breakdown requires --flavor persistent")
	}
	return nil
}

// printsIntervals reports whether a client run prints interval lines.
func printsIntervals(o *options) bool {
	return !o.showOnlyResults && !o.jsonlines
}

// maxUDPMessageBytes is the largest UDP payload over IPv4: 65535 bytes less
// the IPv4 and UDP headers.
const maxUDPMessageBytes = 65535 - 20 - 8

func runClient(ctx context.Context, o *options, stdout io.Writer, logger *slog.Logger) error {
	if err := validateClient(o); err != nil {
		return err
	}

	if o.enablePprof {
		stop, err := startPprof(ctx, o.pprofAddr, logger)
		if err != nil {
			return err
		}
		defer stop()
	}

	addrs := o.args
	if o.addrsFile {
		var err error
		if addrs, err = readAddrs(o.args[0]); err != nil {
			return err
		}
	}

	// The programs count the connections the process opens from now on,
	// for every target.
	var events client.EventReader
	if o.kernelEvents {
		tracer, err := kevents.Start()
		if err != nil {
			return fmt.Errorf("--kernel-events: %w", err)
		}
		defer func() { _ = tracer.Close() }()
		if err := tracer.GenerationError(); err != nil {
			logger.Warn("kernel events are read with a system call after every round trip", "reason", err)
		}
		events = tracer
	}

	printer := report.NewPrinter(stdout)
	if !o.jsonlines {
		if err := printer.Header(); err != nil {
			return err
		}
	}

	cfg := client.Config{
		Protocol:     o.proto,
		Flavor:       o.flavor,
		Connections:  int(o.connections),
		Rate:         int(o.rate),
		Duration:     o.duration,
		MessageBytes: int(o.messageBytes),
		TCPInfo:      o.tcpInfo,
		Breakdown:    o.latencyBreakdown,
		Events:       events,
		Logger:       logger,
	}
	recorders := stats.NewSet(len(addrs), o.mergeResultsEachHost)

	intervalCtx, stopIntervals := context.WithCancel(ctx)
	defer stopIntervals()
	var intervals sync.WaitGroup
	eg, runCtx := errgroup.WithContext(ctx)
	start := time.Now()
	for i, addr := range addrs {
		rec := recorders.For(i)
		if printsIntervals(o) {
			intervals.Go(func() {
				printIntervals(intervalCtx, printer, addr, rec, o.interval, logger)
			})
		}
		eg.Go(func() error {
			return client.Run(runCtx, cfg, addr, rec)
		})
	}
	err := eg.Wait()
	// No interval line may follow the report.
	stopIntervals()
	intervals.Wait()
	if err != nil {
		return fmt.Errorf("connection error: %w", err)
	}

	// The rate of the run is over its duration: the run stops when its last
	// echo is back, up to 1/rate before the end of the duration, which
	// would inflate the rate (to thousands per second for a single
	// message). A run that was interrupted is measured until it stopped.
	run := time.Since(start)
	if ctx.Err() == nil {
		run = max(run, o.duration)
	}

	result := func(peer string, rec *stats.Recorder) report.Result {
		r := report.Result{Peer: peer, Summary: rec.Total(run)}
		if o.tcpInfo {
			conns := rec.Conns()
			r.Conns = &conns
		}
		if o.kernelEvents {
			ev := rec.Events()
			r.Events = &ev
		}
		if o.latencyBreakdown {
			b := rec.Breakdown()
			r.Breakdown = &b
		}
		return r
	}
	var results []report.Result
	if o.mergeResultsEachHost {
		results = []report.Result{result(report.MergedPeer(len(addrs)), recorders.For(0))}
	} else {
		for i, addr := range addrs {
			results = append(results, result(addr, recorders.For(i)))
		}
	}
	if o.jsonlines {
		err = printer.JSONLines(results)
	} else {
		err = printer.Report(results)
	}
	if err != nil {
		return err
	}
	return noRoundTrip(results)
}

// noRoundTrip returns an error naming the targets with which no round trip
// completed (the report shows them too): a target that never answers must
// not pass for a successful run.
func noRoundTrip(results []report.Result) error {
	var silent []string
	for _, r := range results {
		if r.Summary.Count == 0 {
			silent = append(silent, r.Peer)
		}
	}
	if len(silent) == 0 {
		return nil
	}
	return fmt.Errorf("no round trip completed with %s", strings.Join(silent, ", "))
}

// printIntervals prints the round trips to peer of every interval until ctx
// is done.
func printIntervals(ctx context.Context, p *report.Printer, peer string, rec *stats.Recorder, every time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.Line(report.Result{Peer: peer, Summary: rec.TakeInterval()}); err != nil {
				logger.Error("could not print stats", "error", err)
			}
		}
	}
}

// startPprof serves the pprof endpoints (/debug/pprof/) on addr until stop
// is called.
func startPprof(ctx context.Context, addr string, logger *slog.Logger) (stop func(), err error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("starting pprof server: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("pprof server error", "error", err)
		}
	}()
	return func() { _ = srv.Close() }, nil
}

// readAddrs returns the whitespace-separated addresses of a file, which
// must hold at least one.
func readAddrs(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading addresses file: %w", err)
	}
	addrs := strings.Fields(string(data))
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no address in %s", path)
	}
	return addrs, nil
}
