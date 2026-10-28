package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
	"github.com/mozi30/Remote-GPU-Setup/internal/provider"
	"github.com/mozi30/Remote-GPU-Setup/internal/r2"
	"github.com/mozi30/Remote-GPU-Setup/internal/ui"
)

func (a *App) settingsMenu(ctx context.Context) {
	for {
		ui.Title("Settings")
		type c int
		ch, err := ui.Select("Choose:", []ui.Option[c]{
			{Label: "Edit settings", Value: 0},
			{Label: "Test connections & count matching GPU offers", Value: 1},
			{Label: "Show file locations", Value: 2},
			{Label: "Back", Value: 3},
		})
		if err != nil || ch == 3 {
			return
		}
		switch ch {
		case 0:
			a.editSettings()
		case 1:
			a.testConnections(ctx)
		case 2:
			ui.Step("settings: %s", filepath.Join(a.dir, "config.toml"))
			ui.Step("session:  %s", filepath.Join(a.dir, "state.json"))
			ui.Step("log:      %s", a.logPath)
			ui.Step("API keys / R2 secret: Windows Credential Manager (service %q)", "gpu-backpack")
		}
	}
}

func secretDesc(name string) string {
	if config.Secret(name) != "" {
		return "currently set - leave empty to keep, enter '-' to clear"
	}
	return "not set"
}

