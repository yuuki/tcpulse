# tcpulse

[![CI](https://github.com/yuuki/tcpulse/actions/workflows/ci.yml/badge.svg)](https://github.com/yuuki/tcpulse/actions/workflows/ci.yml)
[![Security](https://github.com/yuuki/tcpulse/actions/workflows/security.yml/badge.svg)](https://github.com/yuuki/tcpulse/actions/workflows/security.yml)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/yuuki/tcpulse)

tcpulse is a concurrent TCP/UDP load generator written in Go that provides fine-grained, flow-level control over connection establishment and data transfer.

## Table of Contents

- [What is tcpulse?](#what-is-tcpulse)
- [Why use tcpulse?](#why-use-tcpulse)
- [How it works](#how-it-works)
  - [Persistent Connections](#persistent-connections)
  - [Ephemeral Connections](#ephemeral-connections)
  - [Key Features](#key-features)
- [Installation](#installation)
  - [Pre-built Binaries](#pre-built-binaries)
  - [Container Image](#container-image)
  - [Build from Source](#build-from-source)
  - [Verify Installation](#verify-installation)
- [Usage](#usage)
  - [Environment Variables](#environment-variables)
- [Examples](#examples)
  - [Commands](#commands)
  - [Reports](#reports)
- [Use Cases](#use-cases)
  - [eBPF Kernel Tracer Overhead Measurement](#ebpf-kernel-tracer-overhead-measurement)
  - [L4 Load Balancer Distribution Verification](#l4-load-balancer-distribution-verification)
  - [Firewall/Conntrack Table Exhaustion Testing](#firewallconntrack-table-exhaustion-testing)
  - [UDP Packet Rate Tolerance (Real-time Delivery Simulation)](#udp-packet-rate-tolerance-real-time-delivery-simulation)
  - [Thread Pinning/IRQ Affinity Tuning Effect Measurement](#thread-pinningirq-affinity-tuning-effect-measurement)
  - [Multi-target Infrastructure Validation](#multi-target-infrastructure-validation)
  - [Usage Guidelines](#usage-guidelines)
- [Comparison with Other Tools](#comparison-with-other-tools)
  - [Bandwidth Measurement Tools](#bandwidth-measurement-tools)
  - [tcpulse's Unique Positioning](#tcpulses-unique-positioning)
  - [tcpulse's Limitations](#tcpulses-limitations)
- [Upgrading from 0.8](#upgrading-from-08)
- [Development](#development)
- [License](#license)

## What is tcpulse?

tcpulse is a specialized tool designed to generate load on network connections and analyze the performance characteristics of network traffic. It operates in two primary modes:

- **Server mode (`-s/--server`)**: Acts as an echo server that accepts TCP/UDP connections and echoes back received data
- **Client mode (`-c/--client`)**: Generates configurable load against target servers and measures connection performance metrics

## Why use tcpulse?

Network performance testing is crucial for:

- **Load testing**: Validate server capacity and identify bottlenecks before production deployment
- **Connection establishment performance**: Measure how quickly new connections can be established (ephemeral mode)
- **Sustained connection performance**: Test throughput and latency of long-lived connections (persistent mode)
- **Protocol comparison**: Compare TCP vs UDP performance characteristics
- **Infrastructure validation**: Verify network infrastructure can handle expected traffic patterns
- **Performance regression testing**: Detect performance degradations in network services

## How it works

tcpulse provides two distinct connection patterns to simulate real-world usage:

### Persistent Connections
Maintains long-lived connections and sends multiple requests per connection. This simulates applications like web services with connection pooling or persistent database connections.

### Ephemeral Connections
Creates new connections for each request, immediately closing them afterward. This simulates scenarios like HTTP/1.0 or testing connection establishment overhead.

### Key Features

- **Real-time metrics**: Latency percentiles (90th, 95th, 99th), throughput, and connection counts; minimum, maximum and mean are exact and percentiles within 0.8%, however long the run
- **Rate limiting**: Control connection establishment rate to simulate realistic load patterns
- **Multi-target support**: Test multiple endpoints simultaneously and aggregate results
- **Protocol support**: Both TCP and UDP with optimized implementations
- **Platform optimizations**: Leverages Linux-specific optimizations (TCP_FASTOPEN, SO_REUSEPORT) when available
- **JSON Lines output**: Machine-readable output format for integration with monitoring and analysis tools
- **Kernel TCP statistics**: Retransmissions, the kernel's smoothed RTT and TCP Fast Open use of the connections, with `--tcp-info` on Linux

## Installation

### Pre-built Binaries

Download the archive for your platform (Linux or macOS, amd64 or arm64) from the [GitHub Releases](https://github.com/yuuki/tcpulse/releases) page. Every archive and the checksums file have a signed [build provenance attestation](https://github.com/yuuki/tcpulse/attestations); verify a download with the [GitHub CLI](https://cli.github.com):

```bash
gh attestation verify tcpulse_<version>_linux_amd64.tar.gz --repo yuuki/tcpulse
```

or check it against `tcpulse_<version>_checksums.txt`:

```bash
sha256sum --ignore-missing -c tcpulse_<version>_checksums.txt
```

### Container Image

Multi-platform images (linux/amd64, linux/arm64) are published to the GitHub Container Registry, tagged with the version (`0.8.3`, `0.8`, `0`) and `latest`. They run as an unprivileged user.

```bash
docker run --rm -p 9100:9100/tcp -p 9100:9100/udp ghcr.io/yuuki/tcpulse:latest -s
docker run --rm ghcr.io/yuuki/tcpulse:latest -c --rate 1000 --duration 15s 192.0.2.10:9100
```

### Build from Source

Requirements:
- Go 1.27+ (see [go.mod](go.mod) for exact version requirements)
- Git

```bash
# Install to $GOPATH/bin
go install github.com/yuuki/tcpulse@latest

# Or clone the repository and build with make
git clone https://github.com/yuuki/tcpulse.git
cd tcpulse
make build
```

### Verify Installation

```bash
tcpulse --help
```

## Usage

```shell-session
$ tcpulse --help
Usage: tcpulse [OPTIONS] <addresses...>

tcpulse is a concurrent TCP/UDP load generator that provides fine-grained, flow-level control

Modes:
  -c, --client    Run in client mode (connect to servers)
  -s, --server    Run in server mode (accept connections)

Options:
      --addrs-file                 [client mode] enable to pass a file including a pair of addresses and ports to an argument
  -c, --client                     run in client mode
      --connections int32          [client mode] Number of concurrent connections to keep (only for 'persistent') (default 10)
      --duration duration          [client mode] measurement period (default 10s)
      --enable-pprof               [client mode] enable pprof profiling
      --flavor string              [client mode] connect behavior type 'persistent' or 'ephemeral' (default "persistent")
  -h, --help                       show help information
      --interval duration          [client mode] interval for printing stats (default 5s)
      --jsonlines                  [client mode] output results in JSON Lines format
      --listen-addrs-file string   [server mode] enable to pass a file including a pair of addresses and ports
      --merge-results-each-host    [client mode] merge results of each host (with --show-only-results)
      --message-bytes int32        [client mode] TCP/UDP message size (bytes) (default 64)
      --pprof-addr string          [client mode] pprof listening address:port (default "localhost:6060")
      --proto string               [client mode] protocol (tcp or udp) (default "tcp")
      --protocol string            [server mode] listening protocol ('tcp', 'udp' or 'all') (default "all")
      --rate int32                 [client mode] Message throughput per connection (/s) (for 'persistent' and UDP) or new connections throughput (/s) (for 'ephemeral'). Total messages (CNT) = rate * duration * connections (for 'persistent') or rate * duration (for 'ephemeral') (default 100)
  -s, --server                     run in server mode
      --show-only-results          [client mode] print only results of measurement stats
      --tcp-info                   [client mode] report TCP statistics of the connections read from the kernel (TCP_INFO, Linux only)
      --version                    show version information

Environment:
  Every option but --help and --version can also be set with an environment
  variable: TCPULSE_ and the option name in upper case, with '-' replaced by '_'
  (TCPULSE_MESSAGE_BYTES=1024). An option on the command line takes precedence.

Output Format (Client Mode):
  CNT: Total number of messages/requests sent (not connections)
  For persistent mode: CNT = rate × duration × connections
  For ephemeral mode:  CNT = rate × duration

Examples:
  tcpulse -s                          # Start server on default port 9100
  tcpulse -s 0.0.0.0:8080             # Start server on port 8080
  tcpulse -c localhost:9100           # Connect to server as client
  tcpulse -c --connections 50 host:port # Connect with 50 connections
  tcpulse -c --connections 1 --rate 1 --duration 1s host:port # Send exactly 1 message
```

### Environment Variables

Every option but `--help` and `--version` can also be set with an environment variable named `TCPULSE_` followed by the option name in upper case, with `-` replaced by `_`. An option given on the command line takes precedence, and an empty variable is ignored.

```shell-session
$ TCPULSE_CLIENT=true TCPULSE_RATE=1000 TCPULSE_MESSAGE_BYTES=1024 tcpulse 127.0.0.1:9100
```

## Examples

### Commands

Run as a server.

```shell-session
$ tcpulse -s 127.0.0.1:9100
```

Run as a client to put a load on the server.

```shell-session
$ tcpulse -c --flavor ephemeral --rate 1000 --duration 15s 127.0.0.1:9100
```

```shell-session
$ tcpulse -c --flavor persistent --connections 1000 --duration 15s 127.0.0.1:9100
```

Run as a UDP client.

```shell-session
$ tcpulse -c --proto udp --rate 1000 --duration 15s 127.0.0.1:9100
```

Output results in JSON Lines format for integration with monitoring tools.

```shell-session
$ tcpulse -c --jsonlines --rate 1000 --duration 10s 127.0.0.1:9100
{"peer":"127.0.0.1:9100","count":9998,"latency_max_us":2156,"latency_min_us":145,"latency_mean_us":234,"latency_90p_us":289,"latency_95p_us":321,"latency_99p_us":456,"rate_per_sec":999.8,"timestamp":"2025-01-07T10:30:00Z"}
```

### Reports

#### Standard Output Format

The standard output format includes the following columns:
- `CNT`: Total number of messages/requests sent (not connections)
- For persistent mode: CNT = rate × duration × connections
- For ephemeral mode: CNT = rate × duration
- `LAT_*`: Round-trip latencies in microseconds: from the write of a message to the read of its whole echo, including connection establishment in ephemeral mode. Minimum, maximum and mean are exact; percentiles are within 0.8% of the exact value.
- `RATE(/s)`: Round trips per second over the interval, or over the whole run (its `--duration`, or until it was interrupted)

Only completed round trips are counted: a request that failed in a way tcpulse tolerates (in ephemeral mode, a connection timeout, or a connection reset while the message is written or the echo read) or that was still in flight when the duration elapsed is not, so CNT can be slightly below rate × duration under load. A target with which no round trip completed makes tcpulse exit with status 1 after printing the report. Ctrl-C interrupts the requests in flight and prints the report. See [docs/design.md](docs/design.md) for what is measured and why.

```shell-session
$ tcpulse -c --proto tcp --flavor ephemeral --rate 1000 --duration 15s 10.0.150.2:9200 10.0.150.2:9300
PEER                 CNT        LAT_MAX(µs)     LAT_MIN(µs)     LAT_MEAN(µs)    LAT_90p(µs)     LAT_95p(µs)     LAT_99p(µs)     RATE(/s)
10.0.150.2:9200      4996       4108            212             367             446             492             773             999.00
10.0.150.2:9300      4999       10294           219             389             435             470             1595            999.40
10.0.150.2:9200      4998       3884            209             369             448             489             950             999.40
10.0.150.2:9300      4998       3057            220             356             426             473             1030            999.40
10.0.150.2:9200      4802       22838           219             784             1030            2692            10264           0.00
10.0.150.2:9300      4808       18730           219             801             1033            2382            13412           0.00
--- A result during total execution time ---
10.0.150.2:9200      14799      13736           223             530             501             953             5989            996.00
10.0.150.2:9300      14809      18023           212             542             492             1110            5849            996.47
```

#### JSON Lines Output Format

```shell-session
$ tcpulse -c --jsonlines --proto tcp --flavor ephemeral --rate 1000 --duration 15s 10.0.150.2:9200 10.0.150.2:9300
{"peer":"10.0.150.2:9200","count":14799,"latency_max_us":13736,"latency_min_us":223,"latency_mean_us":530,"latency_90p_us":501,"latency_95p_us":953,"latency_99p_us":5989,"rate_per_sec":996.0,"timestamp":"2025-01-07T10:30:00Z"}
{"peer":"10.0.150.2:9300","count":14809,"latency_max_us":18023,"latency_min_us":212,"latency_mean_us":542,"latency_90p_us":492,"latency_95p_us":1110,"latency_99p_us":5849,"rate_per_sec":996.47,"timestamp":"2025-01-07T10:30:00Z"}
```

The JSON Lines format includes the following fields:
- `peer`: Target server address
- `count`: Total number of messages/requests sent (CNT in standard output)
- `latency_max_us`: Maximum latency in microseconds
- `latency_min_us`: Minimum latency in microseconds
- `latency_mean_us`: Mean latency in microseconds
- `latency_90p_us`: 90th percentile latency in microseconds
- `latency_95p_us`: 95th percentile latency in microseconds
- `latency_99p_us`: 99th percentile latency in microseconds
- `rate_per_sec`: Average request rate per second
- `timestamp`: ISO 8601 timestamp when the measurement was completed
- `tcp_info`: Only with `--tcp-info`: the TCP statistics of the connections (see below)

#### TCP Statistics (`--tcp-info`)

On Linux, `--tcp-info` reads what the kernel reports of every TCP connection (`getsockopt(TCP_INFO)`) right before tcpulse closes it, once the clock of its last round trip has stopped. The report then includes the TCP statistics of the connections of the whole run. It needs no privileges. It is refused with `--proto udp` and on other platforms. Without it, the output is unchanged.

```shell-session
$ tcpulse -c --show-only-results --tcp-info --flavor ephemeral --rate 1000 --duration 15s 127.0.0.1:9100
PEER                 CNT        LAT_MAX(µs)     LAT_MIN(µs)     LAT_MEAN(µs)    LAT_90p(µs)     LAT_95p(µs)     LAT_99p(µs)     RATE(/s)
--- A result during total execution time ---
127.0.0.1:9100       14982      20537           63              294             380             425             749             998.79
--- TCP statistics of the connections (TCP_INFO) ---
PEER                 CONNS      RETRANS_CONNS   RETRANS_SEGS    SEGS_OUT        SRTT_MEAN(µs)   SRTT_99p(µs)    SRTT_MAX(µs)    TFO_CONNS
127.0.0.1:9100       14982      0               0               59928           14              40              1862            0
```

- `CONNS`: The connections whose TCP_INFO was read: every connection that was opened, including those whose request failed or was cut off by the end of the run. A connection that could not be dialed has no socket to read and is not counted.
- `RETRANS_CONNS`: The connections that retransmitted at least one segment
- `RETRANS_SEGS`: The segments retransmitted, SYNs included (`tcpi_total_retrans`)
- `SEGS_OUT`: The segments sent, retransmissions included (`tcpi_segs_out`, Linux 4.2+; it reads 0 on older kernels). SYNs and pure ACKs count too, as in the kernel's `OutSegs`, so `RETRANS_SEGS / SEGS_OUT` is lower than a rate over data segments.
- `SRTT_*`: The kernel's smoothed round-trip time (`tcpi_rtt`), in microseconds: the mean, the 99th percentile (within 0.8%) and the maximum over the connections that have an RTT sample. A connection may have none, for instance when its SYN, sent with Fast Open, was never answered. They are 0 when no connection has one.
- `TFO_CONNS`: The connections whose data in the SYN was acknowledged by TCP Fast Open (`TCPI_OPT_SYN_DATA`). The client side is on by default (`net.ipv4.tcp_fastopen=1`); the server side also needs the server bit (`3`). The tcpulse server accepts one pending Fast Open request per listener at a time, so concurrent handshakes beyond it fall back to a plain handshake.

The kernel's RTT leaves out the time spent in the client's process (system calls, the Go runtime), which `LAT_*` includes. In ephemeral mode, a round trip also includes the handshake, about one more RTT, unless the message travels in the SYN with Fast Open.

Retransmissions have two common causes, which these counters alone do not tell apart:

- **Lost segments, SYNs in particular:** a lost SYN is retransmitted after a timeout of at least 1s, so in ephemeral mode SYN loss shows as steps of whole seconds in the latencies (`LAT_MAX`, `LAT_99p`). The steps depend on the kernel: 1s, 2s, 3s, 4s with `net.ipv4.tcp_syn_linear_timeouts` (1s between the first retransmissions, 4 of them by default), 1s, 3s, 7s on kernels that double the timeout from the first one.
- **TCP Fast Open falling back:** when the server does not accept the data sent in a SYN with a cookie (it no longer enables Fast Open, or its queue of pending Fast Open requests is full), the client sends the data again right after the handshake. That counts as one retransmission per connection, without loss and without the delay of a timeout.

The host's kernel counters `TCPSynRetrans` and `TCPFastOpenActiveFail` (`nstat`, or `/proc/net/netstat`) separate the two.

Interval lines do not include these statistics: a persistent connection is read only when it ends. With `--jsonlines`, each object ends with a `tcp_info` object:

```json
{"peer":"127.0.0.1:9100","count":14982,"latency_max_us":20537,"latency_min_us":63,"latency_mean_us":294,"latency_90p_us":380,"latency_95p_us":425,"latency_99p_us":749,"rate_per_sec":998.79,"timestamp":"2026-11-08T08:40:00Z","tcp_info":{"connections":14982,"retrans_connections":0,"retrans_segments":0,"segments_out":59928,"srtt_mean_us":14,"srtt_99p_us":40,"srtt_max_us":1862,"fastopen_connections":0}}
```

## Use Cases

tcpulse is particularly well-suited for performance validation of network devices and middlewares that relay end-to-end communications. Below are typical experimental scenarios that can be completed with tcpulse alone, which would be difficult to reproduce with iperf/netperf.

### eBPF Kernel Tracer Overhead Measurement

Quantify additional latency in the kernel TCP stack when numerous kprobe/tracepoints are active. Use persistent mode to maintain 10k connections and continuously echo for 1 minute, observing how many microseconds the `p99_latency` increases when eBPF is enabled vs disabled.

```bash
tcpulse -c --flavor persistent --connections 10000 --duration 60s 10.0.0.2:9100
```

### L4 Load Balancer Distribution Verification

Verify that an L4 LB can evenly distribute connections across N backends. Generate 10k CPS (connections per second) using ephemeral mode against the L4 LB's VIP (Virtual IP) address, while collecting connection counts on the backend side to calculate standard deviation.

```bash
tcpulse -c --flavor ephemeral --rate 10000 --duration 30s 10.0.0.2:9100
```

### Firewall/Conntrack Table Exhaustion Testing

Investigate the "new connections per second capacity" of stateful FW/NAT and behavior when tables are exhausted. Place tcpulse client behind NAT, execute ephemeral mode at 40k CPS for 2 minutes, while simultaneously collecting NAT router memory usage and packet loss statistics.

```bash
tcpulse -c --flavor ephemeral --rate 40000 --duration 120s 192.0.2.10:9100
```

### UDP Packet Rate Tolerance (Real-time Delivery Simulation)

Verify that QoS policing and NIC interrupt tuning don't break under VoIP/Gaming-style traffic. Execute UDP at 25k * 2 pps for 10 minutes with 64-byte payload fixed to observe jitter.

```bash
tcpulse -c --proto udp --rate 25000 --duration 10m --message-bytes 64 198.51.100.5:9100
```

### Thread Pinning/IRQ Affinity Tuning Effect Measurement

Measure how throughput and latency change when NIC interrupts are pinned to specific CPU cores. Use persistent 5k connections, measuring continuously for 5 minutes before and after affinity changes.

```bash
tcpulse -c --flavor persistent --connections 5000 --duration 300s 10.1.1.50:9100
```

### Multi-target Infrastructure Validation

Test multiple endpoints simultaneously to validate distributed system performance under realistic load patterns:

```bash
# Test multiple services with different load patterns
tcpulse -c --flavor ephemeral --rate 5000 --duration 60s \
  service1.example.com:9100 \
  service2.example.com:9200 \
  service3.example.com:9300
```

### Usage Guidelines

| Requirement | tcpulse | iperf3 / netperf |
|---|---|---|
| **Fixed CPS/pps Control** | ✅ | △ (coarse with `-b` only) |
| **Maintain 10k+ Concurrent Flows** | ✅ (`persistent`) | △ (`-P` limit ~128) |
| **Multi-target Support** | ✅ (`--addrs-file`) | ❌ |
| **Automatic Percentile Statistics (p95/p99)** | ✅ | △ (netperf: mean/max only) |
| **Integrated Control & Measurement** | ✅ | ✅/○ |

## Comparison with Other Tools

Network performance testing and load generation tools serve different purposes. Here's how tcpulse compares to popular alternatives:

### Bandwidth Measurement Tools

**[iperf3](https://iperf.fr/)** is one of the most widely used network performance measurement tools. It supports both TCP and UDP protocols and provides JSON output and multi-stream capabilities. Its primary purpose is **measuring maximum network bandwidth** - answering "What's the maximum Mbps this network can achieve?" While UDP mode allows coarse message rate control via the `-b` option, and multi-target testing can be achieved through external script parallelization, the tool is fundamentally designed for 1-to-1 communication.

**[netperf](https://github.com/HewlettPackard/netperf)** is a comprehensive network performance benchmark tool that targets both bulk data transfer and request/response performance. It offers various test types including TCP_STREAM, UDP_STREAM, TCP_RR, and UDP_RR with extensive parameter tuning capabilities. The `*_RR` tests provide microsecond-level latency statistics (min/mean/p90/p99, etc.) as standard output, emphasizing not just throughput but also latency measurement. It excels in network equipment performance evaluation and comparing different network configurations.

**[nuttcp](https://nuttcp.net/)** specializes in high-speed UDP testing with features like burst mode and CPU core selection, making it suitable for performance measurement of networks exceeding 10Gbps.

### tcpulse's Unique Positioning

**tcpulse** is designed to answer "How does the network or application behave under specified load conditions?" Specifically:

**Load Pattern Control**: Ephemeral mode precisely controls new connection generation rates, while persistent mode controls concurrent connection counts. This enables analysis like "CPU usage changes when applying 1000 connections/second load for 30 minutes." While iperf3's UDP mode allows coarse control via bitrate specification, precise connection generation rate control for TCP or long-term precise load maintenance is challenging with existing tools.

**Experiment Reproducibility**: Running with the same parameters reproduces identical load patterns, making it effective for comparative experiments under different conditions and problem reproduction.

**Distributed System Support**: Provides multi-server simultaneous load generation capability within a single tool. Existing tools often require external script-based parallel execution.

These characteristics make tcpulse particularly suitable for **operational validation under realistic load conditions** rather than exploring system performance limits.

### tcpulse's Limitations

However, tcpulse has certain limitations:

**Not Suitable for Maximum Throughput Measurement**: For pure bandwidth measurement like "What's the maximum Gbps this network can achieve?", iperf3 is more appropriate. tcpulse focuses on load control and isn't optimized for maximum performance exploration.

**Overkill for Health Monitoring**: For simply checking if a server is alive or measuring basic latency, ping or curl is sufficient. tcpulse's multi-functionality can be overly complex for simple tests.

**No High-Layer Protocol Support**: Doesn't directly support L7 protocols like HTTP, HTTP/2, or QUIC. Verifying protocol-specific performance characteristics requires dedicated tools or libraries (e.g. [wrk](https://github.com/wg/wrk), [vegeta](https://github.com/tsenart/vegeta), [hey](https://github.com/rakyll/hey), etc.).

**Limited Network Diagnostic Features**: Detailed packet content analysis or network path problem identification requires separate packet capture tools like [Wireshark](https://www.wireshark.org/) or [tcpdump](https://www.tcpdump.org/).

## Upgrading from 0.8

tcpulse 0.9 keeps the options, the environment variables and the output formats of 0.8, but:

- **Container image**: it runs as the unprivileged user `nobody` (not root), and the binary is `/usr/local/bin/tcpulse` (the entrypoint was `./tcpulse` in `/`). A command or entrypoint that names `./tcpulse`, a server binding a port below 1024 with `--network host`, or an address file only root can read needs changes, or `--user 0`. The image is published for linux/arm64 too.
- **Measurements**: minimum, maximum and mean are exact and percentiles within 0.8% (0.8 sampled them from a reservoir of 1028 round trips), round trips include the whole echo of large messages, failed and cut-off requests are not counted, and the rate of the run is over its duration, so the numbers differ from those of 0.8 for the same load.
- **Exit status**: a request still in flight when the duration elapses no longer fails the run (0.8 exited 1 with an I/O timeout and printed no report, after a single lost UDP datagram for instance); a target with which no round trip completed exits 1 after the report is printed. Options that cannot generate load (`--rate 0`, which panicked, a run of less than one message, `--protocol` other than `tcp`, `udp` or `all`, which exited 0 at once) exit 1. A command line that cannot be parsed exits 2 and prints a hint instead of the list of options.
- **Environment variables**: values are parsed like the flags, more strictly than 0.8: `TCPULSE_RATE=10.0` and a duration without a unit (`TCPULSE_DURATION=10`, which 0.8 read as 10ns) are errors (exit 2), and so is an unparsable value, which 0.8 read as zero. A variable of the other mode (`TCPULSE_INTERVAL` for a server) is ignored.
- **Help**: `--help` prints to stdout.

## Development

`make check` runs the linters, the workflow audit, `go vet`, the race-enabled tests and the build; `make help` lists the other targets. See [CONTRIBUTING.md](CONTRIBUTING.md), [AGENTS.md](AGENTS.md) for the conventions and compatibility contracts, and [SECURITY.md](SECURITY.md) to report a vulnerability.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
