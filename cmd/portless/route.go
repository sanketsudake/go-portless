package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/sanketsudake/go-portless/control"
)

func cmdRoute(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("route: expected subcommand add|list|rm")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return routeAdd(rest, stderr)
	case "list":
		return routeList(rest, stdout, stderr)
	case "rm":
		return routeRm(rest, stderr)
	default:
		return fmt.Errorf("route: unknown subcommand %q", sub)
	}
}

// splitName pulls the positional NAME out of args so flags can appear before
// or after it.
func splitName(args []string) (string, []string, error) {
	var name string
	var flags []string
	for _, a := range args {
		if len(a) > 0 && a[0] != '-' && name == "" && !isFlagValue(flags) {
			name = a
			continue
		}
		flags = append(flags, a)
	}
	if name == "" {
		return "", nil, errors.New("route: NAME argument required")
	}
	return name, flags, nil
}

// isFlagValue reports whether the next bare token belongs to a preceding
// flag written as "--flag value".
func isFlagValue(flags []string) bool {
	if len(flags) == 0 {
		return false
	}
	last := flags[len(flags)-1]
	if len(last) == 0 || last[0] != '-' {
		return false
	}
	for _, c := range last {
		if c == '=' {
			return false
		}
	}
	// boolean flags never take a separate value; all our value flags are
	// strings, so a trailing "--flag" expects the next token.
	switch last {
	case "--json", "-json":
		return false
	}
	return true
}

func routeAdd(args []string, stderr io.Writer) error {
	name, flagArgs, err := splitName(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("route add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	tcp := fs.String("tcp", "", "static TCP backend address (HOST:PORT)")
	k8sService := fs.String("k8s-service", "", "Kubernetes Service as NS/NAME")
	k8sSelector := fs.String("k8s-selector", "", "Kubernetes pod label selector (requires --k8s-namespace)")
	k8sNamespace := fs.String("k8s-namespace", "", "namespace for --k8s-selector")
	targetPort := fs.String("target-port", "", "pod container port (number or name) for k8s backends")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path for k8s backends")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	spec := control.RouteSpec{Name: name}
	switch {
	case *tcp != "":
		cfg, err := json.Marshal(map[string]string{"address": *tcp})
		if err != nil {
			return err
		}
		spec.Type, spec.Config = "tcp", cfg
	case *k8sService != "":
		ns, svc, ok := strings.Cut(*k8sService, "/")
		if !ok {
			return errors.New("route add: --k8s-service must be NS/NAME")
		}
		cfg, err := json.Marshal(k8sConfig{Namespace: ns, Service: svc, TargetPort: *targetPort, Kubeconfig: *kubeconfig})
		if err != nil {
			return err
		}
		spec.Type, spec.Config = "k8s", cfg
	case *k8sSelector != "":
		if *k8sNamespace == "" {
			return errors.New("route add: --k8s-selector requires --k8s-namespace")
		}
		cfg, err := json.Marshal(k8sConfig{Namespace: *k8sNamespace, Selector: *k8sSelector, TargetPort: *targetPort, Kubeconfig: *kubeconfig})
		if err != nil {
			return err
		}
		spec.Type, spec.Config = "k8s", cfg
	default:
		return errors.New("route add: a backend flag is required (--tcp, --k8s-service, or --k8s-selector)")
	}
	return control.NewClient(*socket).AddRoute(context.Background(), spec)
}

// k8sConfig mirrors k8s.Config for building route specs without importing the
// k8s package here (the daemon deserializes it).
type k8sConfig struct {
	Kubeconfig string `json:"kubeconfig,omitempty"`
	Namespace  string `json:"namespace"`
	Service    string `json:"service,omitempty"`
	Selector   string `json:"selector,omitempty"`
	TargetPort string `json:"targetPort,omitempty"`
}

func routeList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("route list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	routes, err := control.NewClient(*socket).Routes(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(routes)
	}
	tw := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tCONFIG")
	for _, rt := range routes {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", rt.Name, rt.Type, string(rt.Config))
	}
	return tw.Flush()
}

func routeRm(args []string, stderr io.Writer) error {
	name, flagArgs, err := splitName(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("route rm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", control.DefaultSocketPath(), "control socket path")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	return control.NewClient(*socket).RemoveRoute(context.Background(), name)
}
