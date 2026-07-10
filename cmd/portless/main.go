// Command portless is a thin CLI over the go-portless library: a daemon
// (forward proxy + control API) and route management, so shell scripts and
// CI reach named routes via HTTP_PROXY.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/sanketsudake/go-portless/k8s"
)

func init() {
	// Make the "k8s" backend type available to the daemon's control API.
	k8s.Register()
}

const usage = `portless — port-free service routing for tests and CI

Usage:
  portless serve  [--socket PATH] [--proxy ADDR] [--no-proxy]
  portless route  add NAME (--tcp HOST:PORT) [--socket PATH]
  portless route  list [--json] [--socket PATH]
  portless route  rm NAME [--socket PATH]
  portless env    [--shell sh|fish|github] [--socket PATH]
  portless status [--json] [--socket PATH]
  portless doctor [NAME...] [--timeout DUR] [--socket PATH]

The control socket defaults to $PORTLESS_SOCKET, then
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
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "portless: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "portless: %v\n", err)
		return 1
	}
	return 0
}
