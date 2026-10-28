package app

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/mozi30/Remote-GPU-Setup/internal/filter"
	"github.com/mozi30/Remote-GPU-Setup/internal/r2"
	"github.com/mozi30/Remote-GPU-Setup/internal/remote"
	"github.com/mozi30/Remote-GPU-Setup/internal/state"
	"github.com/mozi30/Remote-GPU-Setup/internal/ui"
)

// sessionSyncing: a box is actively mirroring /workspace to R2. While true,
// writes must go to the box, because the next autosave makes R2 match the
// box (anything put only into R2 would be moved to trash).
func (a *App) sessionSyncing() bool {
	return a.sess != nil && a.sess.Phase >= state.PhaseHydrated
}

func (a *App) filesMenu(ctx context.Context) {
	store, err := r2.New(a.cfg)
	if err != nil {
		ui.Err("%v", err)
		return
	}
	for {
		ui.Title("R2 Files")
		type c int
		ch, err := ui.Select("Choose:", []ui.Option[c]{
			{Label: "Upload a PC folder → workspace", Value: 0},
			{Label: "Download from R2 workspace → PC folder", Value: 1},
			{Label: "Restore files from trash (deleted / overwritten versions)", Value: 2},
			{Label: "Show R2 usage", Value: 3},
			{Label: fmt.Sprintf("Prune trash older than %d days", a.cfg.TrashDays), Value: 4},
			{Label: "Back", Value: 5},
		})
		if err != nil || ch == 5 {
			return
		}
		switch ch {
		case 0:
			a.uploadFolder(ctx, store)
		case 1:
			a.downloadFolder(ctx, store)
		case 2:
			a.restoreTrash(ctx, store)
		case 3:
			n, ws, tr, err := store.Usage(ctx)
			if err != nil {
				ui.Err("%v", err)
				break
			}
			ui.Step("workspace: %d files, %s   trash: %s", n, human(ws), human(tr))
			ui.Step("bucket %s, prefix %s/", a.cfg.R2Bucket, a.cfg.R2Prefix)
		case 4:
			n, err := store.PruneTrash(ctx, a.cfg.TrashDays)
			if err != nil {
				ui.Err("%v", err)
			} else {
				ui.OK("removed %d snapshot(s)", n)
			}
		}
	}
}

func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// cleanRel validates a workspace-relative destination path.
func cleanRel(s string) (string, bool) {
	s = strings.Trim(filepath.ToSlash(strings.TrimSpace(s)), "/")
	if s == "" {
		return "", true
	}
	c := path.Clean(s)
	if c == "." || strings.HasPrefix(c, "..") || strings.Contains(c, "/../") {
		return "", false
	}
	return c, true
}

func (a *App) uploadFolder(ctx context.Context, store *r2.Store) {
	src, err := ui.Input("Local folder to upload", "")
	if err != nil || src == "" {
		return
	}
	src = strings.Trim(src, `"`)
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		ui.Err("%s is not a folder", src)
		return
	}
	destIn, err := ui.Input("Destination inside the workspace (folder name, empty = workspace root)", filepath.Base(src))
	if err != nil {
		return
	}
	dest, ok := cleanRel(destIn)
	if !ok {
		ui.Err("invalid destination %q", destIn)
		return
	}
	f := filter.New(a.cfg.Excludes)

	if a.sessionSyncing() {
		ui.Step("a GPU session is running: copying straight into /workspace/%s on the box,", dest)
		ui.Step("then backing up (uploading only to R2 would be undone by the next autosave).")
		if _, ok := a.ensureRunning(ctx, false); !ok {
			return
		}
		n, err := a.host().Upload(ctx, src, path.Join("/workspace", dest), func(rel string, dir bool) bool {
			return f.Excluded(strings.Trim(dest+"/"+rel, "/"), dir)
		})
		if err != nil {
			ui.Err("%v", err)
			return
		}
		ui.OK("copied %d files to the box", n)
		if err := a.runBackup(ctx); err != nil {
			ui.Warn("backup after upload failed: %v (autosave will retry)", err)
		}
		return
	}

	ui.Step("uploading %s → R2 workspace/%s ...", src, dest)
	sent, skipped, err := store.Upload(ctx, src, dest, f, func(done, total int, name string) {
		if done%50 == 0 || done == total {
			fmt.Printf("   %d/%d  %s\n", done, total, trunc(name, 60))
		}
	})
	if err != nil {
		ui.Err("upload failed after %d files: %v", sent, err)
		return
	}
	ui.OK("uploaded %d files (%d unchanged skipped). They will be pulled on the next GPU start.", sent, skipped)
}

