package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/sanketsudake/go-portless/control"
)

// cmdDoctor probes routes for readiness and reports timing — the explicit
// "wait once, then everything is fast" primitive for CI scripts.
func cmdDoctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	timeout := fs.Duration("timeout", 30*time.Second, "per-route readiness timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	c := control.NewClient(*socket)
	names := fs.Args()
	if len(names) == 0 {
		routes, err := c.Routes(context.Background())
		if err != nil {
			return err
		}
		for _, rt := range routes {
			names = append(names, rt.Name)
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "no routes registered")
		return nil
	}

	var failed int
	for _, name := range names {
		start := time.Now()
		if err := c.WaitReady(context.Background(), name, *timeout); err != nil {
			fmt.Fprintf(stdout, "%-30s NOT READY  %v\n", name, err)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "%-30s ready      %v\n", name, time.Since(start).Round(time.Millisecond))
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d route(s) not ready", failed)
	}
	return nil
}
