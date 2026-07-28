package cloudflare

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// ReleaseURL is where a cloudflared build is fetched from when the router has
// none. {arch} is replaced with the Go architecture name, which is what
// Cloudflare happens to publish these under.
const ReleaseURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-{arch}"

// Install makes sure a usable cloudflared is at path.
//
// Downloaded rather than packaged. cloudflared is around thirty megabytes,
// which is more than the entire flash of a typical router — bundling it would
// make this plugin uninstallable on hardware that has no use for the feature.
// A router with room fetches it once, when the operator asks for it.
func Install(ctx context.Context, path string, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}

	if usable(ctx, path) {
		progress(fmt.Sprintf("cloudflared is already installed at %s", path))
		return nil
	}

	url := DownloadURL(runtime.GOARCH)
	progress("downloading cloudflared from " + url)

	if err := download(ctx, url, path); err != nil {
		return err
	}
	if !usable(ctx, path) {
		return fmt.Errorf("cloudflare: %s was downloaded but will not run — "+
			"the build for %s may be wrong for this router",
			path, runtime.GOARCH)
	}

	progress("installed cloudflared at " + path)
	return nil
}

// DownloadURL is where the build for one architecture lives.
func DownloadURL(arch string) string {
	// Cloudflare publishes arm rather than armv7, and has no mips build at
	// all — which is checked for by the caller, because the message that
	// helps says what to do instead and this only knows the URL.
	if arch == "arm" {
		arch = "arm"
	}
	return replaceArch(ReleaseURL, arch)
}

// Supported reports whether Cloudflare publishes a build for an architecture.
//
// Named rather than discovered by a failing download: a router that will never
// have a build should be told so before it spends a minute fetching a 404.
func Supported(arch string) bool {
	switch arch {
	case "amd64", "arm64", "arm", "386":
		return true
	}
	return false
}

func replaceArch(template, arch string) string {
	out := ""
	for i := 0; i < len(template); i++ {
		if i+6 <= len(template) && template[i:i+6] == "{arch}" {
			out += arch
			i += 5
			continue
		}
		out += string(template[i])
	}
	return out
}

// usable reports whether the file at path runs and answers as cloudflared.
func usable(ctx context.Context, path string) bool {
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// --version rather than a size or hash check: a binary for the wrong
	// architecture is the failure worth catching, and it fails here.
	return exec.CommandContext(ctx, path, "--version").Run() == nil
}

// download fetches a binary and puts it in place atomically.
func download(ctx context.Context, url, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: fetching cloudflared: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudflare: %s returned %s", url, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	temporary := path + ".new"
	out, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(temporary)
		return fmt.Errorf("cloudflare: writing %s: %w", path, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(temporary)
		return err
	}

	// Renamed into place so an interrupted download is never the file that
	// runs, and so an upgrade does not blank the binary a running process is
	// executing from.
	return os.Rename(temporary, path)
}
