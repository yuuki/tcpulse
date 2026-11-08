# Security policy

tcpulse is a load generator and an echo server for test networks. The server
writes back whatever any client sends it, over plain TCP and UDP, without
authentication: run it only where the clients that can reach it are allowed
to load it. The client's `--enable-pprof` serves Go's profiling endpoints
without authentication on `--pprof-addr`, `localhost:6060` by default.

Examples of what is in scope:

- Input from the network that crashes the server or the client, makes the
  server stop serving other connections, or makes either use memory out of
  proportion to the load it was configured for.
- Options or environment variables that make tcpulse do something other
  than documented (connect, listen or write elsewhere).
- Privileges of the container image broader than documented (it runs as
  `nobody`).
- Problems with the release archives, checksums, attestations or the
  published image.

Out of scope: the load itself (tcpulse is meant to exhaust the capacity of
what it tests), the absence of authentication and encryption described
above, and the reflection of traffic by an echo server exposed to untrusted
networks.

## Supported versions

Security fixes are made on the `main` branch and released in the next
version. Only the latest release is supported.

## Reporting a vulnerability

Please do **not** open a public issue, pull request or discussion.

Report the vulnerability privately with GitHub's
[private vulnerability reporting](https://github.com/yuuki/tcpulse/security/advisories/new)
(the "Report a vulnerability" button of the Security tab). If that is not
possible, contact the author privately via the contact information on
https://github.com/yuuki.

Please include the version (`tcpulse --version`), the platform, how tcpulse
runs (binary, container image), the options and environment it runs with,
what an attacker controls, steps to reproduce and the impact. You should
receive an answer within a few weeks; tcpulse is maintained on a best-effort
basis.

## Verifying releases

Every release publishes `tcpulse_<version>_checksums.txt`, and every archive
and the checksums file have a signed
[build provenance attestation](https://github.com/yuuki/tcpulse/attestations)
stating that the release workflow built them from the tag. See
[Pre-built binaries](README.md#pre-built-binaries).
