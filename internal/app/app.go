// Package app is the interactive menu and the session lifecycle:
// start/reconnect, backup, safe teardown, R2 file tools, settings.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
	"github.com/mozi30/Remote-GPU-Setup/internal/provider"
	"github.com/mozi30/Remote-GPU-Setup/internal/r2"
	"github.com/mozi30/Remote-GPU-Setup/internal/remote"
	"github.com/mozi30/Remote-GPU-Setup/internal/state"
	"github.com/mozi30/Remote-GPU-Setup/internal/ui"
)

type App struct {
	cfg     *config.Config
	sess    *state.Session
	dir     string
	log     io.Writer
	logPath string
}

func Run(version string) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	logPath := filepath.Join(dir, "backpack.log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	fmt.Fprintf(lf, "\n===== %s gpu-backpack %s =====\n", time.Now().Format(time.RFC3339), version)

	cfg, exists, err := config.Load()
	if err != nil {
		return err
	}
	a := &App{cfg: cfg, dir: dir, log: lf, logPath: logPath}

	fmt.Println(ui.BoxStyle.Render("GPU Backpack " + version + "\nVast.ai / RunPod GPUs with your workspace kept in Cloudflare R2"))

	if !exists {
		a.firstRun()
	}
	a.recover(context.Background())
	return a.menu()
}

func (a *App) logf(f string, args ...any) {
	fmt.Fprintf(a.log, "%s "+f+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
}

func (a *App) knownHosts() string { return filepath.Join(a.dir, "known_hosts") }

func (a *App) host() *remote.Host { return &remote.Host{Alias: a.cfg.SSHAlias, Log: a.log} }

// prov returns the provider for the active session (which may differ from
// the currently configured one) or the configured provider.
func (a *App) prov() (provider.Provider, error) {
	c := *a.cfg
	if a.sess != nil {
		c.Provider = a.sess.Provider
	}
	return provider.New(&c)
}

func (a *App) saveSess() {
	if a.sess == nil {
		return
	}
	if err := a.sess.Save(); err != nil {
		ui.Err("could not save session state: %v", err)
	}
}

func (a *App) firstRun() {
	ui.Title("First run")
	exe, _ := os.Executable()
	cands := []string{".env", filepath.Join(filepath.Dir(exe), ".env")}
	if p, err := a.cfg.ImportDotEnv(cands...); p != "" {
		if err != nil {
			ui.Warn("imported %s partially: %v", p, err)
		} else {
			ui.OK("imported settings and API keys from %s", p)
		}
	}
	ui.Step("let's fill in your settings (you can change them any time)")
	a.editSettings()
}

func (a *App) menu() error {
	for {
		a.printStatus()
		type act int
		const (
			actStart act = iota
			actBackup
			actDestroy
			actFiles
			actSettings
			actQuit
		)
		startLabel := "Start GPU & Pull Workspace"
		if a.sess != nil {
			startLabel = "Reconnect to GPU (open VS Code)"
		}
		choice, err := ui.Select("What do you want to do?", []ui.Option[act]{
			{Label: "1  " + startLabel, Value: actStart},
			{Label: "2  Force Backup to R2 Now", Value: actBackup},
			{Label: "3  Backup & Destroy GPU", Value: actDestroy},
			{Label: "4  R2 Files (upload / download / restore from trash)", Value: actFiles},
			{Label: "5  Settings (API keys, GPU wishlist, min internet speed)", Value: actSettings},
			{Label: "q  Quit", Value: actQuit},
		})
		if err != nil || choice == actQuit {
			a.sayGoodbye()
			return nil
		}
		ctx := context.Background()
		switch choice {
		case actStart:
			if a.sess != nil {
				a.reconnect(ctx)
			} else {
				a.start(ctx)
			}
		case actBackup:
			a.backupNow(ctx)
		case actDestroy:
			a.teardown(ctx)
		case actFiles:
			a.filesMenu(ctx)
		case actSettings:
			a.settingsMenu(ctx)
		}
	}
}

func (a *App) sayGoodbye() {
	if a.sess != nil {
		ui.Warn("GPU %s (%s) is still RUNNING and billing (~$%.3f/h).", a.sess.InstanceID, a.sess.GPU, a.sess.PricePerHr)
		ui.Step("autosave on the box keeps pushing to R2 every %d min. Reopen this tool to reconnect or destroy it.", a.cfg.AutosaveMinutes)
	}
}

// printStatus shows the session banner, including live autosave info from the box.
func (a *App) printStatus() {
	fmt.Println()
	if a.sess == nil {
		fmt.Println(ui.BoxStyle.Render("No GPU running."))
		return
	}
	s := a.sess
	up := time.Since(s.RentedAt).Round(time.Minute)
	lines := []string{
		fmt.Sprintf("GPU session: %s #%s  %s  $%.3f/h", s.Provider, s.InstanceID, s.GPU, s.PricePerHr),
		fmt.Sprintf("Up %s (~$%.2f so far)   setup: %s", up, up.Hours()*s.PricePerHr, s.Phase),
	}
	if s.Phase >= state.PhaseSSH {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		out, err := a.host().Output(ctx, "bash "+remote.Dir+"/autosave.sh status 2>/dev/null")
		cancel()
		if err != nil {
			lines = append(lines, "Box: not reachable right now")
		} else {
			lines = append(lines, describeBoxStatus(out, a.sess))
		}
	}
	fmt.Println(ui.BoxStyle.Render(strings.Join(lines, "\n")))
}

func describeBoxStatus(out string, s *state.Session) string {
	kv := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			kv[k] = v
		}
	}
	parts := []string{"autosave " + kv["autosave"]}
	if t, err := time.Parse(time.RFC3339, kv["last_ok"]); err == nil {
		parts = append(parts, "last backup "+ago(t))
		if t.After(s.LastBackup) {
			s.LastBackup = t
			_ = s.Save()
		}
	} else {
		parts = append(parts, "no backup yet")
	}
	if f := kv["last_failed"]; f != "" {
		parts = append(parts, "LAST BACKUP FAILED "+f)
	}
	return "Box: " + strings.Join(parts, " · ")
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Minute)
	if d < time.Minute {
		return "just now"
	}
	return d.String() + " ago"
}

