# AGENTS.md

## Rules
- Only completed round trips are samples: a request cut off by the end of the run (`cutOff`) or failing with `errTolerated` is neither recorded nor an error; a target without any round trip fails the run (exit 1).
- The clock covers only the round trip: generate the payload before starting it, and read the whole echo (`io.ReadFull`) before stopping it.
- CNT promises rate × duration (× connections for persistent), rounded down (`client.Messages`); pacing stays with `go.uber.org/ratelimit` and its slack.
- Latencies go through `internal/stats.Histogram`: count, min, max and mean exact, percentiles nearest-rank within 1/128; no global registry or package state.
- Validation in `internal/cli` rejects every option set `client.Run` cannot handle (it would panic or send nothing); `FuzzParse` checks it against an exact message count.
- One connection never stops the server: transient accept errors back off, resets are normal ends, socket options are optimizations logged once.
- Tests listen on port 0 (`server.Listen`, `TCPAddrs`) and keep at least 100ms of margin in timing assertions; macOS sends UDP datagrams of 9216 bytes at most.
- `main.go` stays at the root so that `go install github.com/yuuki/tcpulse@latest` works.
- `--kernel-events` adds no hook on the path of every packet or message: tracepoints at state changes, retransmissions and resets; counters checked after the clock stops (on amd64 and arm64 through shared memory, with a system call for a connection's first reading and after an event was counted), a duplicate of each dialed socket so that failed dials are read too; events that cannot be read make a round trip untracked, never one without events.

## Compatibility contracts
- Flags, short options, defaults, exit statuses (2 for a command line or variable that cannot be parsed) and the option lines of the help text (pflag stays on 1.0): `internal/cli/flags.go`.
- `TCPULSE_*` variables, one per option but `--help` and `--version`, below the command line, an invalid one failing only the mode that uses it: `EnvName`, `applyEnv`, `envError`.
- Text columns and JSON Lines fields, names and order (golden in `internal/report/report_test.go`); `Listening at ...`; `tcpulse X (revision: Y)`.
- The echo: bytes and datagrams written back unchanged, default `0.0.0.0:9100`, UDP over IPv4 only (`udp4`) on both sides.
- eBPF: `internal/kevents/bpf/` ↔ the committed bpf2go output `internal/kevents/kevents_bpfel.{go,o}` (`make generate`); the programs stay `GPL-2.0-only OR BSD-2-Clause` (`NOTICE`, `LICENSES/`).
- `version.go` is tagpr's `versionFile`; archive, checksums and image names, tags (`X.Y.Z`, `X.Y`, `X`, `latest`), user and binary path: `.goreleaser.yaml`, `Dockerfile.goreleaser`. A breaking change gets the label `minor` (or `major`) for tagpr.

## Before opening a pull request
- `make check`; also `make test-fuzz` for option parsing, `client.Messages` or `internal/stats`; `make snapshot` for `.goreleaser.yaml`, the Dockerfiles or the release workflows.
- `internal/kevents/` → `make generate` (commit both `kevents_bpfel.*`) and `make test-bpf` as root.
- What is timed or counted: compare runs of the previous and the new binary against the same server, and say so in the PR.

## Git conventions
- Branches: `<type>/<issue>-<summary>` (`<type>/<summary>` without an issue); tools' `agent/*`, `claude/*`, `copilot/*`, `dependabot/*` keep their names.
- Commits and PR titles: Conventional Commits 1.0.0, types `feat` `fix` `perf` `refactor` `test` `docs` `build` `ci` `chore` `revert`; scopes: `client`, `server`, `cli`, `stats`, `report`, `sockopt`, `kevents`, `release`.
