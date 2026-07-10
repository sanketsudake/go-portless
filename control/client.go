package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	portless "github.com/sanketsudake/go-portless"
)

// Client talks to a control server over its unix socket.
type Client struct {
	httpc *http.Client
}

// NewClient returns a client for the control socket at path.
func NewClient(path string) *Client {
	return &Client{httpc: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		},
	}}
}

// Status fetches daemon status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &st)
	return st, err
}

// Routes lists registered routes.
func (c *Client) Routes(ctx context.Context) ([]RouteInfo, error) {
	var routes []RouteInfo
	err := c.do(ctx, http.MethodGet, "/v1/routes", nil, &routes)
	return routes, err
}

// AddRoute registers a route from spec.
func (c *Client) AddRoute(ctx context.Context, spec RouteSpec) error {
	return c.do(ctx, http.MethodPost, "/v1/routes", spec, nil)
}

// RemoveRoute unregisters a route by name.
func (c *Client) RemoveRoute(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/routes/"+url.PathEscape(name), nil, nil)
}

// WaitReady blocks until the named route's backend accepts a connection, up
// to timeout. It is the CI wait primitive behind `portless doctor`.
func (c *Client) WaitReady(ctx context.Context, name string, timeout time.Duration) error {
	p := fmt.Sprintf("/v1/routes/%s/ready?timeout=%s", url.PathEscape(name), url.QueryEscape(timeout.String()))
	return c.do(ctx, http.MethodGet, p, nil, nil)
}

// do performs a JSON request against the control socket. Error responses are
// mapped back to portless sentinel errors where possible.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	// Host is arbitrary: the transport always dials the unix socket.
	req, err := http.NewRequestWithContext(ctx, method, "http://portless"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("control: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		msg := resp.Status
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			msg = e.Error
		}
		return fmt.Errorf("control: %s %s: %s: %w", method, path, msg, sentinelFor(resp.StatusCode, msg))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck // drain for keep-alive
	return nil
}

// sentinelFor maps API status codes back to portless sentinel errors so
// callers can errors.Is across the wire.
func sentinelFor(code int, msg string) error {
	switch {
	case code == http.StatusConflict:
		return portless.ErrRouteExists
	case code == http.StatusNotFound && strings.Contains(msg, "route"):
		return portless.ErrRouteNotFound
	default:
		return fmt.Errorf("http status %d", code)
	}
}
