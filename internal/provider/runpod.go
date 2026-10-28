package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
)

// RunPod is the fallback backend (REST API v1). It has no offer search and no
// network-speed filters: RunPod picks the machine from the GPU type list.
type RunPod struct{ api }

func NewRunPod(key string) *RunPod {
	return &RunPod{api{base: "https://rest.runpod.io/v1", key: key}}
}

func (r *RunPod) Name() string { return "runpod" }

var runpodGPUIDs = map[string]string{
	"RTX 5090":       "NVIDIA GeForce RTX 5090",
	"RTX 5080":       "NVIDIA GeForce RTX 5080",
	"RTX 4090":       "NVIDIA GeForce RTX 4090",
	"RTX 4080 SUPER": "NVIDIA GeForce RTX 4080 SUPER",
	"RTX 4080":       "NVIDIA GeForce RTX 4080",
	"RTX 3090":       "NVIDIA GeForce RTX 3090",
}

func (r *RunPod) Search(context.Context, *config.Config) ([]Offer, error) { return nil, nil }

// EnsureSSHKey: RunPod gets the key via the AUTHORIZED_KEY env (image entrypoint).
func (r *RunPod) EnsureSSHKey(context.Context, string) error { return nil }

func (r *RunPod) Create(ctx context.Context, cfg *config.Config, _ *Offer, pubKey string) (string, error) {
	var ids []string
	for _, n := range cfg.GPUNames {
		if id, ok := runpodGPUIDs[n]; ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("none of the GPU wishlist entries are known RunPod types")
	}
	body := map[string]any{
		"name":              Label,
		"imageName":         cfg.Image,
		"gpuTypeIds":        ids,
		"gpuCount":          1,
		"containerDiskInGb": cfg.DiskGB,
		"volumeInGb":        cfg.DiskGB,
		"volumeMountPath":   "/workspace",
		"ports":             []string{"22/tcp"},
		"env":               map[string]string{"AUTHORIZED_KEY": strings.TrimSpace(pubKey)},
		"dockerEntrypoint":  []string{"/usr/local/bin/entrypoint.sh"},
		"interruptible":     cfg.Mode == "interruptible",
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := r.do(ctx, "POST", "/pods", body, &resp); err != nil {
		return "", fmt.Errorf("runpod create: %w", err)
	}
	if resp.ID == "" {
		return "", fmt.Errorf("runpod create returned no pod id")
	}
	return resp.ID, nil
}

type runpodPod struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	DesiredStatus string         `json:"desiredStatus"`
	PublicIP      string         `json:"publicIp"`
	PortMappings  map[string]int `json:"portMappings"`
	CostPerHr     float64        `json:"costPerHr"`
	GPU           struct {
		DisplayName string `json:"displayName"`
	} `json:"gpu"`
}

func (p *runpodPod) toInstance() *Instance {
	in := &Instance{ID: p.ID, GPU: p.GPU.DisplayName, PricePerHr: p.CostPerHr, Label: p.Name}
	switch strings.ToUpper(p.DesiredStatus) {
	case "RUNNING":
		in.Status = StatusStarting
		if p.PublicIP != "" && p.PublicIP != "0.0.0.0" && p.PortMappings["22"] > 0 {
			in.Status = StatusRunning
			in.Host, in.Port, in.Direct = p.PublicIP, p.PortMappings["22"], true
		}
	case "EXITED":
		in.Status = StatusStopped
	case "TERMINATED":
		in.Status = StatusGone
	}
	return in
}

func (r *RunPod) Get(ctx context.Context, id string) (*Instance, error) {
	var p runpodPod
	err := r.do(ctx, "GET", "/pods/"+id, nil, &p)
	if isNotFound(err) || (err == nil && p.ID == "") {
		return &Instance{ID: id, Status: StatusGone}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runpod get %s: %w", id, err)
	}
	return p.toInstance(), nil
}

func (r *RunPod) ListOwned(ctx context.Context) ([]Instance, error) {
	var pods []runpodPod
	if err := r.do(ctx, "GET", "/pods", nil, &pods); err != nil {
		return nil, fmt.Errorf("runpod list: %w", err)
	}
	var out []Instance
	for i := range pods {
		if pods[i].Name == Label {
			out = append(out, *pods[i].toInstance())
		}
	}
	return out, nil
}

func (r *RunPod) Start(ctx context.Context, id string) error {
	return r.do(ctx, "POST", "/pods/"+id+"/start", nil, nil)
}

func (r *RunPod) Destroy(ctx context.Context, id string) error {
	err := r.do(ctx, "DELETE", "/pods/"+id, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (r *RunPod) Logs(context.Context, string) string { return "" }
