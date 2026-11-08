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
internal/sockopt Linux socket options and TCP_INFO (no-ops or unsupported elsewhere)
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
on both `a` and `b`. An address given twice gets the load twice and two
report lines, each counting its own round trips.

The number of messages is rate × duration, rounded down, per connection in
persistent mode (`client.Messages`): `--rate 3 --duration 1500ms` sends 4.
The run ends when they are all answered or when the duration has elapsed,
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
has been read, before the connection is closed:

- persistent: write the message, read the whole echo (`io.ReadFull`: the
  echo of a message larger than a segment arrives in several reads);
- ephemeral: dial (the TCP handshake, or with TCP Fast Open the SYN that
  carries the data), write, read the whole echo, so the latency includes
  connection establishment;
- UDP: create the socket, write the datagram, read the echo.

Ephemeral and UDP requests dial the address as given, so with a host name
every request resolves it (which DNS-based load balancing relies on) and
the latency includes the lookup; give an IP address to leave it out.

The echo of a TCP message larger than 64 KiB is read while the message is
written: the server writes back as it reads, so a message that does not fit
in the socket buffers of both sides would otherwise block the client and
the server on each other.

### 3.3 End of the run and failures

The I/O of every connection gives up when the run ends (`bindToRun`): at
its deadline, or at once when it is canceled (Ctrl-C, SIGTERM, another
target failing). A request that the end of the run cuts off (the run's
context is done, or the error is a timeout and the deadline has passed:
`cutOff`) is neither recorded nor an error. This is also what happens to a
lost UDP datagram: the client waits for its echo until the end of the run.
CNT can therefore be slightly below rate × duration.

In ephemeral mode a dial that times out (`ETIMEDOUT`), a write still in
progress (`EINPROGRESS`) and a connection reset or closed by the peer while
the message is written or the echo read (`ECONNRESET`, `EPIPE`) are logged
and tolerated (`errTolerated`): the run goes on, and the request is not
recorded. These are the failures of overloaded servers, load balancers and
conntrack tables, which tcpulse is used to provoke. Any other error
(connection refused, a persistent connection closed by the server) stops
the run with exit status 1.

A target with which no round trip completed (it never answers, or every
request failed in a tolerated way) fails the run too: tcpulse prints the
report, then exits 1 naming the target.

### 3.4 TCP statistics

With `--tcp-info` (Linux only), the client reads `getsockopt(TCP_INFO)` of
every TCP connection right before closing it. It reads only when asked, so a
run without the option makes no extra system call. It reads after the clock
of the connection's last round trip has stopped, so what is timed does not
change: in ephemeral mode, the deferred close runs after the latency is
computed.

- **Every opened connection is read,** whatever became of its requests
  (failed in a tolerated way, cut off by the end of the run):
  retransmissions matter most there. A dial that fails leaves no socket to
  read. A connection whose TCP_INFO cannot be read is left out, and a
  warning is logged once per target.
- **Per target,** or for all targets with `--merge-results-each-host`, the
  `Recorder` counts:
  - the connections, and those that retransmitted;
  - the segments retransmitted (`tcpi_total_retrans`, SYNs included) and
    sent (`tcpi_segs_out`, Linux 4.2+: an older kernel returns a shorter
    struct and it reads 0);
  - the connections whose SYN data was acknowledged (`TCPI_OPT_SYN_DATA`);
  - the kernel's smoothed RTTs (`tcpi_rtt`), in a `Histogram`. A connection
    without an RTT sample (with Fast Open, a SYN never answered) has no RTT,
    not an RTT of 0, so it is left out.
- **Whole run only:** the statistics cover the whole run, not each interval.
  A persistent connection is read when it ends, so an interval would see
  none of it.

The kernel's RTT runs from a segment to its acknowledgement. It leaves out
the time spent in the client's process (system calls, the Go runtime), which
`LAT_*` includes; in ephemeral mode a round trip also includes the
handshake, unless the message travels in the SYN with Fast Open.

The client sets `TCP_FASTOPEN_CONNECT`, so it sends its first message in the
SYN when it holds a cookie for the server. When the server does not accept
that data (it no longer enables Fast Open, or its queue of pending Fast Open
requests is full: the tcpulse server keeps a queue of one, see §6), the
client sends the data again right after the handshake, and it counts as a
retransmission. So do lost SYNs, which are retransmitted after a timeout of
at least 1s: the counters alone do not tell the two apart. The latencies do
(steps of whole seconds for SYN loss: 1s, 2s, 3s with
`net.ipv4.tcp_syn_linear_timeouts`, 1s, 3s, 7s where the timeout doubles),
and so do the host's counters `TCPSynRetrans` and `TCPFastOpenActiveFail`.

