# Changelog

## [v0.8.4](https://github.com/yuuki/tcpulse/compare/v0.8.3...v0.8.4) - 2026-01-13

## [v0.8.3](https://github.com/yuuki/tcpulse/compare/v0.8.2...v0.8.3) - 2026-01-12
- docs: clarify CNT field meaning in help output by @yuuki in https://github.com/yuuki/tcpulse/pull/44
- Upgrade Go version to 1.25 by @yuuki in https://github.com/yuuki/tcpulse/pull/45

## [v0.8.2](https://github.com/yuuki/tcpulse/compare/v0.8.1...v0.8.2) - 2025-06-13
- refactor: separate printer functionality into dedicated module by @yuuki in https://github.com/yuuki/tcpulse/pull/40
- Fix SIGINT handling in high-load ephemeral TCP/UDP mode by @yuuki in https://github.com/yuuki/tcpulse/pull/41

## [v0.8.1](https://github.com/yuuki/tcpulse/compare/v0.8.0...v0.8.1) - 2025-06-10
- test: add comprehensive test for multiple concurrent client connections by @yuuki in https://github.com/yuuki/tcpulse/pull/32
- fix: optimize sync.Pool usage to avoid allocation during Put() calls by @yuuki in https://github.com/yuuki/tcpulse/pull/33
- docs: add comprehensive tool comparison section by @yuuki in https://github.com/yuuki/tcpulse/pull/34
- fix: eliminate 'connection reset by peer' errors by @yuuki in https://github.com/yuuki/tcpulse/pull/35
- fix: eliminate pointer allocation warnings in sync.Pool usage (SA6002) by @yuuki in https://github.com/yuuki/tcpulse/pull/36
- feat: environment variable support by @yuuki in https://github.com/yuuki/tcpulse/pull/37
- feat: add --help flag support by @yuuki in https://github.com/yuuki/tcpulse/pull/38

## [v0.8.0](https://github.com/yuuki/tcpulse/compare/v0.7.2...v0.8.0) - 2025-06-08
- refactor: replace cobra subcommands with pflag/viper flags by @yuuki in https://github.com/yuuki/tcpulse/pull/28
- refactor: consolidate utility functions and add comprehensive main.go tests by @yuuki in https://github.com/yuuki/tcpulse/pull/29

## [v0.7.3](https://github.com/yuuki/tcpulse/compare/v0.7.2...v0.7.3) - 2025-06-08
- refactor: replace cobra subcommands with pflag/viper flags by @yuuki in https://github.com/yuuki/tcpulse/pull/28

## [v0.7.2](https://github.com/yuuki/tcpulse/compare/v0.7.1...v0.7.2) - 2025-06-08
- Add JSON Lines output format for measurement results by @yuuki in https://github.com/yuuki/tcpulse/pull/25
- refactor: rename project from connperf to tcpulse by @yuuki in https://github.com/yuuki/tcpulse/pull/26

## [v0.7.1](https://github.com/yuuki/tcpulse/compare/v0.7.0...v0.7.1) - 2025-06-06
- serve: support to pass addrs in a file as an argument by @yuuki in https://github.com/yuuki/tcpulse/pull/18
- bump version go by @yuuki in https://github.com/yuuki/tcpulse/pull/19

## [v0.7.1](https://github.com/yuuki/tcpulse/compare/v0.7.0...v0.7.1) - 2025-06-06
- serve: support to pass addrs in a file as an argument by @yuuki in https://github.com/yuuki/tcpulse/pull/18
- bump version go by @yuuki in https://github.com/yuuki/tcpulse/pull/19

## [v0.7.1](https://github.com/yuuki/tcpulse/compare/v0.7.0...v0.7.1) - 2025-06-06
- serve: support to pass addrs in a file as an argument by @yuuki in https://github.com/yuuki/tcpulse/pull/18
- bump version go by @yuuki in https://github.com/yuuki/tcpulse/pull/19

