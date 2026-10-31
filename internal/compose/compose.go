// Package compose manages docker compose stacks stored on disk and runs
// `docker compose` as a subprocess.
package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"

	"docker-cp/internal/docker"
	"docker-cp/internal/procenv"
)

type Action string

const (
	ActionUp   Action = "up"
	ActionStop Action = "stop"
	ActionDown Action = "down"
	ActionPull Action = "pull"
)

type Stack struct {
	Name     string
	Managed  bool
	Services int
	Running  int
}

type Manager struct {
	dir, bin string
}

// NewManager stores stacks under dir; bin is the docker binary (default "docker").
func NewManager(dir, bin string) *Manager {
	if bin == "" {
		bin = "docker"
	}
	return &Manager{dir: dir, bin: bin}
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidName reports whether name is a valid stack name. "new" is reserved:
// GET /stacks/new is the create form and would shadow such a stack.
func ValidName(name string) bool { return name != "new" && nameRe.MatchString(name) }

func checkName(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("nama stack tidak valid: %q", name)
	}
	return nil
}

func (m *Manager) stackDir(name string) string { return filepath.Join(m.dir, name) }

// List returns managed stacks (directories in dir) merged with external
// compose projects found in the container labels, sorted by name.
func (m *Manager) List(cs []docker.ContainerSummary) ([]Stack, error) {
	byName := map[string]*Stack{}
	entries, err := os.ReadDir(m.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && ValidName(e.Name()) {
			byName[e.Name()] = &Stack{Name: e.Name(), Managed: true}
		}
	}
	for _, c := range cs {
		if c.Project == "" {
			continue
		}
		s := byName[c.Project]
		if s == nil {
			s = &Stack{Name: c.Project}
			byName[c.Project] = s
		}
		s.Services++
		if c.State == "running" {
			s.Running++
		}
	}
	out := make([]Stack, 0, len(byName))
	for _, s := range byName {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Manager) Read(name string) (compose, env string, err error) {
	if err := checkName(name); err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(filepath.Join(m.stackDir(name), "docker-compose.yml"))
	if err != nil {
		return "", "", err
	}
	e, err := os.ReadFile(filepath.Join(m.stackDir(name), ".env"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return string(b), string(e), nil
}

// Write stores the compose file and .env; an empty env removes .env.
func (m *Manager) Write(name, compose, env string) error {
	if err := checkName(name); err != nil {
		return err
	}
	d := m.stackDir(name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(d, "docker-compose.yml"), compose, 0o644); err != nil {
		return err
	}
	envPath := filepath.Join(d, ".env")
	if env == "" {
		if err := os.Remove(envPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFile(envPath, env, 0o600)
}

func writeFile(path, content string, mode os.FileMode) error {
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return err
	}
	return os.Chmod(path, mode) // WriteFile keeps the mode of an existing file
}

// Run executes the action; pull is followed by up. Output goes to out.
func (m *Manager) Run(ctx context.Context, name string, a Action, out io.Writer) error {
	if err := checkName(name); err != nil {
		return err
	}
	var steps [][]string
	switch a {
	case ActionUp:
		steps = [][]string{{"up", "-d", "--remove-orphans"}}
	case ActionStop:
		steps = [][]string{{"stop"}}
	case ActionDown:
		steps = [][]string{{"down"}}
	case ActionPull:
		steps = [][]string{{"pull"}, {"up", "-d", "--remove-orphans"}}
	default:
		return fmt.Errorf("aksi tidak dikenal: %q", a)
	}
	if out == nil {
		out = io.Discard
	}
	for _, s := range steps {
		if err := m.compose(ctx, name, s, out); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) compose(ctx context.Context, name string, args []string, out io.Writer) error {
	d := m.stackDir(name)
	full := append([]string{"compose", "--project-directory", d, "-p", name,
		"-f", filepath.Join(d, "docker-compose.yml")}, args...)
	cmd := exec.CommandContext(ctx, m.bin, full...)
	cmd.Env = procenv.Child()
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("docker compose %s gagal (exit %d)", args[0], ee.ExitCode())
	}
	if err != nil {
		return fmt.Errorf("docker compose %s: %w", args[0], err)
	}
	return nil
}

// Delete runs `down` and then removes the stack directory.
func (m *Manager) Delete(ctx context.Context, name string, out io.Writer) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := m.Run(ctx, name, ActionDown, out); err != nil {
		return err
	}
	return os.RemoveAll(m.stackDir(name))
}

func StartProject(ctx context.Context, dc *docker.Client, cs []docker.ContainerSummary, project string) error {
	return eachInProject(ctx, cs, project, dc.Start)
}

func StopProject(ctx context.Context, dc *docker.Client, cs []docker.ContainerSummary, project string) error {
	return eachInProject(ctx, cs, project, dc.Stop)
}

func eachInProject(ctx context.Context, cs []docker.ContainerSummary, project string, f func(context.Context, string) error) error {
	n := 0
	for _, c := range cs {
		if c.Project != project {
			continue
		}
		n++
		if err := f(ctx, c.ID); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	if n == 0 {
		return fmt.Errorf("project %q tidak punya container", project)
	}
	return nil
}
