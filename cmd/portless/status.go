package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/sanketsudake/go-portless/control"
)

func cmdStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := control.NewClient(*socket).Status(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	fmt.Fprintf(stdout, "pid:     %d\nversion: %s\nproxy:   %s\nroutes:  %d\nuptime:  %ds\n",
		st.PID, st.Version, st.ProxyAddr, st.RouteCount, st.UptimeSec)
	return nil
}
