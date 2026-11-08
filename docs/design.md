# tcpulse design

This document describes how tcpulse generates load, what it measures and
why. The [README](../README.md) describes how to use it.

## 1. Goals

tcpulse answers "how does this network, middlebox or service behave under
this load?" rather than "how fast can it go?". It keeps a load pattern
fixed (a number of concurrent connections, a rate of new connections or of
datagrams) for a period and reports the round-trip latencies it observed.

- **The load is what the user asked for.** A run sends rate × duration
  messages (× connections in persistent mode), paced evenly, whatever the
  latencies are.
- **Every reported number is a completed round trip.** A request that
  failed, or that the end of the run cut off, is not a sample.
- **Same options, same load.** Runs are reproducible, so that two
  configurations of the system under test can be compared.

Non-goals: maximum throughput measurement (iperf3), L7 protocols, and
diagnosing where packets are lost.

## 2. Architecture

```
main.go          process wiring: arguments, environment, signals, version
internal/cli     flags and TCPULSE_* variables, validation, the two modes
internal/client  load generators: TCP persistent, TCP ephemeral, UDP
internal/server  echo server
internal/stats   latency histogram, per-peer recorders
internal/report  text and JSON Lines output
internal/sockopt Linux socket options (no-ops elsewhere)
```

`main.go` stays at the root of the module so that
`go install github.com/yuuki/tcpulse@latest` builds the command. Nothing
keeps state in package variables: the configuration and the recorders of a
run are passed down explicitly, so tests can run several clients and servers
in one process.

## 3. Client

### 3.1 Load patterns

| Protocol, flavor | Sockets | Messages |
| --- | --- | --- |
| TCP, `persistent` | `--connections` connections, open for the whole run | `--rate` per second on each connection |
| TCP, `ephemeral` | a new connection per message, closed after its echo | `--rate` per second |
| UDP | a new socket per datagram | `--rate` per second |

Each target address gets its own load: `tcpulse -c a b` puts the full load
on both `a` and `b`.

The number of messages is rate × duration, rounded down, per connection in
persistent mode (`messages()`): `--rate 3 --duration 1500ms` sends 4. The
run ends when they are all answered or when the duration has elapsed,
whichever comes first.

Messages are paced with `go.uber.org/ratelimit`: evenly spaced, with a slack
of 10 that lets a sender delayed by a slow round trip or a GC pause catch up
on missed slots, so that the average rate holds. Waiting for a slot can be
canceled (Ctrl-C, end of the run).

In persistent mode a connection sends its next message only after the echo
of the previous one: a round trip longer than 1/rate lowers the rate of that
connection. Ephemeral and UDP requests run concurrently, so their rate does
not depend on latency.

### 3.2 What is timed

The clock starts after the payload is generated (random bytes, so that no
middlebox can compress or deduplicate them) and stops when the whole echo
has been read:

- persistent: write the message, read the whole echo (`io.ReadFull`: the
  echo of a message larger than a segment arrives in several reads);
- ephemeral: dial (the TCP handshake, or with TCP Fast Open the SYN that
  carries the data), write, read the whole echo, so the latency includes
  connection establishment;
- UDP: create the socket, write the datagram, read the echo.

### 3.3 End of the run and failures

The connections take the deadline of the run, so blocked I/O returns when
the duration elapses. A request that the end of the run cuts off (the run's
context is done, or the error is a timeout and the deadline has passed:
`cutOff`) is neither recorded nor an error. This is also what happens to a
lost UDP datagram: the client waits for its echo until the end of the run.
CNT can therefore be slightly below rate × duration.

In ephemeral mode a dial that times out (`ETIMEDOUT`), a write still in
progress (`EINPROGRESS`) and a reset while reading the echo (`ECONNRESET`)
are logged and tolerated (`errTolerated`): the run goes on, and the request
is not recorded. These are the failures of overloaded servers, load
balancers and conntrack tables, which tcpulse is used to provoke. Any other
error (connection refused, a persistent connection closed by the server)
stops the run with exit status 1.

## 4. Statistics

`internal/stats.Histogram` counts durations in log-linear buckets: every
power of two of nanoseconds is split into 128 buckets. Its memory is bounded
(at most 57×128 counters, allocated as larger values arrive) however long
the run, and:

- the count, minimum, maximum and mean are exact;
- a percentile is the nearest-rank value (the smallest recorded duration
  that at least p×count durations do not exceed), rounded up to the end of
  its bucket and clamped to [min, max]: at most 0.8% above the exact value,
  exact below 128ns. `FuzzQuantile` checks the bound.

