// Package remote runs commands on the GPU box through the Windows OpenSSH
// client (the same ssh.exe VS Code uses, so both share ~/.ssh/config).
package remote

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Tool returns the path of an OpenSSH binary ("ssh", "ssh-keygen"),
// preferring the Windows built-in one over e.g. Git-Bash's copy.
func Tool(name string) string {
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("SystemRoot"), "System32", "OpenSSH", name+".exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// Host is a reachable box addressed through its ssh config alias.
type Host struct {
	Alias string
	Log   io.Writer // full transcript of remote output (log file); may be nil
}

func (h *Host) cmd(ctx context.Context, timeout time.Duration, remoteCmd string) *exec.Cmd {
	args := []string{"-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", int(timeout.Seconds())),
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4", h.Alias, remoteCmd}
	return exec.CommandContext(ctx, Tool("ssh"), args...)
}

func (h *Host) logw(w io.Writer) io.Writer {
	if h.Log == nil {
		return w
	}
	return io.MultiWriter(w, h.Log)
}

// Output runs a command and returns combined output (not streamed).
func (h *Host) Output(ctx context.Context, remoteCmd string) (string, error) {
	var buf bytes.Buffer
	c := h.cmd(ctx, 10*time.Second, remoteCmd)
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	if h.Log != nil {
		fmt.Fprintf(h.Log, "$ %s\n%s\n", remoteCmd, buf.String())
	}
	return strings.TrimSpace(buf.String()), err
}

// Stream runs a command with output streamed to the terminal (and the log).
func (h *Host) Stream(ctx context.Context, remoteCmd string) error {
	c := h.cmd(ctx, 15*time.Second, remoteCmd)
	c.Stdout, c.Stderr = h.logw(os.Stdout), h.logw(os.Stderr)
	return c.Run()
}

// Ready reports whether ssh works end to end (auth included).
func (h *Host) Ready(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c := h.cmd(ctx, 6*time.Second, "echo READY")
	out, _ := c.Output()
	return strings.TrimSpace(string(out)) == "READY"
}

// WaitReady polls until ssh works or the timeout passes.
func (h *Host) WaitReady(ctx context.Context, timeout time.Duration, tick func()) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.Ready(ctx) {
			return true
		}
		if tick != nil {
			tick()
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Second):
		}
	}
	return false
}

// File is an in-memory file to install on the box.
type File struct {
	Name string
	Mode int64
	Data []byte
}

// Install writes files into dir on the box in one round trip (tar over ssh).
func (h *Host) Install(ctx context.Context, dir string, files []File) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.Name, Mode: f.Mode, Size: int64(len(f.Data)),
			ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	var out bytes.Buffer
	c := h.cmd(ctx, 15*time.Second,
		fmt.Sprintf("mkdir -p %s && umask 077 && tar -xf - -C %s --no-same-owner", dir, dir))
	c.Stdin, c.Stdout, c.Stderr = &buf, &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("install files: %v: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// Upload streams a local directory into remoteDir (created if missing),
// skipping paths matched by excluded(rel). Returns files sent.
func (h *Host) Upload(ctx context.Context, localDir, remoteDir string, excluded func(rel string, dir bool) bool) (int, error) {
	pr, pw := io.Pipe()
	count := 0
	go func() {
		tw := tar.NewWriter(pw)
		err := filepath.Walk(localDir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(localDir, p)
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			if excluded(rel, info.IsDir()) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return nil // skip symlinks/devices
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = rel
			if info.IsDir() {
				hdr.Name += "/"
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			count++
			return err
		})
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	var out bytes.Buffer
	c := h.cmd(ctx, 15*time.Second, fmt.Sprintf("mkdir -p %s && tar -xf - -C %s --no-same-owner", shq(remoteDir), shq(remoteDir)))
	c.Stdin, c.Stdout, c.Stderr = pr, &out, &out
	err := c.Run()
	pr.Close()
	if err != nil {
		return count, fmt.Errorf("upload: %v: %s", err, strings.TrimSpace(out.String()))
	}
	return count, nil
}

// Download pulls remoteDir from the box into localDir as a tar stream.
// tarExcludes are GNU tar --exclude patterns. Returns files written.
func (h *Host) Download(ctx context.Context, remoteDir, localDir string, tarExcludes []string, progress func(n int, name string)) (int, error) {
	var ex strings.Builder
	for _, e := range tarExcludes {
		ex.WriteString(" --exclude=" + shq(e))
	}
	c := h.cmd(ctx, 15*time.Second, fmt.Sprintf("cd %s && tar -cf - %s .", shq(remoteDir), ex.String()))
	var stderr bytes.Buffer
	c.Stderr = &stderr
	rd, err := c.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := c.Start(); err != nil {
		return 0, err
	}
	n, xerr := extractTar(rd, localDir, progress)
	werr := c.Wait()
	if xerr != nil {
		return n, xerr
	}
	if werr != nil {
		return n, fmt.Errorf("remote tar: %v: %s", werr, strings.TrimSpace(stderr.String()))
	}
	return n, nil
}

func extractTar(r io.Reader, dest string, progress func(int, string)) (int, error) {
	tr := tar.NewReader(r)
	n := 0
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return 0, err
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		name := filepath.FromSlash(strings.TrimPrefix(hdr.Name, "./"))
		if name == "" || name == "." {
			continue
		}
		target := filepath.Join(destAbs, name)
		if !strings.HasPrefix(target, destAbs+string(os.PathSeparator)) {
			return n, fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return n, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return n, err
			}
			f, err := os.Create(target)
			if err != nil {
				return n, err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return n, err
			}
			_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
			n++
			if progress != nil {
				progress(n, hdr.Name)
			}
		default:
			// symlinks etc. are skipped on Windows
		}
	}
}

// shq single-quotes a string for the remote POSIX shell.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Q is the exported shell-quote helper.
func Q(s string) string { return shq(s) }
