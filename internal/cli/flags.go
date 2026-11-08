package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/yuuki/tcpulse/internal/client"
	"github.com/yuuki/tcpulse/internal/server"
)

// EnvPrefix starts the name of the environment variable of every option:
// TCPULSE_ followed by the option name in upper case, with '-' replaced by
// '_' (TCPULSE_MESSAGE_BYTES for --message-bytes).
const EnvPrefix = "TCPULSE_"

// noEnv lists the options that have no environment variable.
var noEnv = map[string]bool{"help": true, "version": true}

// serverOptions are the options of server mode only, and commonOptions those
// of both modes; every other option belongs to client mode only.
var (
	serverOptions = map[string]bool{"protocol": true, "listen-addrs-file": true}
	commonOptions = map[string]bool{"client": true, "server": true, "help": true, "version": true}
)

const defaultListenAddr = "0.0.0.0:9100"

// options holds the command line after the environment has filled in the
// options it does not give.
type options struct {
	client bool
	server bool

	// Client mode.
	proto                string
	interval             time.Duration
	flavor               string
	connections          int32
	rate                 int32
	duration             time.Duration
	messageBytes         int32
	showOnlyResults      bool
	mergeResultsEachHost bool
	jsonlines            bool
	tcpInfo              bool
	addrsFile            bool
	enablePprof          bool
	pprofAddr            string

	// Server mode.
	serveProtocol   string
	listenAddrsFile string

	showVersion bool
	showHelp    bool

	// args are the positional arguments: addresses to connect to (or the
	// addresses file), or to listen on.
	args []string

	// envErrors holds, by option name, the environment variables whose
	// value could not be parsed. Only those of the mode that runs are
	// errors (envError), as tcpulse 0.8 ignored the others.
	envErrors map[string]error
}

// envError returns the error of the first environment variable (by option
// name) that could not be parsed among those of server mode, or else of
// client mode.
func (o *options) envError(serverMode bool) error {
	names := make([]string, 0, len(o.envErrors))
	for name := range o.envErrors {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if commonOptions[name] || serverOptions[name] == serverMode {
			return o.envErrors[name]
		}
	}
	return nil
}

func newFlagSet(o *options) *pflag.FlagSet {
	fs := pflag.NewFlagSet("tcpulse", pflag.ContinueOnError)
	// Main reports errors and prints the usage itself.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	// Modes.
	fs.BoolVarP(&o.client, "client", "c", false, "run in client mode")
	fs.BoolVarP(&o.server, "server", "s", false, "run in server mode")

	// Client mode.
	fs.StringVar(&o.proto, "proto", client.ProtocolTCP, "[client mode] protocol (tcp or udp)")
	fs.DurationVar(&o.interval, "interval", 5*time.Second, "[client mode] interval for printing stats")
	fs.StringVar(&o.flavor, "flavor", client.FlavorPersistent,
		fmt.Sprintf("[client mode] connect behavior type '%s' or '%s'", client.FlavorPersistent, client.FlavorEphemeral))
	fs.Int32Var(&o.connections, "connections", 10,
		fmt.Sprintf("[client mode] Number of concurrent connections to keep (only for '%s')", client.FlavorPersistent))
	fs.Int32Var(&o.rate, "rate", 100,
		fmt.Sprintf("[client mode] Message throughput per connection (/s) (for '%s' and UDP) or new connections throughput (/s) (for '%s'). Total messages (CNT) = rate * duration * connections (for '%s') or rate * duration (for '%s')",
			client.FlavorPersistent, client.FlavorEphemeral, client.FlavorPersistent, client.FlavorEphemeral))
	fs.DurationVar(&o.duration, "duration", 10*time.Second, "[client mode] measurement period")
	fs.Int32Var(&o.messageBytes, "message-bytes", 64, "[client mode] TCP/UDP message size (bytes)")
	fs.BoolVar(&o.showOnlyResults, "show-only-results", false, "[client mode] print only results of measurement stats")
	fs.BoolVar(&o.mergeResultsEachHost, "merge-results-each-host", false, "[client mode] merge results of each host (with --show-only-results)")
	fs.BoolVar(&o.jsonlines, "jsonlines", false, "[client mode] output results in JSON Lines format")
	fs.BoolVar(&o.tcpInfo, "tcp-info", false, "[client mode] report TCP statistics of the connections read from the kernel (TCP_INFO, Linux only)")
	fs.BoolVar(&o.addrsFile, "addrs-file", false, "[client mode] enable to pass a file including a pair of addresses and ports to an argument")
	fs.BoolVar(&o.enablePprof, "enable-pprof", false, "[client mode] enable pprof profiling")
	fs.StringVar(&o.pprofAddr, "pprof-addr", "localhost:6060", "[client mode] pprof listening address:port")

	// Server mode.
	fs.StringVar(&o.serveProtocol, "protocol", server.ProtocolAll, "[server mode] listening protocol ('tcp', 'udp' or 'all')")
	fs.StringVar(&o.listenAddrsFile, "listen-addrs-file", "", "[server mode] enable to pass a file including a pair of addresses and ports")

	fs.BoolVar(&o.showVersion, "version", false, "show version information")
	fs.BoolVarP(&o.showHelp, "help", "h", false, "show help information")
	return fs
}

