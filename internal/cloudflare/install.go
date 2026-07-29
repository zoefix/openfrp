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

const ReleaseURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-{arch}"

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

func DownloadURL(arch string) string {

	if arch == "arm" {
		arch = "arm"
	}
	return replaceArch(ReleaseURL, arch)
}

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

func usable(ctx context.Context, path string) bool {
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	return exec.CommandContext(ctx, path, "--version").Run() == nil
}

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

	return os.Rename(temporary, path)
}
