// Command third-party-notices writes the license texts of the Go modules and
// of the Go standard library linked into the tcpulse binary, which the
// release archives ship at their root and the container images in
// /usr/share/licenses/tcpulse.
//
//	go run ./scripts/third-party-notices -o FILE [PACKAGE...]
//
// The packages (by default the command, ".") are listed with their
// dependencies by go list, for every platform tcpulse is released for: the
// file covers the union, since the modules differ between Linux and macOS
// (github.com/cilium/ebpf is Linux only). It fails when a module has no
// license file at its root, so that a new dependency cannot be shipped
// without its license. Only module roots are read: a module whose license
// asks for a NOTICE file that the module zip leaves out, or that bundles code
// under another license in a subdirectory, needs a look by hand.
//
// Adapted from scripts/third-party-notices of github.com/yuuki/shawk.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ownModule is covered by the repository's own LICENSE and NOTICE.
const ownModule = "github.com/yuuki/tcpulse"

// platforms are those of the release archives (.goreleaser.yaml, which
// TestPlatforms compares).
var platforms = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}

var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|patents|unlicense)([.\-_].*)?$`)

type module struct {
	Path    string
	Version string
	Dir     string
	Main    bool
	Replace *module
}

type pkg struct {
	ImportPath string
	Standard   bool
	Module     *module
}

func main() {
	out := flag.String("o", "", "output file")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: third-party-notices -o FILE [PACKAGE...]")
		os.Exit(2)
	}
	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"."}
	}
	b, err := notices(context.Background(), patterns)
	if err == nil {
		err = writeFile(*out, b)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "third-party-notices:", err)
		os.Exit(1)
	}
}

// notices returns the notices of the modules that patterns link, on every
// release platform.
func notices(ctx context.Context, patterns []string) ([]byte, error) {
	mods := map[string]module{}
	for _, platform := range platforms {
		goos, goarch, _ := strings.Cut(platform, "/")
		if err := list(ctx, patterns, goos, goarch, mods); err != nil {
			return nil, fmt.Errorf("%s: %w", platform, err)
		}
	}

	var b bytes.Buffer
	b.WriteString("Third-party software linked into tcpulse, with the texts of its licenses.\n")
	goroot, err := goEnv(ctx, "GOROOT")
	if err != nil {
		return nil, err
	}
	goversion, err := goEnv(ctx, "GOVERSION")
	if err != nil {
		return nil, err
	}
	if err := writeLicenses(&b, "Go standard library and runtime "+goversion, goroot); err != nil {
		// A distribution's Go package may keep them elsewhere: the release
		// is built with Go's own distribution.
		return nil, fmt.Errorf("the Go standard library (%s, %s): %w", goversion, goroot, err)
	}
	keys := make([]string, 0, len(mods))
	for k := range mods {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var missing []string
	for _, k := range keys {
		m := mods[k]
		if err := writeLicenses(&b, m.Path+" "+m.Version, m.Dir); err != nil {
			missing = append(missing, fmt.Sprintf("%s %s: %v", m.Path, m.Version, err))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no license found for:\n  %s", strings.Join(missing, "\n  "))
	}
	return b.Bytes(), nil
}

// list adds the modules of the packages patterns depend on, for GOOS goos and
// GOARCH goarch.
func list(ctx context.Context, patterns []string, goos, goarch string, mods map[string]module) error {
	args := append([]string{"list", "-deps", "-json=ImportPath,Standard,Module"}, patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return err
		}
		m := p.Module
		if p.Standard || m == nil || m.Main {
			continue
		}
		if m.Replace != nil {
			m = m.Replace
		}
		if isOwn(m.Path) || isOwn(p.Module.Path) {
			continue
		}
		if m.Dir == "" {
			return fmt.Errorf("module %s %s is not downloaded", m.Path, m.Version)
		}
		mods[m.Path+"@"+m.Version] = *m
	}
	return nil
}

// isOwn reports whether path is tcpulse's module, or one of its own nested
// modules.
func isOwn(path string) bool {
	return path == ownModule || strings.HasPrefix(path, ownModule+"/")
}

// writeLicenses appends the license files at the root of dir: those whose
// name is a license's (not Go files such as license.go).
func writeLicenses(b *bytes.Buffer, title, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var found bool
	for _, e := range entries {
		if e.IsDir() || !licenseFile.MatchString(e.Name()) || strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		text, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		found = true
		fmt.Fprintf(b, "\n%s\n%s (%s)\n%s\n\n", strings.Repeat("=", 78), title, e.Name(), strings.Repeat("-", 78))
		b.Write(bytes.TrimRight(text, "\n"))
		b.WriteByte('\n')
	}
	if !found {
		return errors.New("no license file")
	}
	return nil
}

func goEnv(ctx context.Context, name string) (string, error) {
	b, err := exec.CommandContext(ctx, "go", "env", name).Output()
	if err != nil {
		return "", fmt.Errorf("go env %s: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// writeFile replaces path with b, through a temporary file. The notices are
// for everyone to read, the image's unprivileged user included: the image
// copies the file with its mode.
func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // see above
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil { //nolint:gosec // see above
		return err
	}
	return os.Rename(tmp, path)
}