// EnvName returns the name of the environment variable of an option.
func EnvName(option string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(option, "-", "_"))
}

// parse parses the command line args, then sets every option that args do
// not give and whose environment variable is set and not empty. It returns
// an error for the command line only: see options.envErrors.
func parse(args []string, lookupEnv func(string) (string, bool)) (*options, *pflag.FlagSet, error) {
	o := &options{}
	fs := newFlagSet(o)
	if err := fs.Parse(args); err != nil {
		return nil, fs, err
	}
	o.envErrors = applyEnv(fs, lookupEnv)
	o.args = fs.Args()
	return o, fs, nil
}

// applyEnv sets the options that the command line does not give from their
// environment variables, and returns the errors of the values it could not
// parse, by option name.
func applyEnv(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) map[string]error {
	var errs map[string]error
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Changed || noEnv[f.Name] {
			return
		}
		name := EnvName(f.Name)
		value, ok := lookupEnv(name)
		if !ok || value == "" {
			return
		}
		if err := fs.Set(f.Name, value); err != nil {
			if errs == nil {
				errs = make(map[string]error)
			}
			errs[f.Name] = fmt.Errorf("invalid %s %q: %w", name, value, err)
		}
	})
	return errs
}

func usage(w io.Writer, program string, fs *pflag.FlagSet) {
	fmt.Fprintf(w, "Usage: %s [OPTIONS] <addresses...>\n\n", program)
	fmt.Fprintf(w, "tcpulse is a concurrent TCP/UDP load generator that provides fine-grained, flow-level control\n\n")
	fmt.Fprintf(w, "Modes:\n")
	fmt.Fprintf(w, "  -c, --client    Run in client mode (connect to servers)\n")
	fmt.Fprintf(w, "  -s, --server    Run in server mode (accept connections)\n\n")
	fmt.Fprintf(w, "Options:\n")
	fmt.Fprint(w, fs.FlagUsages())
	fmt.Fprintf(w, "\nEnvironment:\n")
	fmt.Fprintf(w, "  Every option but --help and --version can also be set with an environment\n")
	fmt.Fprintf(w, "  variable: %s and the option name in upper case, with '-' replaced by '_'\n", EnvPrefix)
	fmt.Fprintf(w, "  (%s=1024). An option on the command line takes precedence.\n", EnvName("message-bytes"))
	fmt.Fprintf(w, "\nOutput Format (Client Mode):\n")
	fmt.Fprintf(w, "  CNT: Total number of messages/requests sent (not connections)\n")
	fmt.Fprintf(w, "  For persistent mode: CNT = rate × duration × connections\n")
	fmt.Fprintf(w, "  For ephemeral mode:  CNT = rate × duration\n\n")
	fmt.Fprintf(w, "Examples:\n")
	fmt.Fprintf(w, "  %s -s                          # Start server on default port 9100\n", program)
	fmt.Fprintf(w, "  %s -s 0.0.0.0:8080             # Start server on port 8080\n", program)
	fmt.Fprintf(w, "  %s -c localhost:9100           # Connect to server as client\n", program)
	fmt.Fprintf(w, "  %s -c --connections 50 host:port # Connect with 50 connections\n", program)
	fmt.Fprintf(w, "  %s -c --connections 1 --rate 1 --duration 1s host:port # Send exactly 1 message\n", program)
}
