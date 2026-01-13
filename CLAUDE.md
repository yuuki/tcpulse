# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

tcpulse is a TCP/UDP connection performance load generator written in Go. It provides two primary modes:
- **serve**: Acts as a server accepting TCP/UDP connections and echoing back data
- **connect**: Acts as a client generating load against target servers

## Build Commands

```bash
# Build the binary (includes vet and staticcheck)
make build

# Build Docker image
make docker/build

# Run all tests (includes vet and staticcheck)
make test

# Run tests without linting
go test ./...

# Run tests with race detector
go test -race ./...

# Run specific test
go test -run TestName ./...

# Run tests with verbose output
go test -v ./...

# Run linting tools
make vet          # go vet
make staticcheck  # staticcheck
make revive       # revive linter
```

## Development Workflow with GitHub

1. Create a new branch for [feature/bug/refactor-name] from main branch.
2. Coding the feature/bug.
3. Add or fix tests.
4. Check the code quality with testing, linting and formatting.
5. Create a pull request.
6. Review the pull request and fix the comments.
7. Merge the pull request.

## Architecture

The project follows a simple flat structure with all Go files in the top-level directory:

- `main.go`: Entry point with flag-based CLI using pflag and viper (-c for client, -s for server)
- `server.go`: TCP/UDP server logic with context-based cancellation and errgroup for concurrent handling
- `client.go`: Client implementation with persistent/ephemeral connection modes, rate limiting, and latency measurements
- `socket_option.go`: Default socket option implementations (non-Linux platforms)
- `socket_option_linux.go`: Linux-specific socket optimizations using build tags
- `printer.go`: Utility functions for formatting and printing output/metrics
- `version.go`: Version information

## Key Design Patterns

**Connection Modes:**
- **Persistent**: Maintains long-lived connections and sends multiple messages per connection
- **Ephemeral**: Creates new connections for each request, useful for connection establishment testing

**Performance Optimizations:**
- Uses `sync.Pool` for buffer reuse to reduce GC pressure
- Platform-specific socket options (TCP_FASTOPEN, SO_REUSEPORT, TCP_QUICKACK on Linux)
- Rate limiting with go.uber.org/ratelimit
- Context-based cancellation throughout

**Metrics & Reporting:**
- Real-time latency statistics using github.com/rcrowley/go-metrics
- Configurable reporting intervals
- Support for merging results across multiple target hosts
- JSON Lines output format (`--jsonlines`) for machine-readable integration with monitoring tools

## Testing

The project includes comprehensive test files:
- `client_test.go`: Client connection and load generation tests
- `server_test.go`: Server echo functionality tests
- `main_test.go`: CLI flag parsing and integration tests
- `e2e_test.go`: End-to-end testing of client-server interaction
- `printer_test.go`: Output formatting and metrics printing tests

When adding new functionality:
- Follow existing test patterns with table-driven tests where appropriate
- Ensure all edge cases are covered, especially around connection handling and error scenarios
- Test both TCP and UDP protocol paths
- Test both persistent and ephemeral connection modes
- Include race condition testing (`go test -race`) for concurrent operations

## Development Notes

- File descriptor limits are automatically raised via printer.go
- Server gracefully handles signals (SIGINT, SIGTERM) for clean shutdown
- Error handling includes specific logic for network timeouts and connection resets
- Build uses Go modules with go 1.25
- Platform-specific code uses build tags (+build linux / +build !linux)
- CGO is disabled by default (CGO_ENABLED=0) for static binary builds

## Dependencies

Key dependencies include:
- github.com/spf13/pflag: POSIX-style command-line flag parsing
- github.com/spf13/viper: Configuration management with flag binding
- github.com/rcrowley/go-metrics: Performance metrics
- go.uber.org/ratelimit: Rate limiting
- golang.org/x/sync/errgroup: Concurrent error handling
- golang.org/x/sys/unix: Linux system calls

## Output Format

### Standard Output
The standard output format includes the following columns:
- `CNT`: Total number of messages/requests sent (not connections)
  - For persistent mode: CNT = rate × duration × connections
  - For ephemeral mode: CNT = rate × duration
- `LAT_*`: Latency metrics in microseconds (max, min, mean, 90p, 95p, 99p)
- `RATE(/s)`: Average request rate per second

### JSON Lines Format
When using `--jsonlines` flag, output includes:
- `peer`: Target server address
- `count`: Total number of messages/requests sent
- `latency_*_us`: Latency metrics in microseconds
- `rate_per_sec`: Average request rate per second
- `timestamp`: ISO 8601 timestamp

## Additional Guidance

- All Go files are placed in the top-level directory
- Use build tags for platform-specific implementations
- Multi-target testing is supported by passing multiple addresses as arguments
- Rate limiting controls messages per connection (persistent mode) or new connections per second (ephemeral mode)
