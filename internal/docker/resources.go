package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

type Image struct {
	ID      string
	Tags    []string
	Size    int64
	Created time.Time
	InUse   bool
}

type Volume struct {
	Name, Driver, Mountpoint string
	InUse                    bool
}

type Network struct {
	ID, Name, Driver string
	Builtin          bool
}

// usage returns the image IDs and volume names referenced by any container.
func (c *Client) usage(ctx context.Context) (images, volumes map[string]bool, err error) {
	var raw []struct {
		ImageID string
		Mounts  []struct{ Name string }
	}
	if err = c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, &raw); err != nil {
		return nil, nil, err
	}
	images, volumes = map[string]bool{}, map[string]bool{}
	for _, r := range raw {
		images[r.ImageID] = true
		for _, m := range r.Mounts {
			if m.Name != "" {
				volumes[m.Name] = true
			}
		}
	}
	return images, volumes, nil
}

func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	used, _, err := c.usage(ctx)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID       string `json:"Id"`
		RepoTags []string
		Size     int64
		Created  int64
	}
	if err := c.getJSON(ctx, "/images/json", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(raw))
	for _, r := range raw {
		out = append(out, Image{ID: r.ID, Tags: r.RepoTags, Size: r.Size, Created: time.Unix(r.Created, 0), InUse: used[r.ID]})
	}
	return out, nil
}

func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	_, used, err := c.usage(ctx)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Volumes []struct{ Name, Driver, Mountpoint string }
	}
	if err := c.getJSON(ctx, "/volumes", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(raw.Volumes))
	for _, v := range raw.Volumes {
		out = append(out, Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, InUse: used[v.Name]})
	}
	return out, nil
}

func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	var raw []struct {
		ID     string `json:"Id"`
		Name   string
		Driver string
	}
	if err := c.getJSON(ctx, "/networks", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(raw))
	for _, r := range raw {
		out = append(out, Network{ID: r.ID, Name: r.Name, Driver: r.Driver, Builtin: r.Name == "bridge" || r.Name == "host" || r.Name == "none"})
	}
	return out, nil
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	return c.call(ctx, http.MethodDelete, "/images/"+esc(id), q)
}

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodDelete, "/volumes/"+esc(name), nil)
}

func (c *Client) RemoveNetwork(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodDelete, "/networks/"+esc(name), nil)
}

// PruneImages removes every image not used by a container (not only dangling).
func (c *Client) PruneImages(ctx context.Context) (uint64, error) {
	q := url.Values{"filters": {`{"dangling":["false"]}`}}
	resp, err := c.do(ctx, http.MethodPost, "/images/prune", q, nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct{ SpaceReclaimed uint64 }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.SpaceReclaimed, nil
}

// PruneVolumes removes all unused volumes, including named ones.
func (c *Client) PruneVolumes(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/volumes/prune", url.Values{"filters": {`{"all":["true"]}`}})
}

func (c *Client) PruneNetworks(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/networks/prune", nil)
}
