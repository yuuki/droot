// Package docker is a minimal client for the Docker Engine API.
//
// It implements only the handful of endpoints that droot needs, using nothing
// but the Go standard library, so that droot does not have to depend on the
// (very large) github.com/docker/docker module. Any daemon that speaks the
// Docker Engine API works, including Docker Desktop, Colima, OrbStack and
// Podman's Docker-compatible socket.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// fallbackAPIVersion is used when the daemon does not advertise its API
	// version. 1.24 is the oldest version still accepted by most daemons and
	// covers every endpoint droot uses.
	fallbackAPIVersion = "1.24"

	userAgent = "droot"
)

// Client talks to a Docker daemon.
type Client struct {
	httpClient *http.Client
	baseURL    string // scheme://host[/path], without a trailing slash
	host       string // the endpoint as configured by the user, for messages

	mu         sync.Mutex
	apiVersion string // "" until negotiated
}

// NewClient returns a client for the given endpoint. The API version is taken
// from ep.APIVersion or negotiated with the daemon on first use.
func NewClient(ep Endpoint) (*Client, error) {
	u, err := url.Parse(ep.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid docker host %q: %w", ep.Host, err)
	}

	transport := &http.Transport{
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0, // exporting a large image may take a while
	}
	c := &Client{
		// No overall timeout: exports stream arbitrarily large archives.
		// Cancellation is done through contexts.
		httpClient: &http.Client{Transport: transport},
		host:       ep.Host,
		apiVersion: ep.APIVersion,
	}

	switch u.Scheme {
	case "unix":
		socket := u.Host + u.Path
		if socket == "" {
			return nil, fmt.Errorf("invalid docker host %q: missing socket path", ep.Host)
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}
		// The host part is irrelevant for unix sockets, but must be valid.
		c.baseURL = "http://api.moby.localhost"
	case "tcp", "http", "https":
		if u.Host == "" {
			return nil, fmt.Errorf("invalid docker host %q: missing address", ep.Host)
		}
		useTLS := ep.UseTLS || u.Scheme == "https"
		host := u.Host
		if u.Port() == "" {
			port := "2375"
			if useTLS {
				port = "2376"
			}
			host = net.JoinHostPort(u.Hostname(), port)
		}
		scheme := "http"
		if useTLS {
			scheme = "https"
			if transport.TLSClientConfig, err = ep.tlsConfig(); err != nil {
				return nil, err
			}
		}
		transport.Proxy = http.ProxyFromEnvironment
		c.baseURL = scheme + "://" + host + strings.TrimRight(u.Path, "/")
	case "ssh":
		return nil, fmt.Errorf("docker host %q: ssh:// endpoints are not supported by droot; "+
			"forward the remote socket instead, e.g. `ssh -nNT -L /tmp/docker.sock:/var/run/docker.sock HOST` "+
			"and set DOCKER_HOST=unix:///tmp/docker.sock", ep.Host)
	default:
		return nil, fmt.Errorf("docker host %q: unsupported scheme %q (supported: unix, tcp, http, https)", ep.Host, u.Scheme)
	}
	return c, nil
}

// NewClientFromEnv returns a client configured the same way as the docker CLI:
// from DOCKER_HOST, DOCKER_TLS_VERIFY, DOCKER_CERT_PATH, DOCKER_API_VERSION,
// DOCKER_CONTEXT and the current docker context. See EndpointFromEnv.
func NewClientFromEnv() (*Client, error) {
	ep, err := EndpointFromEnv()
	if err != nil {
		return nil, err
	}
	return NewClient(ep)
}

// Host returns the daemon address this client talks to.
func (c *Client) Host() string { return c.host }

// APIError is an error response returned by the Docker daemon.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("docker daemon: %s (HTTP %d)", e.Message, e.StatusCode)
}

// IsNotFound reports whether err is a "not found" error from the daemon.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// Ping checks that the daemon is reachable and negotiates the API version
// unless one was configured explicitly.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.send(ctx, http.MethodGet, c.baseURL+"/_ping", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apiVersion == "" {
		// Use the daemon's own (latest) version: the endpoints droot uses
		// have been stable for years, and pinning an old version would be
		// rejected by daemons that raised their minimum supported version.
		c.apiVersion = resp.Header.Get("Api-Version")
		if c.apiVersion == "" {
			c.apiVersion = fallbackAPIVersion
		}
	}
	return nil
}

// APIVersion returns the API version in use, negotiating it if necessary.
func (c *Client) APIVersion(ctx context.Context) (string, error) {
	c.mu.Lock()
	v := c.apiVersion
	c.mu.Unlock()
	if v != "" {
		return v, nil
	}
	if err := c.Ping(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.apiVersion, nil
}

// do sends a request to a versioned API endpoint. path must already be
// escaped. When in is non-nil it is sent as a JSON body.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in any) (*http.Response, error) {
	version, err := c.APIVersion(ctx)
	if err != nil {
		return nil, err
	}
	u := c.baseURL + "/v" + version + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	return c.send(ctx, method, u, body)
}

func (c *Client) send(ctx context.Context, method, u string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("cannot connect to the Docker daemon at %s (is the daemon running?): %w", c.host, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		return nil, decodeAPIError(resp)
	}
	return resp, nil
}

func decodeAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var payload struct {
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(b, &payload) == nil && payload.Message != "" {
		msg = payload.Message
	} else if s := strings.TrimSpace(string(b)); s != "" {
		msg = s
	} else {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg}
}

func decodeJSON(r io.Reader, out any) error {
	if err := json.NewDecoder(r).Decode(out); err != nil {
		return fmt.Errorf("decode docker daemon response: %w", err)
	}
	return nil
}
