# Contributing

Thanks for helping with tcpulse.

- **Discuss first** for anything beyond a small fix: open an issue describing
  the problem, the command line, the platform and what you expected.
- **Read [docs/design.md](docs/design.md)** before changing what the client
  times or counts, or how the server echoes: it explains why tcpulse behaves
  as it does.
- **Keep pull requests focused.** One change per PR, with tests for any
  behaviour change. Tests run against loopback echo servers on port 0 and
  need neither root nor a network.
- **Run `make check`** before pushing. It runs the linters, the workflow
  audit, `go vet` for Linux and macOS, the race-enabled tests and the build.
  Touching option parsing or `internal/stats`? Run `make test-fuzz` too; the
  release configuration or the Dockerfiles, `make snapshot`.
- **Measurements:** a change to what is timed or counted comes with runs of
  the previous and the new binary against the same server, in the PR.
- **Keep the docs in sync.** A change to flags, output or behaviour updates
  `README.md` and `docs/design.md` together. `CHANGELOG.md` is written by
  tagpr from the pull request titles when a release is prepared.
- **Commits, branches and pull request titles** follow the Git conventions
  in [AGENTS.md](AGENTS.md#git-conventions).

The conventions and compatibility contracts are in [AGENTS.md](AGENTS.md).
Security issues go through [SECURITY.md](SECURITY.md), not public issues.
