package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/deploy/detect"
)

// installBinary puts the server binary in place.
//
// Two delivery routes, tried in order. Uploading from here is preferred: it
// works on a server with no outbound internet, and the bytes are the ones we
// verified rather than whatever a mirror served. Downloading on the server is
// the fallback for when no local copy exists for the target architecture.
func (d *Deployer) installBinary(ctx context.Context, report stepReporter, info *detect.Result) error {
	if d.opts.DryRun {
		report.Infof("would install the %s server binary at %s", info.Arch, d.opts.BinaryPath)
		return nil
	}

	if d.opts.LocalBinary != "" {
		return d.uploadBinary(ctx, report)
	}
	return d.downloadBinary(ctx, report, info)
}

func (d *Deployer) uploadBinary(ctx context.Context, report stepReporter) error {
	content, err := os.ReadFile(d.opts.LocalBinary)
	if err != nil {
		return fmt.Errorf("deploy: read %s: %w", d.opts.LocalBinary, err)
	}

	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	// Skip the transfer when the same binary is already there. Re-running a
	// deployment to change one config value should not push megabytes again.
	existing := d.session.Output(ctx,
		"sha256sum "+ShellQuote(d.opts.BinaryPath)+" 2>/dev/null | cut -d' ' -f1")
	if existing == want {
		report.Infof("server binary is already current (%s…)", want[:12])
		return nil
	}

	report.Infof("uploading %d KiB to %s", len(content)/1024, d.opts.BinaryPath)

	// Stop the service first: writing over a running executable fails with
	// ETXTBSY on Linux.
	d.session.Run(ctx, "systemctl stop openfrps 2>/dev/null || "+
		"rc-service openfrps stop 2>/dev/null || "+
		"/etc/init.d/openfrps stop 2>/dev/null || true")

	if err := d.session.WriteFile(ctx, d.opts.BinaryPath, content, 0o755); err != nil {
		return err
	}

	got := d.session.Output(ctx,
		"sha256sum "+ShellQuote(d.opts.BinaryPath)+" 2>/dev/null | cut -d' ' -f1")
	if got != "" && got != want {
		return fmt.Errorf("deploy: checksum mismatch after upload: got %s, want %s", got, want)
	}

	report.Successf("uploaded and verified (sha256 %s…)", want[:12])
	return nil
}

func (d *Deployer) downloadBinary(ctx context.Context, report stepReporter, info *detect.Result) error {
	if d.opts.ReleaseURL == "" {
		return fmt.Errorf(
			"deploy: no local binary for %s and no release URL configured; "+
				"build one with GOARCH=%s and pass --binary", info.Arch, info.Arch)
	}

	url := strings.NewReplacer("{arch}", info.Arch, "{os}", "linux").Replace(d.opts.ReleaseURL)
	report.Infof("downloading %s", url)

	fetch := fmt.Sprintf(
		"curl -fsSL -o %s.tmp %s || wget -qO %s.tmp %s",
		ShellQuote(d.opts.BinaryPath), ShellQuote(url),
		ShellQuote(d.opts.BinaryPath), ShellQuote(url))
	if _, err := d.session.Run(ctx, fetch); err != nil {
		return fmt.Errorf("deploy: download server binary: %w", err)
	}

	if d.opts.SHA256 != "" {
		got := d.session.Output(ctx,
			"sha256sum "+ShellQuote(d.opts.BinaryPath+".tmp")+" | cut -d' ' -f1")
		if got != d.opts.SHA256 {
			d.session.Run(ctx, "rm -f "+ShellQuote(d.opts.BinaryPath+".tmp"))
			return fmt.Errorf("deploy: checksum mismatch: got %s, want %s", got, d.opts.SHA256)
		}
		report.Infof("checksum verified")
	} else {
		report.Warnf("no checksum configured; the download was not verified")
	}

	move := fmt.Sprintf("chmod 0755 %s.tmp && mv %s.tmp %s",
		ShellQuote(d.opts.BinaryPath), ShellQuote(d.opts.BinaryPath),
		ShellQuote(d.opts.BinaryPath))
	if _, err := d.session.Run(ctx, move); err != nil {
		return err
	}

	report.Successf("installed %s", d.opts.BinaryPath)
	return nil
}

// installConfig writes the server configuration.
func (d *Deployer) installConfig(ctx context.Context, report stepReporter) error {
	cfg := config.Server{
		BindAddr:       "0.0.0.0",
		BindPort:       d.opts.BindPort,
		Token:          d.opts.Token,
		VhostHTTPPort:  d.opts.VhostHTTPPort,
		VhostHTTPSPort: d.opts.VhostHTTPSPort,
		Log:            config.Log{Level: "info", Format: "text"},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("deploy: server config: %w", err)
	}

	content, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("deploy: encode server config: %w", err)
	}
	content = append(content, '\n')

	if d.opts.DryRun {
		report.Infof("would write %s", d.opts.ConfigPath)
		return nil
	}

	// 0640 and owned by the service user: the file holds the shared token.
	if err := d.session.WriteFile(ctx, d.opts.ConfigPath, content, 0o640); err != nil {
		return err
	}

	own := fmt.Sprintf("chown %s: %s 2>/dev/null || true",
		ShellQuote(d.opts.ServiceUser), ShellQuote(d.opts.ConfigPath))
	d.session.Run(ctx, own)

	mkdir := fmt.Sprintf("mkdir -p %s && chown %s: %s && chmod 0750 %s",
		ShellQuote(d.opts.StateDir), ShellQuote(d.opts.ServiceUser),
		ShellQuote(d.opts.StateDir), ShellQuote(d.opts.StateDir))
	if _, err := d.session.Run(ctx, mkdir); err != nil {
		return fmt.Errorf("deploy: create state directory: %w", err)
	}

	report.Successf("wrote %s", d.opts.ConfigPath)
	return nil
}
