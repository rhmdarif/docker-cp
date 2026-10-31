package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ComposeProjectLabel marks containers managed by docker compose.
const ComposeProjectLabel = "com.docker.compose.project"

// recreatePlan is what Recreate needs from the old container's inspect JSON.
type recreatePlan struct {
	name, image string
	labels      map[string]string
	create      []byte                     // POST /containers/create body
	connect     map[string]json.RawMessage // extra networks -> EndpointConfig
}

// planRecreate builds the create body from the raw inspect Config, HostConfig
// and network endpoints, so settings the panel's form does not know about
// (entrypoint, user, limits, caps, devices, extra hosts, aliases, ...) are
// kept. Only Image is taken as-is from Config; an auto-assigned Hostname (the
// old short ID) and the old short-ID alias are dropped. API v1.43 accepts a
// single endpoint at create, so the network named by NetworkMode goes into
// NetworkingConfig and every other network is connected before start.
func planRecreate(raw []byte) (recreatePlan, error) {
	var in struct {
		ID              string `json:"Id"`
		Name            string
		Config          map[string]json.RawMessage
		HostConfig      json.RawMessage
		NetworkSettings struct {
			Networks map[string]map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return recreatePlan{}, err
	}
	p := recreatePlan{name: strings.TrimPrefix(in.Name, "/"), connect: map[string]json.RawMessage{}}
	if err := json.Unmarshal(in.Config["Image"], &p.image); err != nil || p.image == "" {
		return recreatePlan{}, fmt.Errorf("inspect tanpa Config.Image")
	}
	json.Unmarshal(in.Config["Labels"], &p.labels)
	short := in.ID
	if len(short) > 12 {
		short = short[:12]
	}
	var hostname string
	json.Unmarshal(in.Config["Hostname"], &hostname)
	if hostname == short {
		delete(in.Config, "Hostname")
	}
	var hc struct{ NetworkMode string }
	if len(in.HostConfig) > 0 {
		if err := json.Unmarshal(in.HostConfig, &hc); err != nil {
			return recreatePlan{}, fmt.Errorf("HostConfig: %w", err)
		}
	}
	primary := hc.NetworkMode
	if primary == "" || primary == "default" {
		primary = "bridge"
	}

	endpoints := map[string]json.RawMessage{}
	for name, ep := range in.NetworkSettings.Networks {
		out := map[string]json.RawMessage{}
		for _, k := range []string{"IPAMConfig", "Links", "DriverOpts"} {
			if v, ok := ep[k]; ok && string(v) != "null" && string(v) != `""` {
				out[k] = v
			}
		}
		var aliases []string
		json.Unmarshal(ep["Aliases"], &aliases)
		var keep []string
		for _, a := range aliases {
			if a != short && a != in.ID {
				keep = append(keep, a)
			}
		}
		if len(keep) > 0 {
			b, _ := json.Marshal(keep)
			out["Aliases"] = b
		}
		b, err := json.Marshal(out)
		if err != nil {
			return recreatePlan{}, err
		}
		if name == primary {
			endpoints[name] = b
		} else if !strings.HasPrefix(hc.NetworkMode, "container:") && hc.NetworkMode != "host" && hc.NetworkMode != "none" {
			p.connect[name] = b
		}
	}

	body := map[string]json.RawMessage{}
	for k, v := range in.Config {
		body[k] = v
	}
	if len(in.HostConfig) > 0 {
		body["HostConfig"] = in.HostConfig
	}
	nc, err := json.Marshal(map[string]any{"EndpointsConfig": endpoints})
	if err != nil {
		return recreatePlan{}, err
	}
	body["NetworkingConfig"] = nc
	if p.create, err = json.Marshal(body); err != nil {
		return recreatePlan{}, err
	}
	return p, nil
}

// createRaw posts a prepared create body and returns the new container's ID.
func (c *Client) createRaw(ctx context.Context, name string, body []byte) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, bytes.NewReader(body), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct{ Id string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Id, nil
}

// connectNetwork attaches container id to network with the given EndpointConfig.
func (c *Client) connectNetwork(ctx context.Context, network, id string, endpoint json.RawMessage) error {
	body, err := json.Marshal(map[string]any{"Container": id, "EndpointConfig": endpoint})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/networks/"+esc(network)+"/connect", nil, bytes.NewReader(body), nil)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Recreate pulls the container's image and replaces the container with a new
// one built from the old container's full inspect config. On failure the old
// container is restored.
func (c *Client) Recreate(ctx context.Context, id, registryAuth string, progress io.Writer) (string, error) {
	raw, err := c.inspectRaw(ctx, id)
	if err != nil {
		return "", err
	}
	plan, err := planRecreate(raw)
	if err != nil {
		return "", err
	}
	if plan.labels[ComposeProjectLabel] != "" {
		return "", fmt.Errorf("container milik stack compose %q: perbarui lewat Stacks", plan.labels[ComposeProjectLabel])
	}
	if err := c.Pull(ctx, plan.image, registryAuth, progress); err != nil {
		return "", fmt.Errorf("pull %s: %w", plan.image, err)
	}
	if err := c.Stop(ctx, id); err != nil {
		return "", fmt.Errorf("stop: %w", err)
	}
	// restore puts the old container back; it ignores cancellation of ctx so a
	// dropped request cannot leave the service down, and reports its failures.
	restore := func(cause error, cleanup ...func(context.Context) error) error {
		rctx := context.WithoutCancel(ctx)
		errs := []error{cause}
		for _, f := range cleanup {
			errs = append(errs, f(rctx))
		}
		if err := c.Rename(rctx, id, plan.name); err != nil {
			errs = append(errs, fmt.Errorf("rollback rename: %w", err))
		}
		if err := c.Start(rctx, id); err != nil {
			errs = append(errs, fmt.Errorf("rollback start: %w", err))
		}
		return errors.Join(errs...)
	}
	if err := c.Rename(ctx, id, fmt.Sprintf("%s-old-%d", plan.name, time.Now().Unix())); err != nil {
		if serr := c.Start(context.WithoutCancel(ctx), id); serr != nil {
			return "", errors.Join(fmt.Errorf("rename: %w", err), fmt.Errorf("rollback start: %w", serr))
		}
		return "", fmt.Errorf("rename: %w", err)
	}
	newID, err := c.createRaw(ctx, plan.name, plan.create)
	if err != nil {
		return "", restore(fmt.Errorf("create: %w", err))
	}
	removeNew := func(rctx context.Context) error {
		if rerr := c.Remove(rctx, newID, false); rerr != nil {
			return fmt.Errorf("rollback remove new: %w", rerr)
		}
		return nil
	}
	for network, ep := range plan.connect {
		if err := c.connectNetwork(ctx, network, newID, ep); err != nil {
			return "", restore(fmt.Errorf("connect %s: %w", network, err), removeNew)
		}
	}
	if err := c.Start(ctx, newID); err != nil {
		return "", restore(fmt.Errorf("start: %w", err), removeNew)
	}
	if err := c.Remove(ctx, id, false); err != nil {
		return newID, fmt.Errorf("remove old: %w", err)
	}
	return newID, nil
}
