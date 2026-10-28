package remote

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	markBegin = "# >>> %s (managed by gpu-backpack) >>>"
	markEnd   = "# <<< %s <<<"
)

// sshPath renders a Windows path the way both Windows ssh.exe and VS Code
// accept it inside ~/.ssh/config: forward slashes, quoted if it has spaces.
func sshPath(p string) string {
	p = filepath.ToSlash(p)
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

// WriteSSHConfig replaces the managed Host block for alias in ~/.ssh/config.
// knownHosts is a tool-private file (reset per rental) so a new box on a
// reused IP:port never trips a host-key mismatch, which would make OpenSSH
// disable port forwarding and break VS Code.
func WriteSSHConfig(alias, host string, port int, identity, knownHosts string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	conf := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		return "", err
	}
	old, err := os.ReadFile(conf)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	kept := stripBlock(string(old), alias)

	var b strings.Builder
	b.WriteString(strings.TrimRight(kept, "\r\n"))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, markBegin+"\n", alias)
	fmt.Fprintf(&b, "Host %s\n", alias)
	fmt.Fprintf(&b, "    HostName %s\n", host)
	fmt.Fprintf(&b, "    Port %d\n", port)
	b.WriteString("    User root\n")
	fmt.Fprintf(&b, "    IdentityFile %s\n", sshPath(identity))
	b.WriteString("    IdentitiesOnly yes\n")
	b.WriteString("    StrictHostKeyChecking accept-new\n")
	fmt.Fprintf(&b, "    UserKnownHostsFile %s\n", sshPath(knownHosts))
	b.WriteString("    ServerAliveInterval 30\n")
	fmt.Fprintf(&b, markEnd+"\n", alias)

	tmp := conf + ".gpu-backpack.tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return conf, os.Rename(tmp, conf)
}

func stripBlock(s, alias string) string {
	begin, end := fmt.Sprintf(markBegin, alias), fmt.Sprintf(markEnd, alias)
	var out []string
	skip := false
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == begin:
			skip = true
		case t == end:
			skip = false
		case !skip:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// ResetKnownHosts empties the tool-private known_hosts (new rental = new host key).
func ResetKnownHosts(path string) error {
	return os.WriteFile(path, nil, 0o600)
}

// EnsureKey generates an ed25519 key pair if missing and returns the public key.
func EnsureKey(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		out, err := exec.Command(Tool("ssh-keygen"), "-t", "ed25519", "-N", "", "-f", path, "-C", "gpu-backpack").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("ssh-keygen: %v: %s", err, out)
		}
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return "", fmt.Errorf("read public key: %w", err)
	}
	return strings.TrimSpace(string(pub)), nil
}
