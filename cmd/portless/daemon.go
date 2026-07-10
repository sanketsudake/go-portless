package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/sanketsudake/go-portless/control"
)

// parseFlags parses fs from args where flags and positionals may be
// interspersed (e.g. "NAME --socket X" as well as "--socket X NAME"),
// returning the positional arguments in order.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, args[0])
		args = args[1:]
	}
}

// ensureDaemon returns a control client for a running daemon at socket,
// starting a detached one if none is reachable. The daemon outlives this
// process so multiple `run`/`alias` invocations share one proxy and registry.
func ensureDaemon(ctx context.Context, socket string) (*control.Client, error) {
	c := control.NewClient(socket)
	if _, err := c.Status(ctx); err == nil {
		return c, nil // already running
	}

	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate self to start daemon: %w", err)
	}
	cmd := exec.Command(self, "serve", "--socket", socket)
	// Detach into its own session so it survives this command exiting.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = devnull.Close() }()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	_ = cmd.Process.Release()

	// Wait for the control socket to come up.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := c.Status(ctx); err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("daemon did not become ready at %s", socket)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// freePort asks the OS for an unused TCP port. There is an inherent race
// between closing the listener and the caller (or a spawned child) binding
// the port, but the registry's readiness loop tolerates a briefly-unavailable
// backend, and a child that loses the race fails to bind and exits visibly.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}
