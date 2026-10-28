package vscode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSettingsKeepsCommentsAndIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	orig := "{\n\t// my comment\n\t\"editor.fontSize\": 14,\n}\n"
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := ensureSettingsAt(p, "gpu-backpack", []string{"ms-python.python"})
	if err != nil || !changed {
		t.Fatalf("first run: changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	for _, want := range []string{"// my comment", `"editor.fontSize": 14`, `"ms-python.python"`, `"gpu-backpack":"linux"`,
		"\n\t\"remote.SSH.defaultExtensions\": ", "\n\t\"remote.SSH.remotePlatform\": "} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	t.Logf("result:\n%s", s)
	if _, err := os.Stat(p + ".gpu-backpack.bak"); err != nil {
		t.Errorf("backup not written: %v", err)
	}
	changed, err = ensureSettingsAt(p, "gpu-backpack", []string{"ms-python.python"})
	if err != nil || changed {
		t.Fatalf("second run should be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestEnsureSettingsCreatesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "Code", "User", "settings.json")
	if _, err := ensureSettingsAt(p, "a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}