`tcpi_fastopen_client_fail`, which says why Fast Open failed, is left out:
`golang.org/x/sys/unix.TCPInfo` leaves its byte out as padding, and it needs
Linux 5.5+. The kernel's counters are 32 bits per connection: a persistent
connection that sends more than 2³² segments wraps them.

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

A `Recorder` holds two histograms per target: one for the whole run and one
for the current interval, swapped under a lock when an interval line is
printed, so no sample is lost between intervals. RATE(/s) is the count
divided by the length of the period: the interval, or the run. The run
lasts `--duration` even when its last echo comes back earlier (up to
1/rate before the end), or until it stopped when it was interrupted. With
`--merge-results-each-host` all targets share a single recorder.

tcpulse 0.8 used `github.com/rcrowley/go-metrics`, now archived, whose
timers drew the minimum, maximum, mean and percentiles from a 1028-sample
exponentially decaying reservoir, and divided the count of the run by the
time from its first round trip to the report, which inflated the rate of
short runs.

## 5. Server

The server writes back every TCP byte stream and every UDP datagram it
receives. It listens on each address for TCP, UDP (`udp4`) or both
(`--protocol all`, the default).

- TCP: one goroutine per connection, reading into a 4 KiB buffer and
  writing back what it read. A client closing or resetting its connection
  is a normal end.
- UDP: one reader per socket, with a buffer that holds the largest IPv4
  payload (65507 bytes), and one goroutine per echo.
- Accept errors that can go away (out of file descriptors or memory, the
  network errors of the connection being accepted that accept(2) says to
  retry, timeouts) are retried after 5ms, doubling up to 1s, like
  `net/http`. Another accept error closes the listener's connections and
  stops the server with exit status 1. No error of a single connection
  stops it.
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
command line wins, and an empty variable is ignored. Values are parsed like
the flags, more strictly than tcpulse 0.8 (whose viper read `10.0` as 10,
`10` as a duration of 10ns, and garbage as 0). A value that cannot be parsed
is an error when its option belongs to the mode that runs, and is ignored
otherwise, as is everything for `--help` and `--version`.

Validation rejects any option set the run cannot work with: a rate below 1
(the rate limiter would divide by zero), a duration that is not positive, a
rate × duration below one message or beyond an int64, an interval that is
not positive when interval lines are printed, an empty message, no
connection in persistent mode, a UDP message above 65507 bytes, an empty
addresses file, an unknown protocol or flavor, `--tcp-info` with UDP or on a
platform other than Linux. `FuzzParse` checks the options validation accepts
against an independent computation of the message count.

Exit statuses: 0 on success (also after Ctrl-C, which interrupts the
requests in flight and prints the report), 1 for invalid options, run
errors and a target without a round trip, 2 for a command line or an
environment variable that cannot be parsed. A second SIGINT or SIGTERM
terminates tcpulse at once.

The Go runtime raises the soft limit of open files to the hard limit at
startup (to `kern.maxfilesperproc` on macOS), which the thousands of
sockets of a run need; tcpulse does not change it further.

## 8. Output

The text output prints a header, an interval line per peer every
`--interval` (unless `--show-only-results` or `--jsonlines`), then a
separator and a line per peer (or a single merged line) for the whole run.
`--jsonlines` prints only the latter, one JSON object per line. Latencies
are whole microseconds, truncated. With `--tcp-info`, the text report ends
with a section of its own: a separator, a header, and a line per peer (or a
single merged line) of TCP statistics. Each JSON object ends with a
`tcp_info` object. Without the option, the output is unchanged. The column
layout and the JSON field names and order are a public format, pinned by
golden tests in `internal/report`.

## 9. Release

tagpr keeps a release pull request up to date on `main` (CHANGELOG.md and
the version in `version.go`); it bumps the patch version unless a merged
pull request has the label `minor` or `major`. Merging it tags `vX.Y.Z` and
calls the release workflow, which runs CI on the tagged commit (without
fuzzing and the online workflow audits, which can fail without a change of
the code), then GoReleaser:

- archives `tcpulse_<version>_<os>_<arch>.tar.gz` for Linux and macOS on
  amd64 and arm64, built with `-trimpath` and commit timestamps, and
  `tcpulse_<version>_checksums.txt`;
- a linux/amd64 and linux/arm64 image `ghcr.io/yuuki/tcpulse` tagged
  `X.Y.Z`, `X.Y`, `X` and `latest`, assembled from the same binaries
  (Alpine, running as `nobody`);
- a signed build provenance attestation of the archives and the checksums
  file.

A pre-release tag pushed by hand (`vX.Y.Z-rc.N`) is released the same way,
marked as a pre-release and with only its own image tag. Running the
release workflow by hand on an existing tag releases it again, replacing
the assets a failed run already uploaded (the archives are reproducible).
The CI snapshot job builds the same archives and image on every change
without publishing them.
