// Package vscode configures and launches VS Code Remote-SSH.
package vscode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/tailscale/hujson"
)

func settingsPath() (string, error) {
	base, err := os.UserConfigDir() // %APPDATA%
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Code", "User", "settings.json"), nil
}

// EnsureSettings makes VS Code auto-install exts on every SSH host
// (remote.SSH.defaultExtensions) and skip the "select platform" prompt for
// alias. settings.json is JSONC; comments and layout are preserved, and a
// one-time backup is kept next to it. Returns true if the file changed.
func EnsureSettings(alias string, exts []string) (bool, error) {
	p, err := settingsPath()
	if err != nil {
		return false, err
	}
	return ensureSettingsAt(p, alias, exts)
}

func ensureSettingsAt(p, alias string, exts []string) (bool, error) {
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		raw = []byte("{}")
	} else if err != nil {
		return false, err
	}
	v, err := hujson.Parse(raw)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", p, err)
	}
	std := v.Clone()
	std.Standardize()
	var cur map[string]any
	if err := json.Unmarshal(std.Pack(), &cur); err != nil {
		return false, err
	}
	if cur == nil {
		cur = map[string]any{}
	}

	platforms, _ := cur["remote.SSH.remotePlatform"].(map[string]any)
	if platforms == nil {
		platforms = map[string]any{}
	}
	wantExts := make([]any, len(exts))
	for i, e := range exts {
		wantExts[i] = e
	}
	if exts == nil {
		wantExts = []any{}
	}
	if reflect.DeepEqual(cur["remote.SSH.defaultExtensions"], wantExts) && platforms[alias] == "linux" {
		return false, nil
	}
	platforms[alias] = "linux"

	type op struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	patch, _ := json.Marshal([]op{
		{"add", "/remote.SSH.defaultExtensions", wantExts},
		{"add", "/remote.SSH.remotePlatform", platforms},
	})
	if err := v.Patch(patch); err != nil {
		return false, fmt.Errorf("patch settings.json: %w", err)
	}
	// No v.Format(): it would realign the user's whole file. Just put the
	// members we touched on their own line, VS Code style.
	if obj, ok := v.Value.(*hujson.Object); ok {
		for i := range obj.Members {
			m := &obj.Members[i]
			if lit, ok := m.Name.Value.(hujson.Literal); ok {
				switch lit.String() {
				case "remote.SSH.defaultExtensions", "remote.SSH.remotePlatform":
					m.Name.BeforeExtra = hujson.Extra("\n\t")
					m.Value.BeforeExtra = hujson.Extra(" ")
				}
			}
		}
		if !strings.HasPrefix(string(obj.AfterExtra), "\n") {
			obj.AfterExtra = hujson.Extra("\n")
		}
	}

	if _, err := os.Stat(p + ".gpu-backpack.bak"); os.IsNotExist(err) && len(raw) > 2 {
		_ = os.WriteFile(p+".gpu-backpack.bak", raw, 0o600)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, v.Pack(), 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, p)
}

func codeCLI() (string, error) {
	if p, err := exec.LookPath("code"); err == nil {
		return p, nil
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		p := filepath.Join(la, "Programs", "Microsoft VS Code", "bin", "code.cmd")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("VS Code 'code' command not found (VS Code: Ctrl+Shift+P → \"Shell Command: Install 'code' command in PATH\")")
}

// Open launches a new VS Code window on alias:/workspace.
func Open(alias, folder string) error {
	code, err := codeCLI()
	if err != nil {
		return err
	}
	cmd := exec.Command(code, "--new-window", "--remote", "ssh-remote+"+alias, folder)
	return cmd.Start()
}
