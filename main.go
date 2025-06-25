/*
Copyright © 2021 Yuuki Tsubouchi <yuki.tsubo@gmail.com>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

var (
	// Mode flags
	clientMode bool
	serverMode bool

	// Client-specific flags
	protocol             string
	intervalStats        time.Duration
	connectFlavor        string
	connections          int32
	rate                 int32
	duration             time.Duration
	messageBytes         int32
	showOnlyResults      bool
	mergeResultsEachHost bool
	jsonlines            bool
	pprofMutex           sync.RWMutex
	pprof                bool
	pprofAddr            string
	addrsFile            bool

	// Server-specific flags
	listenAddrs     []string
	listenAddrsFile string
	serveProtocol   string
)

func init() {
	// Mode flags
	pflag.BoolVarP(&clientMode, "client", "c", false, "run in client mode")
	pflag.BoolVarP(&serverMode, "server", "s", false, "run in server mode")

	// Client flags
	pflag.StringVar(&protocol, "proto", "tcp", "[client mode] protocol (tcp or udp)")
	pflag.DurationVar(&intervalStats, "interval", 5*time.Second, "[client mode] interval for printing stats")
	pflag.StringVar(&connectFlavor, "flavor", flavorPersistent,
		fmt.Sprintf("[client mode] connect behavior type '%s' or '%s'", flavorPersistent, flavorEphemeral))
	pflag.Int32Var(&connections, "connections", 10,
		fmt.Sprintf("[client mode] Number of concurrent connections to keep (only for '%s')", flavorPersistent))
	pflag.Int32Var(&rate, "rate", 100,
		fmt.Sprintf("[client mode] Message throughput per connection (/s) (for '%s' and UDP) or new connections throughput (/s) (for '%s'). Total messages (CNT) = rate * duration * connections (for '%s') or rate * duration (for '%s')", flavorPersistent, flavorEphemeral, flavorPersistent, flavorEphemeral))
	pflag.DurationVar(&duration, "duration", 10*time.Second, "[client mode] measurement period")
	pflag.Int32Var(&messageBytes, "message-bytes", 64, "[client mode] TCP/UDP message size (bytes)")
	pflag.BoolVar(&showOnlyResults, "show-only-results", false, "[client mode] print only results of measurement stats")
	pflag.BoolVar(&mergeResultsEachHost, "merge-results-each-host", false, "[client mode] merge results of each host (with --show-only-results)")
	pflag.BoolVar(&jsonlines, "jsonlines", false, "[client mode] output results in JSON Lines format")
	pflag.BoolVar(&addrsFile, "addrs-file", false, "[client mode] enable to pass a file including a pair of addresses and ports to an argument")
	pflag.BoolVar(&pprof, "enable-pprof", false, "[client mode] enable pprof profiling")
	pflag.StringVar(&pprofAddr, "pprof-addr", "localhost:6060", "[client mode] pprof listening address:port")

	// Server flags
	pflag.StringVar(&serveProtocol, "protocol", "all", "[server mode] listening protocol ('tcp' or 'udp')")
	pflag.StringVar(&listenAddrsFile, "listen-addrs-file", "", "[server mode] enable to pass a file including a pair of addresses and ports")

	// Configure viper for environment variables
	viper.SetEnvPrefix("TCPULSE")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	viper.BindPFlags(pflag.CommandLine)
}

func main() {
	pflag.Parse()

	// Read values from viper (includes environment variables)
	clientMode = viper.GetBool("client")
	serverMode = viper.GetBool("server")
	protocol = viper.GetString("proto")
	intervalStats = viper.GetDuration("interval")
	connectFlavor = viper.GetString("flavor")
	connections = viper.GetInt32("connections")
	rate = viper.GetInt32("rate")
	duration = viper.GetDuration("duration")
	messageBytes = viper.GetInt32("message-bytes")
	showOnlyResults = viper.GetBool("show-only-results")
	mergeResultsEachHost = viper.GetBool("merge-results-each-host")
	jsonlines = viper.GetBool("jsonlines")
	addrsFile = viper.GetBool("addrs-file")
	pprof = viper.GetBool("enable-pprof")
	pprofAddr = viper.GetString("pprof-addr")
	serveProtocol = viper.GetString("protocol")
	listenAddrsFile = viper.GetString("listen-addrs-file")

	// Handle version flag
	handleVersion()

	// Handle help flag
	handleHelp()

	// Handle listen addresses for server mode
	if serverMode {
		// Default listen address if none provided
		if len(pflag.Args()) == 0 {
			listenAddrs = []string{"0.0.0.0:9100"}
		} else {
			listenAddrs = pflag.Args()
		}
	}

	// Validate mode selection
	if clientMode && serverMode {
		fmt.Fprintf(os.Stderr, "Error: cannot specify both client (-c) and server (-s) modes\n")
		os.Exit(1)
	}

	if !clientMode && !serverMode {
		fmt.Fprintf(os.Stderr, "Error: must specify either client (-c) or server (-s) mode\n")
		printUsage()
		os.Exit(1)
	}

	var err error
	if serverMode {
		err = runServer()
	} else {
		err = runClient()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] <addresses...>\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "tcpulse is a concurrent TCP/UDP load generator that provides fine-grained, flow-level control\n\n")
	fmt.Fprintf(os.Stderr, "Modes:\n")
	fmt.Fprintf(os.Stderr, "  -c, --client    Run in client mode (connect to servers)\n")
	fmt.Fprintf(os.Stderr, "  -s, --server    Run in server mode (accept connections)\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	pflag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nOutput Format (Client Mode):\n")
	fmt.Fprintf(os.Stderr, "  CNT: Total number of messages/requests sent (not connections)\n")
	fmt.Fprintf(os.Stderr, "  For persistent mode: CNT = rate × duration × connections\n")
	fmt.Fprintf(os.Stderr, "  For ephemeral mode:  CNT = rate × duration\n\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  %s -s                          # Start server on default port 9100\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -s 0.0.0.0:8080             # Start server on port 8080\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -c localhost:9100           # Connect to server as client\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -c --connections 50 host:port # Connect with 50 connections\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -c --connections 1 --rate 1 --duration 1s host:port # Send exactly 1 message\n", os.Args[0])
}

func runServer() error {
	if err := SetRLimitNoFile(); err != nil {
		return fmt.Errorf("setting file limit: %w", err)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(), unix.SIGINT, unix.SIGTERM)
	defer stop()

	if listenAddrsFile != "" {
		addrs, err := getAddrsFromFile(listenAddrsFile)
		if err != nil {
			return fmt.Errorf("reading addresses from file: %w", err)
		}
		listenAddrs = addrs
	}

	fmt.Printf("Listening at %q ...\n", listenAddrs)

	config := ServerConfig{
		ListenAddrs: listenAddrs,
		Protocol:    serveProtocol,
	}

	server := NewServer(config)
	return server.Start(ctx)
}

func runClient() error {
	args := pflag.Args()

	// Validate client arguments and flags
	switch connectFlavor {
	case flavorPersistent, flavorEphemeral:
	default:
		return fmt.Errorf("unexpected connect flavor %q", connectFlavor)
	}

	switch protocol {
	case "tcp", "udp":
	default:
		return fmt.Errorf("unexpected protocol %q", protocol)
	}

	if len(args) < 1 {
		return fmt.Errorf("required addresses")
	}

	if addrsFile && len(args) != 1 {
		return fmt.Errorf("the number of addresses file must be one")
	}

	if mergeResultsEachHost && !showOnlyResults {
		return fmt.Errorf("--merge-results-each-host flag requires --show-only-results flag")
	}

	setPprofServer()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := SetRLimitNoFile(); err != nil {
		return fmt.Errorf("setting file limit: %w", err)
	}

	addrs := args
	if addrsFile {
		var err error
		addrs, err = getAddrsFromFile(args[0])
		if err != nil {
			return err
		}
	}

	printer := NewPrinter(os.Stdout)
	if !jsonlines {
		printer.PrintStatHeader()
	}

	config := ClientConfig{
		Protocol:             protocol,
		ConnectFlavor:        connectFlavor,
		Connections:          connections,
		Rate:                 rate,
		Duration:             duration,
		MessageBytes:         messageBytes,
		MergeResultsEachHost: mergeResultsEachHost,
		JSONLines:            jsonlines,
	}

	client := NewClient(config)

	eg, ctx := errgroup.WithContext(ctx)
	for _, addr := range addrs {
		addr := addr
		eg.Go(func() error {
			if showOnlyResults || jsonlines {
				return client.ConnectToAddresses(ctx, []string{addr})
			}
			runStatLinePrinter(ctx, printer, addr, intervalStats, mergeResultsEachHost)
			return client.ConnectToAddresses(ctx, []string{addr})
		})
	}

	if err := eg.Wait(); err != nil {
		return fmt.Errorf("connection error: %w", err)
	}

	if jsonlines {
		printer.PrintJSONLinesReport(addrs, mergeResultsEachHost)
	} else {
		printer.PrintReport(addrs, mergeResultsEachHost)
	}
	return nil
}

func setPprofServer() {
	pprofMutex.RLock()
	enablePprof := pprof
	addr := pprofAddr
	pprofMutex.RUnlock()

	if !enablePprof {
		return
	}
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			slog.Error("pprof server error", "error", err)
		}
	}()
}

// SetRLimitNoFile avoids too many open files error.
func SetRLimitNoFile() error {
	var rLimit syscall.Rlimit

	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		return fmt.Errorf("could not get rlimit: %w", err)
	}

	if rLimit.Cur < rLimit.Max {
		rLimit.Cur = rLimit.Max
		err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
		if err != nil {
			return fmt.Errorf("could not set rlimit: %w", err)
		}
	}

	return nil
}

func getAddrsFromFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading addresses file: %w", err)
	}
	return strings.Fields(string(data)), nil
}
