package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CaddyError is an error reported by the Caddy Admin API ({"error": ...}).
type CaddyError struct{ Message string }

func (e *CaddyError) Error() string { return e.Message }

// Admin is a minimal client for the Caddy Admin API.
type Admin struct {
	base string
	http *http.Client
}

// NewAdmin returns a client for the admin endpoint at base, e.g. "http://localhost:2019".
func NewAdmin(base string) *Admin {
	return &Admin{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

func (a *Admin) post(ctx context.Context, path, caddyfile string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewBufferString(caddyfile))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/caddyfile")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("caddy admin: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("caddy admin: %w", err)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return nil, &CaddyError{Message: e.Error}
		}
		return nil, &CaddyError{Message: fmt.Sprintf("caddy admin: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))}
	}
	return body, nil
}

// Load applies a Caddyfile via POST /load.
func (a *Admin) Load(ctx context.Context, caddyfile string) error {
	_, err := a.post(ctx, "/load", caddyfile)
	return err
}

// Adapt converts a Caddyfile to JSON config via POST /adapt without applying it.
func (a *Admin) Adapt(ctx context.Context, caddyfile string) (json.RawMessage, error) {
	body, err := a.post(ctx, "/adapt", caddyfile)
	if err != nil {
		return nil, err
	}
	var r struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("caddy admin: respons /adapt tidak valid: %w", err)
	}
	return r.Result, nil
}
