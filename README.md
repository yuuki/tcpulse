# tcpulse

[![Test](https://github.com/yuuki/tcpulse/actions/workflows/test.yml/badge.svg)](https://github.com/yuuki/tcpulse/actions/workflows/test.yml)
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
  - [Build from Source](#build-from-source)
  - [Verify Installation](#verify-installation)
- [Usage](#usage)
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

- **Real-time metrics**: Latency percentiles (90th, 95th, 99th), throughput, and connection counts
- **Rate limiting**: Control connection establishment rate to simulate realistic load patterns
- **Multi-target support**: Test multiple endpoints simultaneously and aggregate results
- **Protocol support**: Both TCP and UDP with optimized implementations
- **Platform optimizations**: Leverages Linux-specific optimizations (TCP_FASTOPEN, SO_REUSEPORT) when available
- **JSON Lines output**: Machine-readable output format for integration with monitoring and analysis tools

## Installation

### Pre-built Binaries

Download the latest pre-built binaries from the [GitHub Releases](https://github.com/yuuki/tcpulse/releases) page.

### Build from Source

Requirements:
- Go 1.22+ (see [go.mod](go.mod) for exact version requirements)
- Git

```bash
# Clone the repository
git clone https://github.com/yuuki/tcpulse.git
cd tcpulse

# Build using Make
make build

# Or build directly with Go
go build -o tcpulse .

# Install to $GOPATH/bin
go install .
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
      --protocol string            [server mode] listening protocol ('tcp' or 'udp') (default "all")
      --rate int32                 [client mode] Message throughput per connection (/s) (for 'persistent' and UDP) or new connections throughput (/s) (for 'ephemeral'). Total messages (CNT) = rate * duration * connections (for 'persistent') or rate * duration (for 'ephemeral') (default 100)
  -s, --server                     run in server mode
      --show-only-results          [client mode] print only results of measurement stats
      --version                    show version information

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

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
