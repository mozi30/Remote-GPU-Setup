package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
	"github.com/mozi30/Remote-GPU-Setup/internal/provider"
	"github.com/mozi30/Remote-GPU-Setup/internal/r2"
	"github.com/mozi30/Remote-GPU-Setup/internal/remote"
	"github.com/mozi30/Remote-GPU-Setup/internal/state"
	"github.com/mozi30/Remote-GPU-Setup/internal/ui"
	"github.com/mozi30/Remote-GPU-Setup/internal/vscode"
)

var errHydrate = errors.New("pull from R2 failed")

// ---- start a new session ----

func (a *App) start(ctx context.Context) {
	ui.Title("Start GPU & Pull Workspace")
	if err := a.cfg.Validate(); err != nil {
		ui.Err("%v", err)
		return
	}
	store, err := r2.New(a.cfg)
	if err != nil {
		ui.Err("%v", err)
		return
	}
	if err := store.Check(ctx); err != nil {
		ui.Err("%v", err)
		return
	}
	if !a.checkLock(ctx, store) {
		return
	}
	p, err := provider.New(a.cfg)
	if err != nil {
		ui.Err("%v", err)
		return
	}

	pub, err := remote.EnsureKey(a.cfg.SSHKey)
	if err != nil {
		ui.Err("ssh key: %v", err)
		return
	}
	if err := p.EnsureSSHKey(ctx, pub); err != nil {
		ui.Warn("could not register SSH key on %s account (%v); continuing", p.Name(), err)
	}

	offer, ok := a.pickOffer(ctx, p)
	if !ok {
		return
	}

	ui.Step("renting %s ...", offerName(offer))
	id, err := p.Create(ctx, a.cfg, offer, pub)
	if err != nil {
		ui.Err("%v", err)
		return
	}
	// Persist immediately: from here on the instance bills, and a crash must
	// not lose track of it.
	a.sess = &state.Session{Provider: p.Name(), InstanceID: id, Phase: state.PhaseRented, RentedAt: time.Now()}
	if offer != nil {
		a.sess.GPU, a.sess.PricePerHr = offer.GPU, offer.PricePerH
	}
	a.saveSess()
	a.logf("rented %s %s", p.Name(), id)
	ui.OK("instance %s created (saved locally, so it can be recovered if this window closes)", id)

	inst, err := a.waitRunning(ctx, p, id, 15*time.Minute)
	if err != nil {
		ui.Err("%v", err)
		// Nothing was pulled yet, so there is no user data on it.
		ui.Step("destroying %s to stop billing (no workspace data on it yet)", id)
		if derr := p.Destroy(ctx, id); derr != nil {
			ui.Err("destroy failed: %v - destroy it in the %s dashboard!", derr, p.Name())
			return
		}
		_ = state.Clear()
		a.sess = nil
		return
	}
	if inst.GPU != "" {
		a.sess.GPU = inst.GPU
	}
	if inst.PricePerHr > 0 {
		a.sess.PricePerHr = inst.PricePerHr
	}
	_ = remote.ResetKnownHosts(a.knownHosts())
	a.attachLoop(ctx, store, inst)
}

func offerName(o *provider.Offer) string {
	if o == nil {
		return "a GPU from your wishlist"
	}
	return fmt.Sprintf("%s for $%.3f/h (offer %s)", o.GPU, o.PricePerH, o.ID)
}

