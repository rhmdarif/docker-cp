package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const hubRegistry = "https://index.docker.io/v1/"

// splitImage separates repo and tag; a colon only counts as a tag separator
// after the last slash (so registry host ports are untouched).
func splitImage(image string) (repo, tag string) {
	if strings.Contains(image, "@") {
		return image, ""
	}
	i := strings.LastIndex(image, ":")
	if i > strings.LastIndex(image, "/") {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}

// Pull pulls image, writing "status id progress" lines to progress (may be nil).
func (c *Client) Pull(ctx context.Context, image, registryAuth string, progress io.Writer) error {
	repo, tag := splitImage(image)
	q := url.Values{"fromImage": {repo}}
	if tag != "" {
		q.Set("tag", tag)
	}
	hdr := map[string]string{}
	if registryAuth != "" {
		hdr["X-Registry-Auth"] = registryAuth
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create", q, nil, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Status   string `json:"status"`
			ID       string `json:"id"`
			Progress string `json:"progress"`
			Error    string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return errors.New(m.Error)
		}
		if progress != nil {
			line := strings.TrimSpace(strings.Join(strings.Fields(m.Status+" "+m.ID+" "+m.Progress), " "))
			fmt.Fprintln(progress, line)
		}
	}
}

// RegistryAuth builds the X-Registry-Auth value for image from a docker
// config.json. It returns "" when the file or the matching entry is absent.
func RegistryAuth(configPath, image string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", err
	}
	registry := hubRegistry
	first, _, hasSlash := strings.Cut(image, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		registry = first
	}
	entry, ok := cfg.Auths[registry]
	if !ok && registry != hubRegistry {
		entry, ok = cfg.Auths["https://"+registry]
	}
	if !ok || entry.Auth == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(entry.Auth)
	if err != nil {
		return "", fmt.Errorf("docker config: bad auth for %s: %w", registry, err)
	}
	user, pass, found := strings.Cut(string(raw), ":")
	if !found {
		return "", fmt.Errorf("docker config: bad auth for %s", registry)
	}
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass, "serveraddress": registry})
	return base64.URLEncoding.EncodeToString(b), nil
}
