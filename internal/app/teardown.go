package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mozi30/Remote-GPU-Setup/internal/provider"
	"github.com/mozi30/Remote-GPU-Setup/internal/r2"
	"github.com/mozi30/Remote-GPU-Setup/internal/remote"
	"github.com/mozi30/Remote-GPU-Setup/internal/state"
	"github.com/mozi30/Remote-GPU-Setup/internal/ui"
)

// ---- force backup ----

func (a *App) backupNow(ctx context.Context) {
	ui.Title("Force Backup to R2")
	if a.sess == nil {
		ui.Warn("no GPU session - nothing to back up")
		return
	}
	if _, ok := a.ensureRunning(ctx, false); !ok {
		return
	}
	if hyd, ok := a.boxHydrated(ctx); !ok {
		ui.Err("cannot reach the box over SSH")
		return
	} else if !hyd {
		ui.Warn("the workspace was never pulled from R2 on this box; pushing is blocked to protect R2. Use Reconnect first.")
		return
	}
	if err := a.runBackup(ctx); err != nil {
		ui.Err("backup failed: %v", err)
		return
	}
	ui.OK("backup complete")
}

// runBackup pushes /workspace to R2. If rclone stops with a fatal error -
// typically the per-sync delete cap - the user may allow one uncapped run
// (deleted files still land in trash/).
func (a *App) runBackup(ctx context.Context) error {
	// refresh scripts/config so settings changes (excludes etc.) apply
	if err := a.installBundle(ctx); err != nil {
		return err
	}
	h := a.host()
	err := h.Stream(ctx, "bash "+remote.Dir+"/backup.sh")
	if err != nil && strings.Contains(err.Error(), "exit status 7") {
		ui.Warn("rclone stopped with a fatal error. Usually this is the safety cap: this sync would delete more than %d files.", a.cfg.MaxDelete)
		ui.Step("deleted files are not lost - they are moved to the R2 trash and can be restored.")
		if ui.Confirm("Allow this one backup to delete more files than the cap?", false) {
			err = h.Stream(ctx, "BP_MAX_DELETE=-1 bash "+remote.Dir+"/backup.sh")
		}
	}
	if err != nil {
		return err
	}
	a.sess.LastBackup = time.Now()
	a.saveSess()
	return nil
}

// ---- backup & destroy ----

func (a *App) teardown(ctx context.Context) {
	ui.Title("Backup & Destroy GPU")
	if a.sess == nil {
		ui.Warn("no GPU session is tracked")
		a.findOrphans(ctx)
		return
	}
	p, err := a.prov()
	if err != nil {
		ui.Err("%v", err)
		return
	}
	in, err := p.Get(ctx, a.sess.InstanceID)
	if err != nil {
		ui.Err("%v", err)
		return
	}
	switch in.Status {
	case provider.StatusGone:
		a.handleGone(ctx)
		return
	case provider.StatusStopped, provider.StatusError:
		ui.Alert("The instance is "+in.Status.String()+", so its files cannot be backed up right now.",
			"Starting it again lets the backup run first.")
		type c int
		ch, _ := ui.Select("What now?", []ui.Option[c]{
			{Label: "Start it, then back up & destroy (recommended)", Value: 0},
			{Label: "Destroy WITHOUT a final backup", Value: 1},
			{Label: "Cancel", Value: 2},
		})
		switch ch {
		case 0:
			if _, ok := a.ensureRunning(ctx, true); !ok {
				return
			}
		case 1:
			a.destroyUnsafe(ctx)
			return
		default:
			return
		}
	default:
		if _, ok := a.ensureRunning(ctx, false); !ok {
			return
		}
	}

	hyd, reachable := a.boxHydrated(ctx)
	if !reachable {
		ui.Alert("Cannot reach the box over SSH, so the final backup cannot run.",
			"Your R2 copy is from the last autosave: "+a.lastBackupText())
		a.failureMenu(ctx, false)
		return
	}
	if !hyd {
		ui.Warn("the workspace was never pulled onto this box, so it holds none of your R2 files.")
		if ui.Confirm("Destroy it?", true) {
			a.destroyAndForget(ctx)
		}
		return
	}

	h := a.host()
	_ = h.Stream(ctx, "bash "+remote.Dir+"/autosave.sh stop")
	ok := false
	for attempt := 1; attempt <= 2 && !ok; attempt++ {
		ui.Step("final backup (attempt %d/2) ...", attempt)
		if err := a.runBackup(ctx); err != nil {
			ui.Err("backup failed: %v", err)
			continue
		}
		ui.Step("verifying R2 has everything ...")
		if err := h.Stream(ctx, "bash "+remote.Dir+"/verify.sh"); err != nil {
			ui.Err("verification failed: %v", err)
			continue
		}
		ok = true
	}
	if !ok {
		ui.Alert("FINAL BACKUP FAILED - the GPU was NOT destroyed.",
			"Your latest changes exist only on the box right now.")
		a.failureMenu(ctx, true)
		return
	}
	ui.OK("everything is safely in R2")
	_, _ = h.Output(ctx, "rm -f "+remote.Dir+"/rclone.conf") // don't leave R2 creds on a host we hand back
	a.destroyAndForget(ctx)
	if st, err := r2.New(a.cfg); err == nil {
		if n, err := st.PruneTrash(ctx, a.cfg.TrashDays); err == nil && n > 0 {
			ui.Step("pruned %d trash snapshot(s) older than %d days", n, a.cfg.TrashDays)
		}
	}
}

