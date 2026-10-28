// Package state persists the active GPU session so a crashed/closed tool can
// find its instance again. It is written the moment an instance is rented.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
)

// Phase records how far session setup got. Order matters: the safety rules
// compare phases (e.g. never push to R2 before PhaseHydrated).
type Phase int

const (
	PhaseNone     Phase = iota
	PhaseRented         // instance exists, billing runs
	PhaseSSH            // ssh reachable, remote scripts installed
	PhaseHydrated       // R2 workspace pulled successfully -> pushing is safe
	PhaseReady          // env built, autosave running, VS Code opened
)

func (p Phase) String() string {
	return [...]string{"none", "rented", "ssh-ok", "hydrated", "ready"}[p]
}

type Session struct {
	Provider   string    `json:"provider"`
	InstanceID string    `json:"instance_id"`
	Host       string    `json:"host,omitempty"`
	Port       int       `json:"port,omitempty"`
	GPU        string    `json:"gpu,omitempty"`
	PricePerHr float64   `json:"price_per_hr,omitempty"`
	Phase      Phase     `json:"phase"`
	RentedAt   time.Time `json:"rented_at"`
	LastBackup time.Time `json:"last_backup_ok_at,omitempty"`
}

func path() (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "state.json"), nil
}

// Load returns the tracked session or nil when there is none.
func Load() (*Session, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.InstanceID == "" {
		return nil, nil
	}
	return &s, nil
}

// Save writes atomically (temp file + rename) so a crash never leaves a
// half-written state file that would hide a running, billing instance.
func (s *Session) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func Clear() error {
	p, err := path()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
