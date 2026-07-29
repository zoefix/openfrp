package deploy

import (
	"context"
	"fmt"
	"strings"
)

type Existing struct {
	Found bool

	BinaryPath  string
	Version     string
	ServiceName string
	Running     bool
	ConfigPath  string

	Init string
}

func (e Existing) Describe() string {
	if !e.Found {
		return "nothing installed"
	}

	parts := make([]string, 0, 4)
	if e.Version != "" {
		parts = append(parts, "version "+e.Version)
	}
	if e.ServiceName != "" {
		state := "stopped"
		if e.Running {
			state = "running"
		}
		parts = append(parts, fmt.Sprintf("%s service %s", e.Init, state))
	}
	if len(parts) == 0 {
		parts = append(parts, "files but no service")
	}
	return strings.Join(parts, ", ")
}

func (d *Deployer) findExisting(ctx context.Context) Existing {
	found := Existing{
		BinaryPath: d.opts.BinaryPath,
		ConfigPath: d.opts.ConfigPath,
	}

	if out := d.session.Output(ctx,
		"command -v openfrps 2>/dev/null || ls "+ShellQuote(d.opts.BinaryPath)+
			" 2>/dev/null"); out != "" {
		found.Found = true
		found.BinaryPath = strings.TrimSpace(strings.Split(out, "\n")[0])

		probe := fmt.Sprintf("timeout 5 %s -version </dev/null 2>/dev/null | head -1",
			ShellQuote(found.BinaryPath))
		if version := d.session.Output(ctx, probe); version != "" {
			found.Version = strings.TrimSpace(version)
		}
	}

	if out := d.session.Output(ctx, "ls "+ShellQuote(d.opts.ConfigPath)+" 2>/dev/null"); out != "" {
		found.Found = true
	}

	switch {
	case d.session.Output(ctx,
		"systemctl cat openfrps >/dev/null 2>&1 && echo yes") == "yes":
		found.Found = true
		found.Init = "systemd"
		found.ServiceName = "openfrps"
		found.Running = d.session.Output(ctx,
			"systemctl is-active openfrps 2>/dev/null") == "active"

	case d.session.Output(ctx, "ls /etc/init.d/openfrps 2>/dev/null") != "":
		found.Found = true
		found.ServiceName = "openfrps"
		found.Init = "sysvinit"
		if d.session.Output(ctx, "command -v rc-service 2>/dev/null") != "" {
			found.Init = "openrc"
			found.Running = strings.Contains(
				d.session.Output(ctx, "rc-service openfrps status 2>/dev/null"), "started")
		} else {
			found.Running = strings.Contains(
				d.session.Output(ctx, "/etc/init.d/openfrps status 2>/dev/null"), "running")
		}
	}

	return found
}

func (d *Deployer) removeExisting(ctx context.Context, report stepReporter, found Existing) error {
	if !found.Found {
		return nil
	}

	report.Infof("found an existing installation: %s", found.Describe())

	if found.ServiceName != "" {
		switch found.Init {
		case "systemd":

			d.session.Run(ctx, "systemctl stop openfrps 2>/dev/null || true")
			d.session.Run(ctx, "systemctl disable openfrps 2>/dev/null || true")
			d.session.Run(ctx, "rm -f /etc/systemd/system/openfrps.service "+
				"/lib/systemd/system/openfrps.service")
			d.session.Run(ctx, "systemctl daemon-reload 2>/dev/null || true")

		case "openrc":
			d.session.Run(ctx, "rc-service openfrps stop 2>/dev/null || true")
			d.session.Run(ctx, "rc-update del openfrps default 2>/dev/null || true")
			d.session.Run(ctx, "rm -f /etc/init.d/openfrps")

		default:
			d.session.Run(ctx, "/etc/init.d/openfrps stop 2>/dev/null || true")
			d.session.Run(ctx, "rm -f /etc/init.d/openfrps")
		}
		report.Infof("removed the %s service", found.Init)
	}

	d.session.Run(ctx, "pkill -x openfrps 2>/dev/null || true")

	if found.ConfigPath != "" {
		d.session.Run(ctx, "rm -f "+ShellQuote(found.ConfigPath))
	}

	report.Successf("cleared the previous installation")
	return nil
}
