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
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/yuuki/tcpulse/internal/client"
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

func runServer(ctx context.Context, o *options, stdout io.Writer, logger *slog.Logger) error {
	if err := raiseFileLimit(); err != nil {
		return fmt.Errorf("setting file limit: %w", err)
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
	return nil
}

func runClient(ctx context.Context, o *options, stdout io.Writer, logger *slog.Logger) error {
	if err := validateClient(o); err != nil {
		return err
	}

	if o.enablePprof {
		stop, err := startPprof(o.pprofAddr, logger)
		if err != nil {
			return err
		}
		defer stop()
	}

	if err := raiseFileLimit(); err != nil {
		return fmt.Errorf("setting file limit: %w", err)
	}

	addrs := o.args
	if o.addrsFile {
		var err error
		if addrs, err = readAddrs(o.args[0]); err != nil {
			return err
		}
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
		Logger:       logger,
	}
	recorders := stats.NewSet(addrs, o.mergeResultsEachHost)
	printIntervalLines := !o.showOnlyResults && !o.jsonlines

	intervalCtx, stopIntervals := context.WithCancel(ctx)
	defer stopIntervals()
	var intervals sync.WaitGroup
	eg, runCtx := errgroup.WithContext(ctx)
	for _, addr := range addrs {
		rec := recorders.For(addr)
		if printIntervalLines {
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

	var results []report.Result
	if o.mergeResultsEachHost {
		results = []report.Result{{Peer: report.MergedPeer(len(addrs)), Summary: recorders.For("").Total()}}
	} else {
		for _, addr := range addrs {
			results = append(results, report.Result{Peer: addr, Summary: recorders.For(addr).Total()})
		}
	}
	if o.jsonlines {
		return printer.JSONLines(results)
	}
	return printer.Report(results)
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
func startPprof(addr string, logger *slog.Logger) (stop func(), err error) {
	ln, err := net.Listen("tcp", addr)
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

// raiseFileLimit raises the soft limit of open files to the hard limit.
func raiseFileLimit() error {
	var rLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
		return fmt.Errorf("could not get rlimit: %w", err)
	}
	if rLimit.Cur < rLimit.Max {
		rLimit.Cur = rLimit.Max
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
			return fmt.Errorf("could not set rlimit: %w", err)
		}
	}
	return nil
}

// readAddrs returns the whitespace-separated addresses of a file.
func readAddrs(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading addresses file: %w", err)
	}
	return strings.Fields(string(data)), nil
}
