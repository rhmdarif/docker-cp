// Package docker is a minimal Docker Engine API client over the unix socket.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const baseURL = "http://docker/v1.43"

// Client talks to the Docker Engine API through a unix socket.
type Client struct {
	hc       *http.Client
	sockPath string
}

// New returns a Client that dials the unix socket at sockPath.
func New(sockPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	return &Client{hc: &http.Client{Transport: tr}, sockPath: sockPath}
}

// APIError is an error response from the Docker daemon.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("docker: %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is a 404 from the daemon.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// do sends a request and returns the response; status >= 400 becomes *APIError.
// The caller must close the body of a non-error response.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, hdr map[string]string) (*http.Response, error) {
	u := baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var m struct {
			Message string `json:"message"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &m) == nil && m.Message != "" {
			msg = m.Message
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	return resp, nil
}

// call performs a request, discarding the response body.
func (c *Client) call(ctx context.Context, method, path string, q url.Values) error {
	resp, err := c.do(ctx, method, path, q, nil, nil)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// getJSON performs a GET and decodes the JSON response into v.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, v any) error {
	resp, err := c.do(ctx, http.MethodGet, path, q, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// Ping checks that the daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	return c.call(ctx, http.MethodGet, "/_ping", nil)
}
