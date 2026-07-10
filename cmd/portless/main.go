// Command portless is a thin CLI over the go-portless library: a daemon
// (forward proxy + control API) and route management, so shell scripts and
// CI reach named routes via HTTP_PROXY.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sanketsudake/go-portless/k8s"
)

func init() {
	// Make the "k8s" backend type available to the daemon's control API.
	k8s.Register()
}

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `portless — port-free service routing for tests and CI

Usage:
  portless run    NAME [--port-env VAR] -- CMD [args...]   run a process on an assigned port
  portless alias  NAME HOST:PORT [--socket PATH]           name an already-running service
  portless serve  [--socket PATH] [--proxy ADDR] [--tls] [--no-proxy]
  portless route  add NAME (--tcp HOST:PORT | --k8s-service NS/NAME) [--socket PATH]
  portless route  list [--json] [--socket PATH]
  portless route  rm NAME [--socket PATH]
  portless env    [--shell sh|fish|github] [--socket PATH]
  portless status [--json] [--socket PATH]
  portless doctor [NAME...] [--timeout DUR] [--socket PATH]
  portless ca     path | install | uninstall [--yes]      manage the local HTTPS CA
  portless version

Examples:
  # Run a server on an assigned port and reach it by name
  portless run web -- go run ./cmd/server
  eval "$(portless env)"
  curl http://web/healthz

  # Point a name at an already-running service
  portless alias db 127.0.0.1:5432

  # Enable https://<name> (trust the CA once)
  portless serve --tls &
  portless ca install

run, alias, and route start a shared background daemon automatically if one is
not already running. The control socket defaults to $PORTLESS_SOCKET, then
$XDG_RUNTIME_DIR/portless.sock, then a per-user temp path.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "run":
		err = cmdRun(rest, stdout, stderr)
	case "alias":
		err = cmdAlias(rest, stdout, stderr)
	case "serve":
		err = cmdServe(rest, stdout, stderr)
	case "route":
		err = cmdRoute(rest, stdout, stderr)
	case "env":
		err = cmdEnv(rest, stdout)
	case "status":
		err = cmdStatus(rest, stdout)
	case "doctor":
		err = cmdDoctor(rest, stdout)
	case "ca":
		err = cmdCA(rest, stdout, stderr)
	case "version", "--version":
		fmt.Fprintf(stdout, "portless %s\n", version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "portless: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		// `run` propagates its child's exit code verbatim.
		if ec, ok := errors.AsType[*exitCodeError](err); ok {
			return ec.code
		}
		fmt.Fprintf(stderr, "portless: %v\n", err)
		return 1
	}
	return 0
}
