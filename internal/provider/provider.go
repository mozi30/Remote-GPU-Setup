// Package provider abstracts GPU clouds (Vast.ai primary, RunPod fallback).
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
)

// Label tags every instance we create so reuse/recovery never touches
// instances the tool did not rent.
const Label = "gpu-backpack"

type Status int

const (
	StatusUnknown  Status = iota
	StatusStarting        // scheduled / pulling image
	StatusRunning
	StatusStopped // stopped or preempted (interruptible outbid); disk still exists
	StatusGone    // destroyed / not found
	StatusError
)

func (s Status) String() string {
	return [...]string{"unknown", "starting", "running", "stopped", "gone", "error"}[s]
}

type Offer struct {
	ID        string
	GPU       string
	VRAMGB    float64
	PricePerH float64
	DLPerf    float64
	Relia     float64
	CPUCores  float64
	RAMGB     float64
	UpMbps    float64
	DownMbps  float64
	Geo       string
	MinBid    float64
}

type Instance struct {
	ID         string
	Status     Status
	StatusMsg  string
	Host       string
	Port       int
	Direct     bool // false = provider SSH proxy (slower)
	GPU        string
	PricePerHr float64
	Label      string
}

type Provider interface {
	Name() string
	// Search returns rentable offers matching cfg, cheapest first.
	// A nil slice with nil error means "provider picks" (RunPod).
	Search(ctx context.Context, cfg *config.Config) ([]Offer, error)
	EnsureSSHKey(ctx context.Context, pubKey string) error
	Create(ctx context.Context, cfg *config.Config, offer *Offer, pubKey string) (id string, err error)
	Get(ctx context.Context, id string) (*Instance, error)
	ListOwned(ctx context.Context) ([]Instance, error)
	Start(ctx context.Context, id string) error
	Destroy(ctx context.Context, id string) error
	Logs(ctx context.Context, id string) string
}

func New(cfg *config.Config) (Provider, error) {
	switch cfg.Provider {
	case "vast":
		return NewVast(config.Secret(config.SecretVast)), nil
	case "runpod":
		return NewRunPod(config.Secret(config.SecretRunPod)), nil
	}
	return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
}

// ---- shared JSON-over-HTTP helper ----

type api struct {
	base   string
	key    string
	client *http.Client
}

type httpError struct {
	Code int
	Body string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

func isNotFound(err error) bool {
	he, ok := err.(*httpError)
	return ok && he.Code == http.StatusNotFound
}

func (a *api) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.client == nil {
		a.client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		msg := string(raw)
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return &httpError{resp.StatusCode, msg}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}