A `Recorder` holds two histograms per peer: one for the whole run and one
for the current interval, swapped under a lock when an interval line is
printed, so no sample is lost between intervals. RATE(/s) is the count
divided by the length of the period: the interval, or the run from its
start to the report. With `--merge-results-each-host` all peers share a
single recorder.

tcpulse 0.8 used `github.com/rcrowley/go-metrics`, now archived, whose
timers drew the minimum, maximum, mean and percentiles from a 1028-sample
exponentially decaying reservoir, and the rate from a meter updated every 5
seconds (a fresh timer printed 0.00).

## 5. Server

The server writes back every TCP byte stream and every UDP datagram it
receives. It listens on each address for TCP, UDP (`udp4`) or both
(`--protocol all`, the default).

- TCP: one goroutine per connection, reading into a 4 KiB buffer and
  writing back what it read. A client closing or resetting its connection
  is a normal end.
- UDP: one reader per socket, with a buffer that holds the largest IPv4
  payload (65507 bytes), and one goroutine per echo.
- Accept errors that can go away (out of file descriptors or memory, a
  connection aborted in the backlog, timeouts) are retried after 5ms,
  doubling up to 1s, like `net/http`; other accept errors stop the server.
  No error of a single connection stops it.
- On SIGINT or SIGTERM it closes its listeners and connections and waits
  for its goroutines.

UDP is IPv4 only on both sides: unlike TCP, a UDP client cannot fall back
from `::1` to `127.0.0.1`, so `localhost` would fail against the default
`0.0.0.0:9100`.

## 6. Socket options (Linux)

| Option | Where | Why |
| --- | --- | --- |
| `SO_REUSEPORT` | listening and client sockets | several servers can share a port |
| `TCP_FASTOPEN_CONNECT` | client sockets | the first message travels in the SYN when the server allows it |
| `TCP_FASTOPEN` (queue of 1) | listening sockets | accept data in the SYN (with `net.ipv4.tcp_fastopen` enabling the server side) |
| `TCP_QUICKACK` | ephemeral connections, accepted connections | acknowledge at once instead of delaying |

They are optimizations: a kernel that refuses one gets a warning logged once
per option, and the connection goes on. Other platforms set none.

## 7. Configuration

Options are POSIX-style flags (`spf13/pflag`, kept on 1.0, whose help text
is that of tcpulse 0.8). Every option but `--help` and `--version` can also
come from `TCPULSE_<OPTION>` (upper case, `-` replaced by `_`); a flag on the
command line wins, an empty variable is ignored, an invalid one is an error.

Validation rejects any option set the run cannot work with: a rate below 1
(the rate limiter would divide by zero), an interval or a duration that is
not positive, an empty message, no connection in persistent mode, a UDP
message above 65507 bytes, an empty addresses file, an unknown protocol or
flavor. `FuzzParse` checks that whatever validation accepts can generate
load.

Exit statuses: 0 on success (also after Ctrl-C, which prints the report),
1 for invalid options and run errors, 2 for a command line that cannot be
parsed. A second SIGINT or SIGTERM terminates tcpulse at once.

The Go runtime raises the soft limit of open files to the hard limit at
startup (to `kern.maxfilesperproc` on macOS), which the thousands of
sockets of a run need; tcpulse does not change it further.

## 8. Output

The text output prints a header, an interval line per peer every
`--interval` (unless `--show-only-results` or `--jsonlines`), then a
separator and a line per peer (or a single merged line) for the whole run.
`--jsonlines` prints only the latter, one JSON object per line. Latencies
are whole microseconds, truncated. The column layout and the JSON field
names and order are a public format, pinned by golden tests in
`internal/report`.

## 9. Release

tagpr keeps a release pull request up to date on `main` (CHANGELOG.md and
the version in `version.go`). Merging it tags `vX.Y.Z` and calls the
release workflow, which runs CI on the tagged commit, then GoReleaser:

- archives `tcpulse_<version>_<os>_<arch>.tar.gz` for Linux and macOS on
  amd64 and arm64, built with `-trimpath` and commit timestamps, and
  `tcpulse_<version>_checksums.txt`;
- a linux/amd64 and linux/arm64 image `ghcr.io/yuuki/tcpulse` tagged
  `X.Y.Z`, `X.Y`, `X` and `latest`, assembled from the same binaries
  (Alpine, running as `nobody`);
- a signed build provenance attestation of the archives and the checksums
  file.

The CI snapshot job builds the same archives and image on every change
without publishing them.
