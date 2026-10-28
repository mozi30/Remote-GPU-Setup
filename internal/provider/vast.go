package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
)

// Vast talks to the Vast.ai REST API directly (no vastai CLI needed).
type Vast struct{ api }

func NewVast(key string) *Vast {
	return &Vast{api{base: "https://console.vast.ai/api/v0", key: key}}
}

func (v *Vast) Name() string { return "vast" }

type vastOffer struct {
	ID          int64   `json:"id"`
	GPUName     string  `json:"gpu_name"`
	GPURAM      float64 `json:"gpu_ram"` // MB
	DPHTotal    float64 `json:"dph_total"`
	DLPerf      float64 `json:"dlperf"`
	Reliability float64 `json:"reliability2"`
	CPUCores    float64 `json:"cpu_cores_effective"`
	CPURAM      float64 `json:"cpu_ram"` // MB
	InetUp      float64 `json:"inet_up"`
	InetDown    float64 `json:"inet_down"`
	Geo         string  `json:"geolocation"`
	MinBid      float64 `json:"min_bid"`
}

func (v *Vast) Search(ctx context.Context, cfg *config.Config) ([]Offer, error) {
	gte := func(x any) map[string]any { return map[string]any{"gte": x} }
	q := map[string]any{
		"gpu_name":          map[string]any{"in": cfg.GPUNames}, // API uses spaces: "RTX 4090"
		"num_gpus":          map[string]any{"eq": 1},
		"rentable":          map[string]any{"eq": true},
		"rented":            map[string]any{"eq": false},
		"verified":          map[string]any{"eq": true},
		"external":          map[string]any{"eq": false},
		"disk_space":        gte(cfg.DiskGB),
		"inet_down":         gte(cfg.MinDownMbps),
		"inet_up":           gte(cfg.MinUpMbps),
		"reliability2":      gte(cfg.MinRelia),
		"direct_port_count": gte(cfg.MinDirect),
		"allocated_storage": cfg.DiskGB, // so dph_total includes storage cost
		"order":             [][]string{{"dph_total", "asc"}},
		"limit":             64,
	}
	if cfg.MinCUDA > 0 {
		q["cuda_max_good"] = gte(cfg.MinCUDA)
	}
	if cfg.Mode == "interruptible" {
		q["type"] = "bid"
		q["min_bid"] = map[string]any{"lte": cfg.MaxPrice}
	} else {
		q["type"] = "on-demand"
		q["dph_total"] = map[string]any{"lte": cfg.MaxPrice}
	}

	var resp struct {
		Offers []vastOffer `json:"offers"`
	}
	if err := v.do(ctx, "POST", "/bundles/", q, &resp); err != nil {
		return nil, fmt.Errorf("vast search: %w", err)
	}
	out := make([]Offer, 0, len(resp.Offers))
	for _, o := range resp.Offers {
		price := o.DPHTotal
		if cfg.Mode == "interruptible" {
			price = bidPrice(o.MinBid, cfg.MaxPrice)
		}
		out = append(out, Offer{
			ID: strconv.FormatInt(o.ID, 10), GPU: o.GPUName, VRAMGB: o.GPURAM / 1024,
			PricePerH: price, DLPerf: o.DLPerf, Relia: o.Reliability, CPUCores: o.CPUCores,
			RAMGB: o.CPURAM / 1024, UpMbps: o.InetUp, DownMbps: o.InetDown, Geo: o.Geo, MinBid: o.MinBid,
		})
	}
	return out, nil
}

// bidPrice bids 15% over the current minimum so we are not outbid instantly,
// but never above the configured cap.
func bidPrice(minBid, maxPrice float64) float64 {
	return math.Round(math.Min(minBid*1.15, maxPrice)*1000) / 1000
}

func (v *Vast) EnsureSSHKey(ctx context.Context, pubKey string) error {
	pubKey = strings.TrimSpace(pubKey)
	fields := strings.Fields(pubKey)
	if len(fields) < 2 {
		return fmt.Errorf("malformed public key")
	}
	// Already registered? Compare the base64 body; tolerate unknown response shapes.
	var existing json.RawMessage
	if err := v.do(ctx, "GET", "/ssh/", nil, &existing); err == nil &&
		strings.Contains(string(existing), fields[1]) {
		return nil
	}
	err := v.do(ctx, "POST", "/ssh/", map[string]string{"ssh_key": pubKey}, nil)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "exist") {
		return nil
	}
	return err
}

