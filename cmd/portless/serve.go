package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/ca"
	"github.com/sanketsudake/go-portless/control"
	"github.com/sanketsudake/go-portless/proxy"
)

type serveOptions struct {
	socket    string
	proxyAddr string // "" disables the forward proxy
	tls       bool   // terminate TLS with the local CA
	stateDir  string
}

func cmdServe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	proxyAddr := fs.String("proxy", "127.0.0.1:0", "forward proxy listen address")
	noProxy := fs.Bool("no-proxy", false, "disable the forward proxy")
	tlsOn := fs.Bool("tls", false, "terminate TLS so https://<name> works (trust the CA: portless ca install)")
	stateDir := fs.String("state-dir", ca.DefaultStateDir(), "CA/state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := serveOptions{socket: *socket, proxyAddr: *proxyAddr, tls: *tlsOn, stateDir: *stateDir}
	if *noProxy {
		opts.proxyAddr = ""
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if code := runServe(ctx, opts, stdout, stderr); code != 0 {
		return fmt.Errorf("daemon exited with code %d", code)
	}
	return nil
}

// runServe runs the daemon until ctx is canceled. Split from cmdServe so
// tests can drive it with their own context.
func runServe(ctx context.Context, opts serveOptions, stdout, stderr io.Writer) int {
	// Default registries are strict: the forward proxy reaches only
	// registered routes, never a real network dial (open-proxy risk).
	reg := portless.New()
	defer func() { _ = reg.Close() }()

	proxyAddr := ""
	if opts.proxyAddr != "" {
		var popts []proxy.Option
		if opts.tls {
			authority, err := ca.Load(opts.stateDir)
			if err != nil {
				fmt.Fprintf(stderr, "portless: load CA: %v\n", err)
				return 1
			}
			popts = append(popts, proxy.WithTLS(authority))
		}
		p := proxy.New(reg, popts...)
		addr, err := p.Start(ctx, opts.proxyAddr)
		if err != nil {
			fmt.Fprintf(stderr, "portless: %v\n", err)
			return 1
		}
		defer func() { _ = p.Close() }()
		proxyAddr = addr
		scheme := "http"
		if opts.tls {
			scheme = "https"
		}
		fmt.Fprintf(stdout, "proxy listening on %s (%s://<name> routes)\n", addr, scheme)
	}

	if err := control.EnsureSocketDir(opts.socket); err != nil {
		fmt.Fprintf(stderr, "portless: %v\n", err)
		return 1
	}
	l, err := net.Listen("unix", opts.socket)
	if err != nil {
		fmt.Fprintf(stderr, "portless: listen control socket: %v\n", err)
		return 1
	}
	if err := os.Chmod(opts.socket, 0o600); err != nil {
		fmt.Fprintf(stderr, "portless: chmod control socket: %v\n", err)
		return 1
	}
	defer func() { _ = os.Remove(opts.socket) }()

	srv := control.NewServer(reg, control.WithProxyAddr(func() string { return proxyAddr }))
	fmt.Fprintf(stdout, "control socket at %s\n", opts.socket)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(l) }()

	select {
	case <-ctx.Done():
		_ = srv.Close()
		<-errc
		return 0
	case err := <-errc:
		if err != nil {
			fmt.Fprintf(stderr, "portless: control server: %v\n", err)
			return 1
		}
		return 0
	}
}
