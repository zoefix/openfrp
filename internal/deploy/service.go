package deploy

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"text/template"

	"github.com/zoefix/openfrp/internal/deploy/detect"
)

//go:embed templates/*
var templateFS embed.FS

// serviceParams fill a unit template.
type serviceParams struct {
	BinaryPath string
	ConfigPath string
	StateDir   string
	User       string
}

// serviceUnit describes where a unit lives and how it is controlled, for one
// init system.
type serviceUnit struct {
	template string
	path     string
	mode     uint32

	enable  []string
	start   []string
	restart []string
	status  []string
}

func unitFor(init detect.InitSystem) (serviceUnit, error) {
	switch init {
	case detect.InitSystemd:
		return serviceUnit{
			template: "templates/openfrps.service",
			path:     "/etc/systemd/system/openfrps.service",
			mode:     0o644,
			enable:   []string{"systemctl daemon-reload", "systemctl enable openfrps"},
			start:    []string{"systemctl start openfrps"},
			restart:  []string{"systemctl restart openfrps"},
			status:   []string{"systemctl is-active openfrps"},
		}, nil

	case detect.InitOpenRC:
		return serviceUnit{
			template: "templates/openfrps.openrc",
			path:     "/etc/init.d/openfrps",
			mode:     0o755,
			enable:   []string{"rc-update add openfrps default"},
			start:    []string{"rc-service openfrps start"},
			restart:  []string{"rc-service openfrps restart"},
			status:   []string{"rc-service openfrps status"},
		}, nil

	case detect.InitSysVInit:
		return serviceUnit{
			template: "templates/openfrps.sysvinit",
			path:     "/etc/init.d/openfrps",
			mode:     0o755,
			// update-rc.d on Debian derivatives, chkconfig on RHEL ones. Try
			// both and let the one that is absent fail harmlessly.
			enable:  []string{"update-rc.d openfrps defaults 2>/dev/null || chkconfig --add openfrps 2>/dev/null || true"},
			start:   []string{"/etc/init.d/openfrps start"},
			restart: []string{"/etc/init.d/openfrps restart"},
			status:  []string{"/etc/init.d/openfrps status"},
		}, nil

	default:
		return serviceUnit{}, fmt.Errorf(
			"deploy: cannot manage services under %q; supported: systemd, OpenRC, sysvinit", init)
	}
}

// renderUnit fills the template for the detected init system.
func renderUnit(init detect.InitSystem, params serviceParams) ([]byte, serviceUnit, error) {
	unit, err := unitFor(init)
	if err != nil {
		return nil, serviceUnit{}, err
	}

	raw, err := templateFS.ReadFile(unit.template)
	if err != nil {
		return nil, unit, fmt.Errorf("deploy: read %s: %w", unit.template, err)
	}

	tmpl, err := template.New("unit").Parse(string(raw))
	if err != nil {
		return nil, unit, fmt.Errorf("deploy: parse %s: %w", unit.template, err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, params); err != nil {
		return nil, unit, fmt.Errorf("deploy: render %s: %w", unit.template, err)
	}
	return out.Bytes(), unit, nil
}

// installService writes the unit and brings the service up.
func (d *Deployer) installService(ctx context.Context, report stepReporter, info *detect.Result) error {
	params := serviceParams{
		BinaryPath: d.opts.BinaryPath,
		ConfigPath: d.opts.ConfigPath,
		StateDir:   d.opts.StateDir,
		User:       d.opts.ServiceUser,
	}

	content, unit, err := renderUnit(info.Init, params)
	if err != nil {
		return err
	}

	if d.opts.DryRun {
		report.Infof("would install %s (%s) and enable it", unit.path, info.Init)
		return nil
	}

	if err := d.session.WriteFile(ctx, unit.path, content, 0o644); err != nil {
		return err
	}
	if unit.mode != 0o644 {
		if _, err := d.session.Run(ctx, fmt.Sprintf("chmod %o %s", unit.mode, ShellQuote(unit.path))); err != nil {
			return err
		}
	}
	report.Infof("installed %s", unit.path)

	for _, command := range unit.enable {
		if _, err := d.session.Run(ctx, command); err != nil {
			return fmt.Errorf("deploy: enable service: %w", err)
		}
	}

	// Restart rather than start: a re-run is an in-place upgrade, and the
	// service is very likely already running.
	for _, command := range unit.restart {
		if _, err := d.session.Run(ctx, command); err != nil {
			return fmt.Errorf("deploy: start service: %w", err)
		}
	}

	report.Successf("service enabled and started under %s", info.Init)
	return nil
}

// ensureServiceUser creates the unprivileged account the daemon runs as.
func (d *Deployer) ensureServiceUser(ctx context.Context, report stepReporter) error {
	user := d.opts.ServiceUser
	if user == "root" {
		report.Warnf("running the server as root; set a service user instead")
		return nil
	}

	exists := d.session.Output(ctx,
		"id "+ShellQuote(user)+" >/dev/null 2>&1 && echo yes || echo no") == "yes"
	if exists {
		return nil
	}

	if d.opts.DryRun {
		report.Infof("would create system user %q", user)
		return nil
	}

	// useradd on most distributions, adduser on Alpine. Neither is universal.
	command := fmt.Sprintf(
		"useradd --system --no-create-home --shell /usr/sbin/nologin %s 2>/dev/null || "+
			"adduser -S -D -H -s /sbin/nologin %s 2>/dev/null || true",
		ShellQuote(user), ShellQuote(user))
	if _, err := d.session.Run(ctx, command); err != nil {
		return fmt.Errorf("deploy: create service user: %w", err)
	}

	report.Infof("created system user %q", user)
	return nil
}