// recover runs at launch: re-attach to a tracked session, notice instances
// that vanished, and find labelled instances the tool lost track of.
func (a *App) recover(ctx context.Context) {
	sess, err := state.Load()
	if err != nil {
		ui.Err("state file unreadable: %v", err)
	}
	if sess == nil {
		a.findOrphans(ctx)
		return
	}
	a.sess = sess
	p, err := a.prov()
	if err != nil {
		ui.Err("%v", err)
		return
	}
	inst, err := p.Get(ctx, sess.InstanceID)
	if err != nil {
		ui.Warn("tracked GPU %s: cannot reach %s API (%v). Keeping it tracked.", sess.InstanceID, sess.Provider, err)
		return
	}
	switch inst.Status {
	case provider.StatusGone:
		a.handleGone(ctx)
	case provider.StatusStopped:
		ui.Alert("Found your GPU session from before, but the instance is STOPPED",
			"(interruptible instances stop when outbid). Its disk still exists.",
			"Choose 'Reconnect' to restart it, or 'Backup & Destroy'.")
	default:
		ui.OK("found your running GPU session %s (%s, %s) - choose Reconnect to continue.", sess.InstanceID, inst.GPU, inst.Status)
	}
}

func (a *App) handleGone(ctx context.Context) {
	s := a.sess
	last := "never"
	if !s.LastBackup.IsZero() {
		last = s.LastBackup.Local().Format("2006-01-02 15:04") + " (" + ago(s.LastBackup) + ")"
	}
	ui.Alert(fmt.Sprintf("GPU instance %s no longer exists on %s (destroyed or reclaimed).", s.InstanceID, s.Provider),
		"Your workspace in R2 is as of the last successful backup: "+last)
	if st, err := r2.New(a.cfg); err == nil {
		_ = st.ReleaseLock(ctx, s.InstanceID)
	}
	_ = state.Clear()
	a.sess = nil
}

func (a *App) findOrphans(ctx context.Context) {
	if a.cfg.Validate() != nil {
		return
	}
	p, err := a.prov()
	if err != nil {
		return
	}
	insts, err := p.ListOwned(ctx)
	if err != nil || len(insts) == 0 {
		return
	}
	ui.Alert(fmt.Sprintf("Found %d gpu-backpack instance(s) on your %s account that this PC is not tracking.", len(insts), p.Name()),
		"They are billing. Adopt one to reconnect / back up / destroy it.")
	opts := []ui.Option[int]{{Label: "Ignore", Value: -1}}
	for i, in := range insts {
		opts = append(opts, ui.Option[int]{Label: fmt.Sprintf("Adopt #%s  %s  %s  $%.3f/h", in.ID, in.GPU, in.Status, in.PricePerHr), Value: i})
	}
	i, err := ui.Select("Adopt an instance?", opts)
	if err != nil || i < 0 {
		return
	}
	in := insts[i]
	a.sess = &state.Session{Provider: p.Name(), InstanceID: in.ID, GPU: in.GPU, PricePerHr: in.PricePerHr,
		Phase: state.PhaseRented, RentedAt: time.Now()}
	a.saveSess()
	ui.OK("adopted %s - choose Reconnect", in.ID)
}
