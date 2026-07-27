package deploy

import (
	"context"
	"fmt"
	"strings"
)

// Existing describes an OpenFrp server already installed on the host.
type Existing struct {
	// Found is true if anything at all was left behind, including a service
	// unit with no binary or a config with no service.
	Found bool

	BinaryPath  string
	Version     string
	ServiceName string
	Running     bool
	ConfigPath  string

	// Init is the service manager the existing unit was installed under, which
	// is not necessarily the one being installed now: a host can be
	// re-provisioned after switching from sysvinit to systemd.
	Init string
}

// Describe summarises what was found, for a progress line.
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

// findExisting looks for a previous installation.
//
// Worth doing before installing rather than after: a second install over a
// first leaves whichever service manager the old one used still holding the
// port, and the symptom is a new binary that starts and immediately fails to
// bind. Detection is by observation rather than by assuming the paths this
// version happens to use, because the old one may not have used them.
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

		// -version is a flag, not a subcommand. Passing a bare word instead
		// makes the binary ignore it, load its configuration and start
		// serving — which on a host whose service is stopped would leave a
		// rogue daemon behind, started by the act of looking.
		//
		// Bounded and with stdin closed so a build that predates the flag
		// cannot block the deployment waiting on something.
		probe := fmt.Sprintf("timeout 5 %s -version </dev/null 2>/dev/null | head -1",
			ShellQuote(found.BinaryPath))
		if version := d.session.Output(ctx, probe); version != "" {
			found.Version = strings.TrimSpace(version)
		}
	}

	if out := d.session.Output(ctx, "ls "+ShellQuote(d.opts.ConfigPath)+" 2>/dev/null"); out != "" {
		found.Found = true
	}

	// Each service manager is asked in its own terms; a host may have more
	// than one installed and only one actually managing this service.
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

// removeExisting stops and clears a previous installation.
//
// The configuration and any state are removed along with the service. A
// redeployment writes a fresh configuration with a fresh token, so keeping the
// old one would leave a file that no longer describes the running service and
// a token nothing holds.
//
// The binary itself is left to the install step, which overwrites it and
// verifies the checksum. Deleting it here would open a window where the host
// has no server at all if the upload then failed.
func (d *Deployer) removeExisting(ctx context.Context, report stepReporter, found Existing) error {
	if !found.Found {
		return nil
	}

	report.Infof("found an existing installation: %s", found.Describe())

	if found.ServiceName != "" {
		switch found.Init {
		case "systemd":
			// Stop before disable: disabling a running unit leaves it running
			// and holding the port, which is exactly the failure this exists
			// to prevent.
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

	// Whatever the service manager thought, make sure nothing is still holding
	// the port. A process started by hand outside any service manager would
	// otherwise survive all of the above and make the new one fail to bind.
	d.session.Run(ctx, "pkill -x openfrps 2>/dev/null || true")

	if found.ConfigPath != "" {
		d.session.Run(ctx, "rm -f "+ShellQuote(found.ConfigPath))
	}

	report.Successf("cleared the previous installation")
	return nil
}
