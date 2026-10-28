package remote

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
)

func TestStripBlockKeepsOtherHosts(t *testing.T) {
	in := "Host other\n    HostName 1.2.3.4\n\n" +
		"# >>> gpu-backpack (managed by gpu-backpack) >>>\nHost gpu-backpack\n    Port 1\n# <<< gpu-backpack <<<\n" +
		"Host after\n    Port 2\n"
	out := stripBlock(in, "gpu-backpack")
	if strings.Contains(out, "Port 1") || !strings.Contains(out, "Host other") || !strings.Contains(out, "Host after") {
		t.Fatalf("unexpected:\n%s", out)
	}
}

func TestWriteSSHConfigReplacesBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	key := filepath.Join(home, "my keys", "id")
	for _, port := range []int{1111, 2222} {
		if _, err := WriteSSHConfig("gpu-backpack", "9.9.9.9", port, key, filepath.Join(home, "kh")); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	s := string(b)
	if strings.Count(s, "Host gpu-backpack") != 1 || !strings.Contains(s, "Port 2222") || strings.Contains(s, "Port 1111") {
		t.Fatalf("bad config:\n%s", s)
	}
	if !strings.Contains(s, `IdentityFile "`+filepath.ToSlash(key)+`"`) {
		t.Fatalf("identity path with space must be quoted:\n%s", s)
	}
}

func TestTarExcludes(t *testing.T) {
	got := TarExcludes([]string{".venv/**", "/data/**", "**/node_modules/**", "*.tmp"})
	want := []string{".venv", "./data", "node_modules", "*.tmp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBundleScriptsUseLF(t *testing.T) {
	cfg := config.Default()
	cfg.R2Bucket, cfg.R2AccountID, cfg.R2AccessKey = "b", "acct", "ak"
	files, err := Bundle(cfg, "sk")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, f := range files {
		names[f.Name] = string(f.Data)
		if strings.HasSuffix(f.Name, ".sh") && strings.Contains(string(f.Data), "\r") {
			t.Errorf("%s contains CR", f.Name)
		}
	}
	for _, n := range []string{"common.sh", "hydrate.sh", "backup.sh", "verify.sh", "autosave.sh", "envsetup.sh", "restore.sh", "rclone.conf", "backpack.env", "excludes.txt"} {
		if _, ok := names[n]; !ok {
			t.Errorf("bundle missing %s", n)
		}
	}
	if !strings.Contains(names["backpack.env"], `R2_DEST="r2:b/backpack/workspace"`) {
		t.Errorf("env: %s", names["backpack.env"])
	}
	if !strings.Contains(names["rclone.conf"], "endpoint = https://acct.r2.cloudflarestorage.com") {
		t.Errorf("rclone.conf: %s", names["rclone.conf"])
	}
}

func TestExtractTarRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if _, err := extractTar(&buf, t.TempDir(), nil); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}
