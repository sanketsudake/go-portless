package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sanketsudake/go-portless/control"
)

const noProxyList = "localhost,127.0.0.1,::1"

// cmdEnv prints proxy environment exports for shells and CI:
//
//	eval "$(portless env)"
//	portless env --shell github >> "$GITHUB_ENV"
func cmdEnv(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	shell := fs.String("shell", "sh", "output format: sh, fish, or github")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := control.NewClient(*socket).Status(context.Background())
	if err != nil {
		return err
	}
	if st.ProxyAddr == "" {
		return errors.New("env: daemon is running without a forward proxy (started with --no-proxy?)")
	}
	proxyURL := "http://" + st.ProxyAddr

	switch *shell {
	case "sh":
		fmt.Fprintf(stdout, "export HTTP_PROXY=%s\nexport HTTPS_PROXY=%s\nexport NO_PROXY=%s\n",
			proxyURL, proxyURL, noProxyList)
	case "fish":
		fmt.Fprintf(stdout, "set -gx HTTP_PROXY %s\nset -gx HTTPS_PROXY %s\nset -gx NO_PROXY %s\n",
			proxyURL, proxyURL, noProxyList)
	case "github":
		fmt.Fprintf(stdout, "HTTP_PROXY=%s\nHTTPS_PROXY=%s\nNO_PROXY=%s\n",
			proxyURL, proxyURL, noProxyList)
	default:
		return fmt.Errorf("env: unknown shell %q (sh, fish, github)", *shell)
	}
	return nil
}
