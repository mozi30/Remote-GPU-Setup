// Package config holds user settings (config.toml) and secrets (Windows
// Credential Manager via keyring). Everything lives under %APPDATA%\gpu-backpack.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/zalando/go-keyring"
)

const (
	AppName      = "gpu-backpack"
	keyringSvc   = "gpu-backpack"
	SecretVast   = "vast_api_key"
	SecretRunPod = "runpod_api_key"
	SecretR2     = "r2_secret_access_key"
)

type Config struct {
	Provider string `toml:"provider"` // "vast" | "runpod"
	Image    string `toml:"image"`
	Mode     string `toml:"mode"` // "on_demand" | "interruptible"

	GPUNames    []string `toml:"gpu_names"`
	MaxPrice    float64  `toml:"max_price"`
	DiskGB      int      `toml:"disk_gb"`
	AutoPick    bool     `toml:"auto_pick_cheapest"`
	MinDownMbps float64  `toml:"min_inet_down_mbps"`
	MinUpMbps   float64  `toml:"min_inet_up_mbps"`
	MinRelia    float64  `toml:"min_reliability"`
	MinDirect   int      `toml:"min_direct_ports"`
	MinCUDA     float64  `toml:"min_cuda"`

	R2AccountID string `toml:"r2_account_id"`
	R2AccessKey string `toml:"r2_access_key_id"`
	R2Bucket    string `toml:"r2_bucket"`
	R2Prefix    string `toml:"r2_prefix"`

	AutosaveMinutes int      `toml:"autosave_minutes"`
	MaxDelete       int      `toml:"max_delete_per_sync"`
	TrashDays       int      `toml:"trash_retention_days"`
	Excludes        []string `toml:"excludes"`

	SSHKey     string   `toml:"ssh_key"`
	SSHAlias   string   `toml:"ssh_alias"`
	OpenVSCode bool     `toml:"open_vscode"`
	Extensions []string `toml:"vscode_extensions"`
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Provider: "vast",
		Image:    "ghcr.io/mozi30/fast-remote-gpu:latest",
		Mode:     "on_demand",
		GPUNames: []string{"RTX 5060 Ti", "RTX 5070", "RTX 5070 Ti", "RTX 5080", "RTX 5090",
			"RTX 4090", "RTX 4080 SUPER", "RTX 4080", "RTX 3090"},
		MaxPrice:    0.60,
		DiskGB:      100,
		MinDownMbps: 500,
		MinUpMbps:   500,
		MinRelia:    0.98,
		MinDirect:   1,
		MinCUDA:     12.8, // image is cu12.8; older drivers can't run it

		R2Prefix: "backpack",

		AutosaveMinutes: 12,
		MaxDelete:       500,
		TrashDays:       14,
		// rclone filter syntax: "x/**" = directory x at any depth,
		// "/x/**" = only the top-level /workspace/x.
		Excludes: []string{
			".venv/**", ".cache/**", "__pycache__/**", "node_modules/**",
			".ipynb_checkpoints/**",
			"/data/**", // datasets: re-download instead of syncing
		},

		SSHKey:     filepath.Join(home, ".ssh", "fast_remote_gpu"),
		SSHAlias:   "gpu-backpack",
		OpenVSCode: true,
		Extensions: []string{"ms-python.python", "ms-python.vscode-pylance", "ms-toolsai.jupyter"},
	}
}

// Dir is %APPDATA%\gpu-backpack (created on demand).
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, AppName)
	return d, os.MkdirAll(d, 0o700)
}

func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// Load returns the saved config, or defaults (exists=false) on first run.
func Load() (cfg *Config, exists bool, err error) {
	cfg = Default()
	p, err := path()
	if err != nil {
		return nil, false, err
	}
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return cfg, false, nil
	}
	if _, err := toml.DecodeFile(p, cfg); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", p, err)
	}
	return cfg, true, nil
}

func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// R2Endpoint is the S3-compatible endpoint host for the account.
func (c *Config) R2Endpoint() string {
	return c.R2AccountID + ".r2.cloudflarestorage.com"
}

// Validate reports what is missing before a GPU session can start.
func (c *Config) Validate() error {
	var miss []string
	switch c.Provider {
	case "vast":
		if Secret(SecretVast) == "" {
			miss = append(miss, "Vast API key")
		}
	case "runpod":
		if Secret(SecretRunPod) == "" {
			miss = append(miss, "RunPod API key")
		}
	default:
		miss = append(miss, "provider (vast|runpod)")
	}
	if c.R2AccountID == "" || c.R2AccessKey == "" || c.R2Bucket == "" || Secret(SecretR2) == "" {
		miss = append(miss, "R2 account ID / access key / secret / bucket")
	}
	if c.Image == "" {
		miss = append(miss, "image")
	}
	if len(c.GPUNames) == 0 {
		miss = append(miss, "GPU wishlist")
	}
	if len(miss) > 0 {
		return fmt.Errorf("missing settings: %s (open Settings)", strings.Join(miss, ", "))
	}
	return nil
}

// Secret reads a secret from the OS keyring; "" when unset.
func Secret(name string) string {
	v, err := keyring.Get(keyringSvc, name)
	if err != nil {
		return ""
	}
	return v
}

func SetSecret(name, value string) error {
	if value == "" {
		err := keyring.Delete(keyringSvc, name)
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return keyring.Set(keyringSvc, name, value)
}

// ImportDotEnv seeds settings from the old bash-era .env file, if one is found.
// Returns the path it imported from ("" if none).
func (c *Config) ImportDotEnv(candidates ...string) (string, error) {
	for _, p := range candidates {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()
		env := map[string]string{}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
		if v := env["PROVIDER"]; v != "" {
			c.Provider = v
		}
		if v := env["IMAGE"]; v != "" && !strings.Contains(v, "your-github-username") {
			c.Image = v
		}
		if v := env["MODE"]; v != "" {
			c.Mode = v
		}
		if v := env["GPU_NAMES"]; v != "" {
			c.GPUNames = SplitList(v)
		}
		if v, err := strconv.ParseFloat(env["MAX_PRICE"], 64); err == nil {
			c.MaxPrice = v
		}
		if v, err := strconv.Atoi(env["DISK_GB"]); err == nil {
			c.DiskGB = v
		}
		if v := env["SSH_KEY"]; v != "" {
			if strings.HasPrefix(v, "~") {
				home, _ := os.UserHomeDir()
				v = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(v, "~"), "/"))
			}
			c.SSHKey = filepath.Clean(v)
		}
		if v := env["VAST_API_KEY"]; v != "" {
			if err := SetSecret(SecretVast, v); err != nil {
				return p, err
			}
		}
		if v := env["RUNPOD_API_KEY"]; v != "" {
			if err := SetSecret(SecretRunPod, v); err != nil {
				return p, err
			}
		}
		return p, sc.Err()
	}
	return "", nil
}

// SplitList splits "a, b ,c" into trimmed non-empty items.
func SplitList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