func (a *App) pickOffer(ctx context.Context, p provider.Provider) (*provider.Offer, bool) {
	ui.Step("searching %s: %s | <= $%.2f/h | down>=%.0f up>=%.0f Mbps | reliability>=%.2f | direct ports>=%d",
		p.Name(), strings.Join(a.cfg.GPUNames, ", "), a.cfg.MaxPrice, a.cfg.MinDownMbps, a.cfg.MinUpMbps, a.cfg.MinRelia, a.cfg.MinDirect)
	offers, err := p.Search(ctx, a.cfg)
	if err != nil {
		ui.Err("%v", err)
		return nil, false
	}
	if offers == nil { // provider chooses the machine (RunPod)
		ui.Warn("%s has no offer search; it will pick from your GPU wishlist (speed filters not applied).", p.Name())
		return nil, ui.Confirm("Rent now?", true)
	}
	if len(offers) == 0 {
		ui.Err("no offers match. Loosen a filter in Settings: raise max price (now $%.2f), lower min speeds, or add GPUs.", a.cfg.MaxPrice)
		return nil, false
	}
	if a.cfg.AutoPick {
		return &offers[0], true
	}
	n := min(len(offers), 20)
	opts := []ui.Option[int]{}
	for i := 0; i < n; i++ {
		o := offers[i]
		opts = append(opts, ui.Option[int]{Value: i, Label: fmt.Sprintf("%-14s %3.0fG  $%.3f/h  dlperf %5.1f  rel %4.1f%%  ↓%5.0f ↑%5.0f Mbps  %2.0f cpu %4.0fG ram  %s",
			o.GPU, o.VRAMGB, o.PricePerH, o.DLPerf, o.Relia*100, o.DownMbps, o.UpMbps, o.CPUCores, o.RAMGB, trunc(o.Geo, 22))})
	}
	opts = append(opts, ui.Option[int]{Label: "Cancel", Value: -1})
	i, err := ui.Select(fmt.Sprintf("%d offers (cheapest first) - pick one:", len(offers)), opts)
	if err != nil || i < 0 {
		return nil, false
	}
	return &offers[i], true
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// checkLock makes sure no other session is writing the same R2 workspace.
func (a *App) checkLock(ctx context.Context, store *r2.Store) bool {
	l, err := store.ReadLock(ctx)
	if err != nil {
		ui.Warn("could not read R2 lock: %v", err)
		return true
	}
	if l == nil || (a.sess != nil && l.InstanceID == a.sess.InstanceID) {
		return true
	}
	// Stale if that instance no longer exists.
	c := *a.cfg
	c.Provider = l.Provider
	if p, err := provider.New(&c); err == nil {
		if in, err := p.Get(ctx, l.InstanceID); err == nil && in.Status == provider.StatusGone {
			ui.Step("clearing stale R2 lock of destroyed instance %s", l.InstanceID)
			return true
		}
	}
	ui.Alert(fmt.Sprintf("Another session holds the R2 workspace: %s #%s from PC %q since %s.",
		l.Provider, l.InstanceID, l.PC, l.Since.Local().Format("2006-01-02 15:04")),
		"Two GPUs syncing the same workspace would overwrite each other's changes.")
	return ui.Confirm("Start anyway (take over the lock)?", false)
}

func (a *App) waitRunning(ctx context.Context, p provider.Provider, id string, timeout time.Duration) (*provider.Instance, error) {
	ui.Step("waiting for the instance to boot (image pull can take a few minutes)...")
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		in, err := p.Get(ctx, id)
		if err == nil {
			msg := in.Status.String()
			if in.StatusMsg != "" {
				msg += ": " + trunc(in.StatusMsg, 90)
			}
			if msg != last {
				fmt.Println("   " + ui.Dim("%s  %s", time.Now().Format("15:04:05"), msg))
				last = msg
			}
			switch in.Status {
			case provider.StatusRunning:
				if in.Host != "" && in.Port > 0 {
					return in, nil
				}
			case provider.StatusError:
				return nil, fmt.Errorf("instance failed: %s", in.StatusMsg)
			case provider.StatusGone:
				return nil, fmt.Errorf("instance disappeared")
			}
		}
		time.Sleep(10 * time.Second)
	}
	return nil, fmt.Errorf("instance did not start within %s (%s)", timeout, p.Logs(ctx, id))
}

// ---- reconnect ----

func (a *App) reconnect(ctx context.Context) {
	ui.Title("Reconnect")
	inst, ok := a.ensureRunning(ctx, true)
	if !ok {
		return
	}
	store, err := r2.New(a.cfg)
	if err != nil {
		ui.Err("%v", err)
		return
	}
	a.attachLoop(ctx, store, inst)
}

// ensureRunning brings the tracked instance to "running" (starting a
// stopped one if allowed) and refreshes host/port + ssh config.
func (a *App) ensureRunning(ctx context.Context, allowStart bool) (*provider.Instance, bool) {
	p, err := a.prov()
	if err != nil {
		ui.Err("%v", err)
		return nil, false
	}
	in, err := p.Get(ctx, a.sess.InstanceID)
	if err != nil {
		ui.Err("%v", err)
		return nil, false
	}
	switch in.Status {
	case provider.StatusGone:
		a.handleGone(ctx)
		return nil, false
	case provider.StatusStopped:
		if !allowStart || !ui.Confirm("The instance is stopped (outbid / preempted?). Start it again?", true) {
			return nil, false
		}
		if err := p.Start(ctx, in.ID); err != nil {
			ui.Err("start failed: %v", err)
			return nil, false
		}
		fallthrough
	case provider.StatusStarting, provider.StatusUnknown:
		if in, err = a.waitRunning(ctx, p, in.ID, 15*time.Minute); err != nil {
			ui.Err("%v", err)
			return nil, false
		}
	case provider.StatusError:
		ui.Err("instance reports an error: %s", in.StatusMsg)
		return nil, false
	}
	if err := a.writeSSH(in); err != nil {
		ui.Err("%v", err)
		return nil, false
	}
	return in, true
}

func (a *App) writeSSH(in *provider.Instance) error {
	if !in.Direct {
		ui.Warn("no direct SSH port; using the provider's SSH proxy (slower)")
	}
	a.sess.Host, a.sess.Port = in.Host, in.Port
	a.saveSess()
	conf, err := remote.WriteSSHConfig(a.cfg.SSHAlias, in.Host, in.Port, a.cfg.SSHKey, a.knownHosts())
	if err != nil {
		return fmt.Errorf("write ssh config: %w", err)
	}
	a.logf("ssh config %s -> %s:%d", conf, in.Host, in.Port)
	return nil
}

