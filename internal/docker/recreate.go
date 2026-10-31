package docker

import (
	"context"
	"errors"
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
	// restore puts the old container back; it ignores cancellation of ctx so a
	// dropped request cannot leave the service down, and reports its failures.
	restore := func(cause error, cleanup ...func(context.Context) error) error {
		rctx := context.WithoutCancel(ctx)
		errs := []error{cause}
		for _, f := range cleanup {
			errs = append(errs, f(rctx))
		}
		if err := c.Rename(rctx, id, spec.Name); err != nil {
			errs = append(errs, fmt.Errorf("rollback rename: %w", err))
		}
		if err := c.Start(rctx, id); err != nil {
			errs = append(errs, fmt.Errorf("rollback start: %w", err))
		}
		return errors.Join(errs...)
	}
	if err := c.Rename(ctx, id, fmt.Sprintf("%s-old-%d", spec.Name, time.Now().Unix())); err != nil {
		if serr := c.Start(context.WithoutCancel(ctx), id); serr != nil {
			return "", errors.Join(fmt.Errorf("rename: %w", err), fmt.Errorf("rollback start: %w", serr))
		}
		return "", fmt.Errorf("rename: %w", err)
	}
	newID, err := c.Create(ctx, spec)
	if err != nil {
		return "", restore(fmt.Errorf("create: %w", err))
	}
	if err := c.Start(ctx, newID); err != nil {
		return "", restore(fmt.Errorf("start: %w", err), func(rctx context.Context) error {
			if rerr := c.Remove(rctx, newID, false); rerr != nil {
				return fmt.Errorf("rollback remove new: %w", rerr)
			}
			return nil
		})
	}
	if err := c.Remove(ctx, id, false); err != nil {
		return newID, fmt.Errorf("remove old: %w", err)
	}
	return newID, nil
}
