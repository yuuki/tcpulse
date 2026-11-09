package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPlatforms checks that the notices cover the platforms the release
// builds (.goreleaser.yaml).
func TestPlatforms(t *testing.T) {
	data, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Builds []struct {
			Goos   []string
			Goarch []string
			Ignore []struct{ Goos, Goarch string }
		}
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	var built []string
	for _, b := range cfg.Builds {
		for _, goos := range b.Goos {
			for _, goarch := range b.Goarch {
				if !slices.ContainsFunc(b.Ignore, func(i struct{ Goos, Goarch string }) bool {
					return (i.Goos == "" || i.Goos == goos) && (i.Goarch == "" || i.Goarch == goarch)
				}) {
					built = append(built, goos+"/"+goarch)
				}
			}
		}
	}
	slices.Sort(built)
	want := slices.Sorted(slices.Values(platforms))
	if len(built) == 0 || !slices.Equal(slices.Compact(built), want) {
		t.Fatalf("the release builds %v, the notices cover %v", built, want)
	}
}

func TestIsOwn(t *testing.T) {
	for path, want := range map[string]bool{
		"github.com/yuuki/tcpulse":          true,
		"github.com/yuuki/tcpulse/tools":    true,
		"github.com/yuuki/tcpulse-exporter": false,
		"github.com/yuuki/shawk":            false,
	} {
		if isOwn(path) != want {
			t.Errorf("isOwn(%q) = %v", path, !want)
		}
	}
}

// TestNotices lists the dependencies of the tcpulse command itself, so it
// needs the modules of go.mod, as the build does.
func TestNotices(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list for every release platform")
	}
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(goroot)), "LICENSE")); err != nil {
		t.Skipf("the Go toolchain has no LICENSE in its GOROOT, as some distributions' packages: %v", err)
	}
	t.Chdir("../..")
	b, err := notices(t.Context(), []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Go standard library and runtime go",
		"github.com/spf13/pflag ",
		"go.uber.org/ratelimit ",
		"golang.org/x/sys ",
		// Linux only: listed although the host may be another system.
		"github.com/cilium/ebpf ",
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("notices lack %q", want)
		}
	}
	if bytes.Contains(b, []byte("github.com/yuuki/tcpulse ")) {
		t.Error("notices list tcpulse itself")
	}
	// Modules of the tests are not linked into the binary.
	if bytes.Contains(b, []byte("github.com/stretchr/testify")) {
		t.Error("notices list a module the binary does not link")
	}
}

func TestWriteLicensesNoLicense(t *testing.T) {
	// Go files named like a license are code, not a license.
	dir := t.TempDir()
	for _, name := range []string{"license.go", "notice_test.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	if err := writeLicenses(&b, "x", dir); err == nil || !strings.Contains(err.Error(), "no license") {
		t.Fatalf("writeLicenses of a directory without license = %v, want an error", err)
	}
}
