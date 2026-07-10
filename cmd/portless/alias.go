package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sanketsudake/go-portless/control"
)

// cmdAlias registers a static route to an already-running service:
//
//	portless alias web 127.0.0.1:9000
//
// It is the escape hatch for pointing a name at an endpoint you did not start
// through portless (a container, an external service), analogous to
// portless.sh's `alias`. The daemon is started if not already running.
func cmdAlias(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("alias", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("usage: portless alias NAME HOST:PORT")
	}
	name, addr := pos[0], pos[1]

	ctx := context.Background()
	c, err := ensureDaemon(ctx, *socket)
	if err != nil {
		return err
	}
	cfg, err := json.Marshal(map[string]string{"address": addr})
	if err != nil {
		return err
	}
	if err := c.AddRoute(ctx, control.RouteSpec{Name: name, Type: "tcp", Config: cfg}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s → %s\n", name, addr)
	return nil
}
