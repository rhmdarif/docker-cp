package docker

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Recreate pulls the container's image and replaces the container with a new
// one built from the same spec. On failure the old container is restored.
func (c *Client) Recreate(ctx context.Context, id, registryAuth string, progress io.Writer) (string, error) {
	d, err := c.Inspect(ctx, id)
	if err != nil {
		return "", err
	}
	spec := d.Spec
	if err := c.Pull(ctx, spec.Image, registryAuth, progress); err != nil {
		return "", fmt.Errorf("pull %s: %w", spec.Image, err)
	}
	if err := c.Stop(ctx, id); err != nil {
		return "", fmt.Errorf("stop: %w", err)
	}
	restore := func() {
		// best effort: the caller already gets the primary error
		c.Rename(ctx, id, spec.Name)
		c.Start(ctx, id)
	}
	if err := c.Rename(ctx, id, fmt.Sprintf("%s-old-%d", spec.Name, time.Now().Unix())); err != nil {
		c.Start(ctx, id)
		return "", fmt.Errorf("rename: %w", err)
	}
	newID, err := c.Create(ctx, spec)
	if err != nil {
		restore()
		return "", fmt.Errorf("create: %w", err)
	}
	if err := c.Start(ctx, newID); err != nil {
		c.Remove(ctx, newID, false)
		restore()
		return "", fmt.Errorf("start: %w", err)
	}
	if err := c.Remove(ctx, id, false); err != nil {
		return newID, fmt.Errorf("remove old: %w", err)
	}
	return newID, nil
}