## [v0.7.1](https://github.com/yuuki/tcpulse/compare/v0.7.0...v0.7.1) - 2025-06-06
- serve: support to pass addrs in a file as an argument by @yuuki in https://github.com/yuuki/tcpulse/pull/18
- bump version go by @yuuki in https://github.com/yuuki/tcpulse/pull/19

## [v0.7.0](https://github.com/yuuki/tcpulse/compare/v0.6.0...v0.7.0) - 2021-05-31
- serve: set SO_REUSEPORTS by @yuuki in https://github.com/yuuki/tcpulse/pull/15
- serve: add --protocol flag by @yuuki in https://github.com/yuuki/tcpulse/pull/16
- connect: support to pass addrs in a file as an argument by @yuuki in https://github.com/yuuki/tcpulse/pull/17

## [v0.6.0](https://github.com/yuuki/tcpulse/compare/v0.5.3...v0.6.0) - 2021-05-25
- serve: support multiple listening ports by @yuuki in https://github.com/yuuki/tcpulse/pull/14

## [v0.5.3](https://github.com/yuuki/tcpulse/compare/v0.5.2...v0.5.3) - 2021-05-24
- connect: add --merge-results-each-host flag by @yuuki in https://github.com/yuuki/tcpulse/pull/13

## [v0.5.2](https://github.com/yuuki/tcpulse/compare/v0.5.1...v0.5.2) - 2021-05-24
- avoid connect overflow by @yuuki in https://github.com/yuuki/tcpulse/pull/11
- connect: fix nil pointer dereference of the 'conn' object by @yuuki in https://github.com/yuuki/tcpulse/pull/12

## [v0.5.1](https://github.com/yuuki/tcpulse/compare/v0.5.0...v0.5.1) - 2021-04-19

## [v0.5.0](https://github.com/yuuki/tcpulse/compare/v0.4.1...v0.5.0) - 2021-04-19
- use GITHUB_TOKEN instead of PAT in GitHub Action by @yuuki in https://github.com/yuuki/tcpulse/pull/7
- Use docker actions by @yuuki in https://github.com/yuuki/tcpulse/pull/8
- connect: add --message-bytes option by @yuuki in https://github.com/yuuki/tcpulse/pull/9
- Tuning TCP option for ephemeral flavor by @yuuki in https://github.com/yuuki/tcpulse/pull/10

## [v0.4.1](https://github.com/yuuki/tcpulse/compare/v0.4.0...v0.4.1) - 2021-03-24

## [v0.4.0](https://github.com/yuuki/tcpulse/compare/v0.3.2...v0.4.0) - 2021-03-23
- enable persistent rate by @yuuki in https://github.com/yuuki/tcpulse/pull/6

## [v0.3.2](https://github.com/yuuki/tcpulse/compare/v0.3.1...v0.3.2) - 2021-03-03
- connect: enable to return network error by @yuuki in https://github.com/yuuki/tcpulse/pull/5

## [v0.3.1](https://github.com/yuuki/tcpulse/compare/v0.3.0...v0.3.1) - 2021-03-03
- use the new ErrClosed error introduced by go 1.16 by @yuuki in https://github.com/yuuki/tcpulse/pull/4

## [v0.3.0](https://github.com/yuuki/tcpulse/compare/v0.2.0...v0.3.0) - 2021-02-22
- Bump version Go 1.16 by @yuuki in https://github.com/yuuki/tcpulse/pull/3

## [v0.2.0](https://github.com/yuuki/tcpulse/compare/v0.1.0...v0.2.0) - 2021-02-13
- connect: Support multiple addrs by @yuuki in https://github.com/yuuki/tcpulse/pull/1
- Add --show-only-results by @yuuki in https://github.com/yuuki/tcpulse/pull/2

## [v0.1.0](https://github.com/yuuki/tcpulse/commits/v0.1.0) - 2021-02-13
