package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	downloadTimeout = 10 * time.Minute

	serviceSettleTime = 8 * time.Second
)

type Options struct {
	Root string

	ServiceCommand []string

	StageDir string

	Client *Client

	Log io.Writer
}

func (o *Options) applyDefaults() {
	if o.Root == "" {
		o.Root = "/"
	}
	if len(o.ServiceCommand) == 0 {
		o.ServiceCommand = []string{"/etc/init.d/openfrp", "restart"}
	}
	if o.StageDir == "" {
		o.StageDir = "/tmp"
	}
	if o.Client == nil {
		o.Client = NewClient()
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
}

func (o *Options) logf(format string, args ...any) {
	fmt.Fprintf(o.Log, format+"\n", args...)
}

// ParseChecksums reads the sha256sum-style file a release publishes.
func ParseChecksums(r io.Reader) map[string]string {
	sums := map[string]string{}

	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		sums[name] = strings.ToLower(fields[0])
	}
	return sums
}

func (c *Client) fetchTo(ctx context.Context, url string, dst io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "openfrp-updater")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	download := *client
	download.Timeout = downloadTimeout

	resp, err := download.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update: %s returned %s", url, resp.Status)
	}

	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, digest), io.LimitReader(resp.Body, maxBundleSize)); err != nil {
		return "", fmt.Errorf("update: download %s: %w", url, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Apply installs the newest release over the running one.
func Apply(ctx context.Context, opts Options) error {
	opts.applyDefaults()

	release, err := opts.Client.Latest(ctx)
	if err != nil {
		return err
	}
	opts.logf("==> latest release is %s", release.Tag)

	asset, ok := release.AssetFor(opts.Client.GOOS, opts.Client.GOARCH)
	if !ok {
		return fmt.Errorf("update: %s has no build for %s/%s",
			release.Tag, opts.Client.GOOS, opts.Client.GOARCH)
	}

	// The checksum has to come from the release rather than from the download,
	// or it would only prove the bytes arrived intact from whoever sent them.
	sumsAsset, ok := release.Checksums()
	if !ok {
		return fmt.Errorf("update: %s publishes no %s, so the download cannot "+
			"be verified; refusing to install it", release.Tag, ChecksumsName)
	}

	var sumsRaw strings.Builder
	if _, err := opts.Client.fetchTo(ctx, sumsAsset.URL, &sumsRaw); err != nil {
		return err
	}
	want, ok := ParseChecksums(strings.NewReader(sumsRaw.String()))[asset.Name]
	if !ok {
		return fmt.Errorf("update: %s is not listed in %s; refusing to install "+
			"an unverifiable download", asset.Name, ChecksumsName)
	}

	stage, err := os.MkdirTemp(opts.StageDir, "openfrp-update-")
	if err != nil {
		return fmt.Errorf("update: make a staging directory: %w", err)
	}
	defer os.RemoveAll(stage)

	bundlePath := filepath.Join(stage, asset.Name)
	file, err := os.Create(bundlePath)
	if err != nil {
		return err
	}

	opts.logf("==> downloading %s (%.1f MiB)", asset.Name, float64(asset.Size)/(1<<20))
	got, err := opts.Client.fetchTo(ctx, asset.URL, file)
	file.Close()
	if err != nil {
		return err
	}

	if got != want {
		return fmt.Errorf("update: checksum mismatch for %s: got %s, expected %s; "+
			"the download was not what the release published", asset.Name, got, want)
	}
	opts.logf("==> checksum verified (sha256 %s…)", got[:12])

	unpacked := filepath.Join(stage, "root")
	bundle, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	written, err := Extract(bundle, unpacked)
	bundle.Close()
	if err != nil {
		return err
	}
	opts.logf("==> unpacked %d files", len(written))

	if err := smokeTest(ctx, filepath.Join(unpacked, "usr/bin/openfrpc")); err != nil {
		return err
	}
	opts.logf("==> the new client runs on this machine")

	backup := filepath.Join(stage, "backup")
	if err := install(opts, unpacked, backup, written); err != nil {
		opts.logf("==> install failed: %v", err)
		if restoreErr := restore(opts, backup, written); restoreErr != nil {
			return fmt.Errorf("update: install failed (%w) and the previous "+
				"version could not be put back: %v", err, restoreErr)
		}
		opts.logf("==> the previous version was put back")
		return err
	}
	opts.logf("==> installed %s", release.Tag)

	if err := restart(ctx, opts); err != nil {
		opts.logf("==> %v", err)
		opts.logf("==> rolling back")

		if restoreErr := restore(opts, backup, written); restoreErr != nil {
			return fmt.Errorf("update: %s did not start (%w) and the previous "+
				"version could not be put back: %v", release.Tag, err, restoreErr)
		}
		if restartErr := restart(ctx, opts); restartErr != nil {
			return fmt.Errorf("update: %s did not start, and neither did the "+
				"version it replaced: %v", release.Tag, restartErr)
		}
		return fmt.Errorf("update: %s did not start, so the previous version "+
			"was put back and is running again: %w", release.Tag, err)
	}

	opts.logf("==> %s is running", release.Tag)
	return nil
}

// smokeTest runs the downloaded client before anything is replaced.
//
// A build for the wrong architecture, or one that is subtly broken, is far
// cheaper to find here than after it has overwritten the working one.
func smokeTest(ctx context.Context, binary string) error {
	info, err := os.Stat(binary)
	if err != nil {
		return fmt.Errorf("update: the bundle has no usr/bin/openfrpc: %w", err)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("update: the client in the bundle is not executable")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, binary, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("update: the client in the bundle does not run here: %w (%s)",
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

func install(opts Options, from, backup string, files []string) error {
	for _, name := range files {
		target := filepath.Join(opts.Root, name)

		if _, err := os.Stat(target); err == nil {
			saved := filepath.Join(backup, name)
			if err := os.MkdirAll(filepath.Dir(saved), 0o755); err != nil {
				return err
			}
			if err := copyFile(target, saved); err != nil {
				return fmt.Errorf("update: back up %s: %w", target, err)
			}
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := replace(filepath.Join(from, name), target); err != nil {
			return err
		}
	}
	return nil
}

func restore(opts Options, backup string, files []string) error {
	var failed error
	for _, name := range files {
		saved := filepath.Join(backup, name)
		if _, err := os.Stat(saved); err != nil {
			continue
		}
		if err := replace(saved, filepath.Join(opts.Root, name)); err != nil && failed == nil {
			failed = err
		}
	}
	return failed
}

// replace swaps a file in place.
//
// Written beside the target and renamed, because a binary cannot be written
// over while it is running, and a half-written one is worse than either
// version.
func replace(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	tmp := dst + ".openfrp-new"
	if err := copyFile(src, tmp); err != nil {
		return err
	}
	if err := os.Chmod(tmp, info.Mode().Perm()); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("update: replace %s: %w", dst, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func restart(ctx context.Context, opts Options) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, opts.ServiceCommand[0], opts.ServiceCommand[1:]...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update: restart the service: %w (%s)",
			err, strings.TrimSpace(string(out)))
	}

	// Coming back up is what proves the install, not the restart command
	// returning zero: an init script exits long before the daemon has decided
	// whether it can read its config.
	time.Sleep(serviceSettleTime)

	installed := filepath.Join(opts.Root, "usr/bin/openfrpc")
	out, err := exec.CommandContext(ctx, installed, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("update: the installed client does not run: %w (%s)",
			err, strings.TrimSpace(string(out)))
	}
	return nil
}
