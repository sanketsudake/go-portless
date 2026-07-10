package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCAPathGeneratesAndPrints(t *testing.T) {
	dir := t.TempDir()
	out, code := runCLI(t, "ca", "path", "--state-dir", dir)
	if code != 0 {
		t.Fatalf("ca path exited %d: %s", code, out)
	}
	want := filepath.Join(dir, "ca.crt")
	if strings.TrimSpace(out) != want {
		t.Fatalf("ca path = %q, want %q", strings.TrimSpace(out), want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("ca cert not generated: %v", err)
	}
}

func TestCAInstallRefusesNonInteractive(t *testing.T) {
	// Point stdin at /dev/null so confirm sees a non-interactive shell and
	// refuses before touching the system trust store (deterministic, and it
	// avoids consuming the shared os.Stdin).
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	old := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() { os.Stdin = old })

	dir := t.TempDir()
	out, code := runCLI(t, "ca", "install", "--state-dir", dir)
	if code == 0 {
		t.Fatalf("ca install should refuse without a TTY or --yes, got: %s", out)
	}
	if !strings.Contains(out, "--yes") {
		t.Fatalf("refusal should mention --yes, got: %s", out)
	}
}

func TestVersion(t *testing.T) {
	out, code := runCLI(t, "version")
	if code != 0 || !strings.Contains(out, "portless") {
		t.Fatalf("version output: %q (exit %d)", out, code)
	}
	out, code = runCLI(t, "--version")
	if code != 0 || !strings.Contains(out, "portless") {
		t.Fatalf("--version output: %q (exit %d)", out, code)
	}
}