func (v *Vast) Create(ctx context.Context, cfg *config.Config, offer *Offer, pubKey string) (string, error) {
	body := map[string]any{
		"client_id": "me",
		"image":     cfg.Image,
		"disk":      cfg.DiskGB,
		"label":     Label,
		// Vast's own spelling: direct ssh with proxy fallback.
		"runtype": "ssh_direc ssh_proxy",
		"env":     map[string]string{"AUTHORIZED_KEY": strings.TrimSpace(pubKey)},
	}
	if cfg.Mode == "interruptible" {
		body["price"] = offer.PricePerH
	}
	var resp struct {
		Success     bool   `json:"success"`
		NewContract int64  `json:"new_contract"`
		Msg         string `json:"msg"`
		Error       string `json:"error"`
	}
	if err := v.do(ctx, "PUT", "/asks/"+offer.ID+"/", body, &resp); err != nil {
		return "", fmt.Errorf("vast create: %w", err)
	}
	if !resp.Success || resp.NewContract == 0 {
		return "", fmt.Errorf("vast create failed: %s %s", resp.Error, resp.Msg)
	}
	return strconv.FormatInt(resp.NewContract, 10), nil
}

type vastInstance struct {
	ID             int64  `json:"id"`
	ActualStatus   string `json:"actual_status"`
	CurState       string `json:"cur_state"`
	IntendedStatus string `json:"intended_status"`
	StatusMsg      string `json:"status_msg"`
	PublicIP       string `json:"public_ipaddr"`
	SSHHost        string `json:"ssh_host"`
	SSHPort        int    `json:"ssh_port"`
	Ports          map[string][]struct {
		HostPort string `json:"HostPort"`
	} `json:"ports"`
	GPUName  string  `json:"gpu_name"`
	DPHTotal float64 `json:"dph_total"`
	Label    string  `json:"label"`
}

func (vi *vastInstance) toInstance() *Instance {
	in := &Instance{
		ID: strconv.FormatInt(vi.ID, 10), StatusMsg: strings.TrimSpace(vi.StatusMsg),
		GPU: vi.GPUName, PricePerHr: vi.DPHTotal, Label: vi.Label,
	}
	actual := strings.ToLower(vi.ActualStatus)
	cur := strings.ToLower(vi.CurState)
	switch {
	case actual == "running":
		in.Status = StatusRunning
	case cur == "stopped" || actual == "exited" || actual == "stopped" || actual == "offline":
		in.Status = StatusStopped
	case actual == "error" || strings.Contains(strings.ToLower(vi.StatusMsg), "error"):
		in.Status = StatusError
	default: // created, loading, scheduling, ""
		in.Status = StatusStarting
	}
	if p := vi.Ports["22/tcp"]; len(p) > 0 && p[0].HostPort != "" && vi.PublicIP != "" {
		in.Host = strings.TrimSpace(vi.PublicIP)
		in.Port, _ = strconv.Atoi(p[0].HostPort)
		in.Direct = true
	} else if vi.SSHHost != "" && vi.SSHPort > 0 {
		in.Host, in.Port = vi.SSHHost, vi.SSHPort
	}
	return in
}

func (v *Vast) Get(ctx context.Context, id string) (*Instance, error) {
	var resp struct {
		Instances *vastInstance `json:"instances"`
	}
	err := v.do(ctx, "GET", "/instances/"+id+"/?owner=me", nil, &resp)
	if isNotFound(err) || (err == nil && (resp.Instances == nil || resp.Instances.ID == 0)) {
		return &Instance{ID: id, Status: StatusGone}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vast show %s: %w", id, err)
	}
	return resp.Instances.toInstance(), nil
}

func (v *Vast) ListOwned(ctx context.Context) ([]Instance, error) {
	var resp struct {
		Instances []vastInstance `json:"instances"`
	}
	if err := v.do(ctx, "GET", "/instances/?owner=me", nil, &resp); err != nil {
		return nil, fmt.Errorf("vast list: %w", err)
	}
	var out []Instance
	for i := range resp.Instances {
		if resp.Instances[i].Label == Label {
			out = append(out, *resp.Instances[i].toInstance())
		}
	}
	return out, nil
}

func (v *Vast) Start(ctx context.Context, id string) error {
	return v.do(ctx, "PUT", "/instances/"+id+"/", map[string]string{"state": "running"}, nil)
}

func (v *Vast) Destroy(ctx context.Context, id string) error {
	err := v.do(ctx, "DELETE", "/instances/"+id+"/", nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (v *Vast) Logs(ctx context.Context, id string) string {
	in, err := v.Get(ctx, id)
	if err != nil {
		return err.Error()
	}
	return in.StatusMsg
}