func (a *App) downloadFolder(ctx context.Context, store *r2.Store) {
	if a.sessionSyncing() {
		ui.Warn("a GPU session is running; R2 can be up to %d min behind the box.", a.cfg.AutosaveMinutes)
		if ui.Confirm("Run a backup first so R2 is current?", true) {
			if _, ok := a.ensureRunning(ctx, false); ok {
				if err := a.runBackup(ctx); err != nil {
					ui.Warn("backup failed: %v (downloading what R2 has)", err)
				}
			}
		}
	}
	entries, err := store.TopLevel(ctx)
	if err != nil {
		ui.Err("%v", err)
		return
	}
	if len(entries) == 0 {
		ui.Warn("the R2 workspace is empty")
		return
	}
	opts := []ui.Option[string]{{Label: "(entire workspace)", Value: "/"}}
	for _, e := range entries {
		opts = append(opts, ui.Option[string]{Label: e, Value: e})
	}
	pick, err := ui.Select("What to download?", opts)
	if err != nil {
		return
	}
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, "gpu-backpack-workspace")
	src := strings.Trim(pick, "/")
	isDir := pick == "/" || strings.HasSuffix(pick, "/")
	def := base
	if isDir && src != "" {
		def = filepath.Join(base, filepath.FromSlash(src))
	}
	dest, err := ui.Input("Save to local folder", def)
	if err != nil || dest == "" {
		return
	}
	ui.Step("downloading ...")
	got, skipped, err := store.Download(ctx, src, dest, func(done, total int, name string) {
		if done%50 == 0 || done == total {
			fmt.Printf("   %d/%d  %s\n", done, total, trunc(name, 60))
		}
	})
	if err != nil {
		ui.Err("download failed after %d files: %v", got, err)
		return
	}
	ui.OK("downloaded %d files (%d already up to date) to %s", got, skipped, dest)
}

func (a *App) restoreTrash(ctx context.Context, store *r2.Store) {
	snaps, err := store.Snapshots(ctx)
	if err != nil {
		ui.Err("%v", err)
		return
	}
	if len(snaps) == 0 {
		ui.Step("trash is empty - nothing was deleted or overwritten by a sync yet")
		return
	}
	opts := []ui.Option[int]{}
	for i, s := range snaps {
		when := s.Stamp
		if !s.Time.IsZero() {
			when = s.Time.Local().Format("2006-01-02 15:04")
		}
		opts = append(opts, ui.Option[int]{Label: fmt.Sprintf("%s   %d file(s), %s", when, s.Files, human(s.Bytes)), Value: i})
	}
	opts = append(opts, ui.Option[int]{Label: "Back", Value: -1})
	i, err := ui.Select("Snapshots = versions replaced/deleted by the sync at that time:", opts)
	if err != nil || i < 0 {
		return
	}
	snap := snaps[i]
	files, err := store.SnapshotFiles(ctx, snap.Stamp)
	if err != nil {
		ui.Err("%v", err)
		return
	}

	var rels []string
	all := false
	type c int
	mode, err := ui.Select(fmt.Sprintf("%d file(s) in this snapshot:", len(files)), []ui.Option[c]{
		{Label: "Pick files to restore", Value: 0},
		{Label: fmt.Sprintf("Restore all %d files", len(files)), Value: 1},
		{Label: "Back", Value: 2},
	})
	if err != nil || mode == 2 {
		return
	}
	if mode == 1 {
		all = true
		for _, f := range files {
			rels = append(rels, f.Rel)
		}
	} else {
		hopts := make([]huh.Option[string], len(files))
		for i, f := range files {
			hopts[i] = huh.NewOption(fmt.Sprintf("%s  (%s)", f.Rel, human(f.Size)), f.Rel)
		}
		if err := huh.NewMultiSelect[string]().Title("Space = select, / = filter, Enter = done").
			Options(hopts...).Value(&rels).Filterable(true).Height(20).Run(); err != nil || len(rels) == 0 {
			return
		}
	}
	if !ui.Confirm(fmt.Sprintf("Restore %d file(s)? Current versions are kept in trash.", len(rels)), true) {
		return
	}

	if a.sessionSyncing() {
		if _, ok := a.ensureRunning(ctx, false); !ok {
			return
		}
		h := a.host()
		if all {
			if err := h.Stream(ctx, fmt.Sprintf("bash %s/restore.sh %s", remote.Dir, remote.Q(snap.Stamp))); err != nil {
				ui.Err("%v", err)
				return
			}
		} else {
			for _, r := range rels {
				if err := h.Stream(ctx, fmt.Sprintf("bash %s/restore.sh %s %s", remote.Dir, remote.Q(snap.Stamp), remote.Q(r))); err != nil {
					ui.Err("%s: %v", r, err)
					return
				}
			}
		}
		ui.OK("restored into /workspace on the box; they reach R2 with the next backup")
		return
	}
	if err := store.RestoreInR2(ctx, snap.Stamp, rels); err != nil {
		ui.Err("%v", err)
		return
	}
	ui.OK("restored %d file(s) in R2", len(rels))
}