func numValidator(bits int) func(string) error {
	return func(s string) error {
		_, err := strconv.ParseFloat(strings.TrimSpace(s), bits)
		if err != nil {
			return fmt.Errorf("enter a number")
		}
		return nil
	}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func (a *App) editSettings() {
	c := *a.cfg
	var vastKey, rpKey, r2Secret string
	gpus := strings.Join(c.GPUNames, ", ")
	maxPrice, disk := ftoa(c.MaxPrice), strconv.Itoa(c.DiskGB)
	down, up, relia := ftoa(c.MinDownMbps), ftoa(c.MinUpMbps), ftoa(c.MinRelia)
	direct, cuda := strconv.Itoa(c.MinDirect), ftoa(c.MinCUDA)
	autosave, maxDel, trash := strconv.Itoa(c.AutosaveMinutes), strconv.Itoa(c.MaxDelete), strconv.Itoa(c.TrashDays)
	excludes, exts := strings.Join(c.Excludes, "\n"), strings.Join(c.Extensions, "\n")
	num := numValidator(64)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("1/6  GPU provider"),
			huh.NewSelect[string]().Title("Provider").Options(
				huh.NewOption("Vast.ai (cheapest, speed filters)", "vast"),
				huh.NewOption("RunPod (fallback)", "runpod")).Value(&c.Provider),
			huh.NewInput().Title("Vast.ai API key").Description(secretDesc(config.SecretVast)).
				EchoMode(huh.EchoModePassword).Value(&vastKey),
			huh.NewInput().Title("RunPod API key").Description(secretDesc(config.SecretRunPod)).
				EchoMode(huh.EchoModePassword).Value(&rpKey),
			huh.NewSelect[string]().Title("Mode").Options(
				huh.NewOption("On-demand (stable)", "on_demand"),
				huh.NewOption("Interruptible (cheaper, can be preempted)", "interruptible")).Value(&c.Mode),
			huh.NewInput().Title("Container image").Value(&c.Image),
		),
		huh.NewGroup(
			huh.NewNote().Title("2/6  GPU wishlist"),
			huh.NewText().Title("GPU names (comma separated)").Value(&gpus).Lines(3),
			huh.NewInput().Title("Max price $/hour").Value(&maxPrice).Validate(num),
			huh.NewInput().Title("Disk GB").Value(&disk).Validate(num),
			huh.NewConfirm().Title("Auto-pick the cheapest offer (skip the picker)?").Value(&c.AutoPick),
		),
		huh.NewGroup(
			huh.NewNote().Title("3/6  Minimum machine quality (Vast)").
				Description("Fast up/down links keep R2 pulls and the final backup quick."),
			huh.NewInput().Title("Min download Mbps").Value(&down).Validate(num),
			huh.NewInput().Title("Min upload Mbps").Value(&up).Validate(num),
			huh.NewInput().Title("Min reliability (0-1)").Value(&relia).Validate(num),
			huh.NewInput().Title("Min direct ports (>=1 = direct SSH, no proxy)").Value(&direct).Validate(num),
			huh.NewInput().Title("Min CUDA version supported by the driver").Value(&cuda).Validate(num),
		),
		huh.NewGroup(
			huh.NewNote().Title("4/6  Cloudflare R2").
				Description("Create an R2 API token with Object Read & Write limited to this one bucket."),
			huh.NewInput().Title("Account ID").Value(&c.R2AccountID),
			huh.NewInput().Title("Access key ID").Value(&c.R2AccessKey),
			huh.NewInput().Title("Secret access key").Description(secretDesc(config.SecretR2)).
				EchoMode(huh.EchoModePassword).Value(&r2Secret),
			huh.NewInput().Title("Bucket").Value(&c.R2Bucket),
			huh.NewInput().Title("Prefix (folder in the bucket)").Value(&c.R2Prefix),
		),
		huh.NewGroup(
			huh.NewNote().Title("5/6  Sync"),
			huh.NewInput().Title("Autosave every N minutes").Value(&autosave).Validate(num),
			huh.NewInput().Title("Max files one sync may delete (safety cap)").Value(&maxDel).Validate(num),
			huh.NewInput().Title("Keep trash snapshots for N days").Value(&trash).Validate(num),
			huh.NewText().Title("Excludes (one rclone pattern per line)").
				Description("x/** = folder x anywhere, /x/** = top-level only").Value(&excludes).Lines(7),
		),
		huh.NewGroup(
			huh.NewNote().Title("6/6  SSH & VS Code"),
			huh.NewInput().Title("SSH private key path").Value(&c.SSHKey),
			huh.NewInput().Title("SSH host alias").Value(&c.SSHAlias),
			huh.NewConfirm().Title("Open VS Code automatically?").Value(&c.OpenVSCode),
			huh.NewText().Title("VS Code extensions to install on the GPU box (one id per line)").Value(&exts).Lines(5),
		),
	)
	if err := form.Run(); err != nil {
		ui.Warn("settings not changed")
		return
	}

	pf := func(s string) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return v }
	pi := func(s string) int { return int(pf(s)) }
	c.GPUNames = config.SplitList(gpus)
	c.MaxPrice, c.DiskGB = pf(maxPrice), pi(disk)
	c.MinDownMbps, c.MinUpMbps, c.MinRelia = pf(down), pf(up), pf(relia)
	c.MinDirect, c.MinCUDA = pi(direct), pf(cuda)
	c.AutosaveMinutes, c.MaxDelete, c.TrashDays = max(pi(autosave), 1), max(pi(maxDel), 1), pi(trash)
	c.Excludes = config.SplitList(excludes)
	c.Extensions = config.SplitList(exts)
	c.R2Prefix = strings.Trim(c.R2Prefix, "/ ")
	if c.R2Prefix == "" {
		c.R2Prefix = "backpack"
	}

	for name, v := range map[string]string{config.SecretVast: vastKey, config.SecretRunPod: rpKey, config.SecretR2: r2Secret} {
		v = strings.TrimSpace(v)
		switch v {
		case "":
			continue
		case "-":
			v = ""
		}
		if err := config.SetSecret(name, v); err != nil {
			ui.Err("saving secret: %v", err)
		}
	}
	*a.cfg = c
	if err := a.cfg.Save(); err != nil {
		ui.Err("save settings: %v", err)
		return
	}
	ui.OK("settings saved")
	if a.sess != nil {
		ui.Step("sync changes apply to the running GPU on the next backup or Reconnect.")
	}
}

func (a *App) testConnections(ctx context.Context) {
	if p, err := provider.New(a.cfg); err != nil {
		ui.Err("%v", err)
	} else {
		ui.Step("searching %s offers with your filters ...", p.Name())
		offers, err := p.Search(ctx, a.cfg)
		switch {
		case err != nil:
			ui.Err("%s: %v", p.Name(), err)
		case offers == nil:
			ui.OK("%s API reachable (no offer search on this provider)", p.Name())
		default:
			ui.OK("%s: %d matching offers", p.Name(), len(offers))
			if len(offers) > 0 {
				o := offers[0]
				ui.Step("cheapest: %s $%.3f/h  ↓%.0f ↑%.0f Mbps  %s", o.GPU, o.PricePerH, o.DownMbps, o.UpMbps, o.Geo)
			}
		}
	}
	st, err := r2.New(a.cfg)
	if err != nil {
		ui.Err("%v", err)
		return
	}
	if err := st.Check(ctx); err != nil {
		ui.Err("%v", err)
		return
	}
	ui.OK("R2 bucket %q reachable", a.cfg.R2Bucket)
}