// attachLoop runs attach and, if the R2 pull fails, keeps the instance and
// lets the user retry or destroy it (nothing has been pushed at that point).
func (a *App) attachLoop(ctx context.Context, store *r2.Store, inst *provider.Instance) {
	for {
		err := a.attach(ctx, store, inst)
		if err == nil {
			return
		}
		ui.Err("%v", err)
		if !errors.Is(err, errHydrate) {
			ui.Step("instance %s kept running; use Reconnect to retry or Backup & Destroy to stop it.", a.sess.InstanceID)
			return
		}
		ui.Alert("Pulling your workspace from R2 failed.",
			"Nothing was pushed - your R2 copy is untouched. Autosave is OFF on this box.")
		type c int
		choice, _ := ui.Select("What now?", []ui.Option[c]{
			{Label: "Retry the pull", Value: 0},
			{Label: "Destroy this instance (safe: it holds no work of yours)", Value: 1},
			{Label: "Leave it running and go back to the menu", Value: 2},
		})
		switch choice {
		case 0:
			continue
		case 1:
			a.destroyAndForget(ctx)
			return
		default:
			return
		}
	}
}

// attach: ssh up → install scripts → pull from R2 → python envs → autosave →
// R2 lock → VS Code. Safe to run repeatedly (reconnect).
func (a *App) attach(ctx context.Context, store *r2.Store, inst *provider.Instance) error {
	if a.sess.Host != inst.Host || a.sess.Port != inst.Port || a.sess.Phase < state.PhaseSSH {
		if err := a.writeSSH(inst); err != nil {
			return err
		}
	}
	h := a.host()
	ui.Step("waiting for SSH on %s:%d ...", inst.Host, inst.Port)
	if !h.WaitReady(ctx, 6*time.Minute, func() { fmt.Print(".") }) {
		fmt.Println()
		return fmt.Errorf("SSH did not come up. Debug: ssh -v %s", a.cfg.SSHAlias)
	}
	fmt.Println()
	ui.OK("SSH is up")

	if err := a.installBundle(ctx); err != nil {
		return err
	}
	if a.sess.Phase < state.PhaseSSH {
		a.sess.Phase = state.PhaseSSH
		a.saveSess()
	}

	ui.Step("pulling workspace from R2 ...")
	if err := h.Stream(ctx, "bash "+remote.Dir+"/hydrate.sh"); err != nil {
		return fmt.Errorf("%w: %v", errHydrate, err)
	}
	firstAttach := a.sess.Phase < state.PhaseHydrated
	if firstAttach {
		a.sess.Phase = state.PhaseHydrated
		a.saveSess()
	}
	ui.OK("workspace pulled")

	if firstAttach || a.sess.Phase < state.PhaseReady {
		ui.Step("building Python environments with uv ...")
		if err := h.Stream(ctx, "bash "+remote.Dir+"/envsetup.sh"); err != nil {
			ui.Warn("env setup reported an error (continuing): %v", err)
		}
	}

	if err := h.Stream(ctx, "bash "+remote.Dir+"/autosave.sh start"); err != nil {
		ui.Warn("autosave did not start: %v - use 'Force Backup' manually!", err)
	}

	host, _ := os.Hostname()
	if err := store.WriteLock(ctx, r2.Lock{Provider: a.sess.Provider, InstanceID: a.sess.InstanceID, PC: host, Since: time.Now().UTC()}); err != nil {
		ui.Warn("could not write R2 lock: %v", err)
	}

	if changed, err := vscode.EnsureSettings(a.cfg.SSHAlias, a.cfg.Extensions); err != nil {
		ui.Warn("VS Code settings: %v", err)
	} else if changed {
		ui.OK("VS Code will auto-install %d extension(s) on the GPU box", len(a.cfg.Extensions))
	}
	if a.cfg.OpenVSCode {
		if err := vscode.Open(a.cfg.SSHAlias, "/workspace"); err != nil {
			ui.Warn("%v", err)
		} else {
			ui.OK("opening VS Code on %s:/workspace", a.cfg.SSHAlias)
		}
	}
	ui.Step("terminal: ssh %s", a.cfg.SSHAlias)

	a.sess.Phase = state.PhaseReady
	a.saveSess()
	ui.OK("ready - autosave pushes to R2 every %d min", a.cfg.AutosaveMinutes)
	return nil
}

func (a *App) installBundle(ctx context.Context) error {
	files, err := remote.Bundle(a.cfg, config.Secret(config.SecretR2))
	if err != nil {
		return err
	}
	if err := a.host().Install(ctx, remote.Dir, files); err != nil {
		return err
	}
	return nil
}

// boxHydrated asks the box whether a pull from R2 completed there.
// ok=false means the box could not be asked.
func (a *App) boxHydrated(ctx context.Context) (hydrated, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := a.host().Output(ctx, "test -f "+remote.Dir+"/hydrated && echo yes || echo no")
	if err != nil {
		return false, false
	}
	return strings.HasSuffix(out, "yes"), true
}
