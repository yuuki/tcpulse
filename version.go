package main

import "runtime/debug"

// version is the version of tcpulse. tagpr rewrites it when it prepares a
// release (versionFile in .tagpr), and release builds set it with
// -ldflags "-X main.version=...".
var version = "0.9.0"

// revision is the commit tcpulse was built from. Release builds set it with
// -ldflags "-X main.revision=...".
var revision = ""

// buildRevision returns revision, or else the commit the Go toolchain
// stamped into the binary when it was built from a git checkout, or "HEAD".
func buildRevision() string {
	if revision != "" {
		return revision
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value[:min(len(s.Value), 7)]
			}
		}
	}
	return "HEAD"
}
