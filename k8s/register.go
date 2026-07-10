package k8s

import (
	"encoding/json"
	"fmt"
	"net/url"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/control"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/clientcmd"
)

// Config is the JSON shape of a "k8s" control-plane RouteSpec.
type Config struct {
	Kubeconfig string `json:"kubeconfig,omitempty"` // path; empty uses in-cluster/default loading
	Namespace  string `json:"namespace"`
	Service    string `json:"service,omitempty"`
	Selector   string `json:"selector,omitempty"`
	Pod        string `json:"pod,omitempty"`
	TargetPort string `json:"targetPort,omitempty"` // number or named port
}

// Register makes the "k8s" backend type available to control servers, so the
// portless daemon can create port-forward routes. Call once at startup
// (e.g. from cmd/portless).
func Register() {
	control.RegisterBackendType("k8s", func(raw json.RawMessage) (portless.Backend, error) {
		var c Config
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("k8s config: %w", err)
		}
		loading := clientcmd.NewDefaultClientConfigLoadingRules()
		if c.Kubeconfig != "" {
			loading.ExplicitPath = c.Kubeconfig
		}
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loading, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("k8s: load kubeconfig: %w", err)
		}

		var opts []Option
		switch {
		case c.Service != "":
			opts = append(opts, Service(c.Namespace, c.Service))
		case c.Selector != "":
			opts = append(opts, LabelSelector(c.Namespace, c.Selector))
		case c.Pod != "":
			opts = append(opts, Pod(c.Namespace, c.Pod))
		default:
			return nil, fmt.Errorf("k8s config: one of service, selector, or pod is required")
		}
		if c.TargetPort != "" {
			opts = append(opts, TargetPort(intstr.Parse(c.TargetPort)))
		}
		return PortForward(cfg, opts...)
	})
}

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic("k8s: bad portforward URL: " + err.Error())
	}
	return u
}
