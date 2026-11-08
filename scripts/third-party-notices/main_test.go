package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestNotices lists the dependencies of the tcpulse command itself, so it
// needs the modules of go.mod, as the build does.
func TestNotices(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list for every release platform")
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
	var b bytes.Buffer
	if err := writeLicenses(&b, "x", t.TempDir()); err == nil || !strings.Contains(err.Error(), "no license") {
		t.Fatalf("writeLicenses of a directory without license = %v, want an error", err)
	}
}
