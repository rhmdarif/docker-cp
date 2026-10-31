// Package store persists panel state (admin, settings, Caddy sites) as an
// atomically written JSON file.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type State struct {
	Admin    Admin    `json:"admin"`
	Settings Settings `json:"settings"`
	Sites    []Site   `json:"sites"`
}

type Admin struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

type Settings struct {
	PanelDomain string `json:"panel_domain"`
	PanelPort   int    `json:"panel_port"`
	ACMEEmail   string `json:"acme_email"`
}

type Site struct {
	ID          string   `json:"id"`
	Domains     []string `json:"domains"`
	Target      Target   `json:"target"`
	Custom      string   `json:"custom"`
	Manual      bool     `json:"manual"`
	ManualBlock string   `json:"manual_block"`
}

type Target struct {
	Container     string `json:"container"`
	ContainerPort int    `json:"container_port"`
	Host          string `json:"host"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	state State
}

func defaultState() State {
	return State{Settings: Settings{PanelPort: 8800}, Sites: []Site{}}
}

// Open loads the state at path. A missing file yields the default state,
// which is not written until the first successful Update.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: path, state: defaultState()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Get returns a deep copy of the current state.
func (s *Store) Get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _ := clone(s.state)
	return c
}

// Update applies fn to a copy of the state and persists it atomically.
// If fn or the write fails, the in-memory and on-disk state are unchanged.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := clone(s.state)
	if err != nil {
		return err
	}
	if err := fn(&next); err != nil {
		return err
	}
	if err := s.write(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func clone(st State) (State, error) {
	var out State
	b, err := json.Marshal(st)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func (s *Store) write(st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// NewID returns 8 random bytes as hex.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
