package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"syscall"

	"github.com/sanketsudake/go-portless/control"
)

// exitCodeError carries a child process's exit code up to main so `run` can
// propagate it.
type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("child exited with code %d", e.code) }

// cmdRun spawns a process on an OS-assigned port and gives it a stable name —
// the port-free equivalent of `portless.sh`'s `portless <name> <cmd>`:
//
//	portless run web -- go run ./cmd/server
//
// It ensures a shared daemon, picks a free port, sets $PORT (or --port-env)
// in the child, registers NAME → that port, streams the child's output, and
// deregisters on exit. The child never sees a hardcoded port number.
func cmdRun(args []string, stdout, stderr io.Writer) error {
	before, cmdArgs, hasSep := cut(args, "--")
	if !hasSep || len(cmdArgs) == 0 {
		return errors.New("usage: portless run NAME [flags] -- CMD [args...]")
	}

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	portEnv := fs.String("port-env", "PORT", "environment variable used to pass the assigned port")
	pos, err := parseFlags(fs, before)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: portless run NAME [flags] -- CMD [args...]")
	}
	name := pos[0]

	// Signals stop the child and trigger cleanup.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c, err := ensureDaemon(ctx, *socket)
	if err != nil {
		return err
	}
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("assign port: %w", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	cfg, _ := json.Marshal(map[string]string{"address": addr})
	if err := c.AddRoute(ctx, control.RouteSpec{Name: name, Type: "tcp", Config: cfg}); err != nil {
		return err
	}
	defer c.RemoveRoute(context.Background(), name) //nolint:errcheck // best-effort cleanup

	if st, err := c.Status(ctx); err == nil && st.ProxyAddr != "" {
		fmt.Fprintf(stderr, "portless: %s → http://%s (via proxy %s; run `portless env`)\n", name, name, st.ProxyAddr)
	}

	child := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	child.Env = append(os.Environ(), *portEnv+"="+strconv.Itoa(port))
	child.Stdout, child.Stderr, child.Stdin = stdout, stderr, os.Stdin
	if err := child.Start(); err != nil {
		return fmt.Errorf("start %q: %w", cmdArgs[0], err)
	}
	if err := child.Wait(); err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return &exitCodeError{code: ee.ExitCode()}
		}
		return err
	}
	return nil
}

// cut splits args at the first occurrence of sep, returning the parts before
// and after and whether sep was found.
func cut(args []string, sep string) (before, after []string, found bool) {
	i := slices.Index(args, sep)
	if i < 0 {
		return args, nil, false
	}
	return args[:i], args[i+1:], true
}