func (a *App) lastBackupText() string {
	if a.sess.LastBackup.IsZero() {
		return "unknown"
	}
	return a.sess.LastBackup.Local().Format("2006-01-02 15:04") + " (" + ago(a.sess.LastBackup) + ")"
}

// failureMenu is the safety gate: the instance stays alive until the user
// explicitly decides.
func (a *App) failureMenu(ctx context.Context, reachable bool) {
	for {
		type c int
		opts := []ui.Option[c]{{Label: "Retry backup & destroy", Value: 0}}
		if reachable {
			opts = append(opts, ui.Option[c]{Label: "Download /workspace to this PC over SSH (rescue copy)", Value: 1})
		}
		opts = append(opts,
			ui.Option[c]{Label: "Keep the GPU running (autosave back on) and return to menu", Value: 2},
			ui.Option[c]{Label: "Destroy anyway - may LOSE unsaved work", Value: 3})
		ch, err := ui.Select("What do you want to do?", opts)
		if err != nil {
			ch = 2
		}
		switch ch {
		case 0:
			a.teardown(ctx)
			return
		case 1:
			a.rescueDownload(ctx)
		case 2:
			if reachable {
				_ = a.host().Stream(ctx, "bash "+remote.Dir+"/autosave.sh start")
			}
			ui.Warn("GPU %s is still running and billing.", a.sess.InstanceID)
			return
		case 3:
			if a.destroyUnsafe(ctx) {
				return
			}
		}
	}
}

func (a *App) rescueDownload(ctx context.Context) {
	home, _ := os.UserHomeDir()
	def := filepath.Join(home, "gpu-backpack-rescue", time.Now().Format("20060102-150405"))
	dir, err := ui.Input("Save /workspace to which folder?", def)
	if err != nil || dir == "" {
		return
	}
	ui.Step("downloading /workspace -> %s (excluding envs/caches/data) ...", dir)
	n, err := a.host().Download(ctx, "/workspace", dir, remote.TarExcludes(a.cfg.Excludes), func(n int, name string) {
		if n%200 == 0 {
			fmt.Printf("   %d files ... %s\n", n, trunc(name, 60))
		}
	})
	if err != nil {
		ui.Err("download failed after %d files: %v", n, err)
		return
	}
	ui.OK("saved %d files to %s", n, dir)
}

// destroyUnsafe destroys without a verified backup, after a typed confirmation.
func (a *App) destroyUnsafe(ctx context.Context) bool {
	ui.Alert("Destroying now deletes everything on the box that is not in R2.",
		"Last successful backup: "+a.lastBackupText())
	if !ui.TypeToConfirm("Destroy the GPU without a verified backup?", "DESTROY") {
		ui.Step("cancelled - nothing destroyed")
		return false
	}
	a.destroyAndForget(ctx)
	return true
}

// destroyAndForget destroys the instance, waits until the provider confirms,
// releases the R2 lock and clears local state.
func (a *App) destroyAndForget(ctx context.Context) {
	p, err := a.prov()
	if err != nil {
		ui.Err("%v", err)
		return
	}
	id := a.sess.InstanceID
	ui.Step("destroying %s ...", id)
	if err := p.Destroy(ctx, id); err != nil {
		ui.Err("destroy failed: %v - instance still tracked; retry or use the %s dashboard", err, p.Name())
		return
	}
	gone := false
	for i := 0; i < 12; i++ {
		if in, err := p.Get(ctx, id); err == nil && in.Status == provider.StatusGone {
			gone = true
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !gone {
		ui.Warn("destroy was accepted but %s still lists %s; check the dashboard in a minute.", p.Name(), id)
	}
	if st, err := r2.New(a.cfg); err == nil {
		_ = st.ReleaseLock(ctx, id)
	}
	up, price := time.Since(a.sess.RentedAt), a.sess.PricePerHr
	_ = state.Clear()
	a.sess = nil
	ui.OK("GPU %s destroyed - billing stopped (session %s, ~$%.2f)", id, up.Round(time.Minute), up.Hours()*price)
}
