package update

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxBundleSize = 128 << 20

// The modes an install must end up with, set explicitly rather than left to
// whatever umask the caller happened to have.
const (
	FileMode = os.FileMode(0o644)
	ExecMode = os.FileMode(0o755)
	DirMode  = os.FileMode(0o755)
)

// MkdirAllMode creates a directory chain and fixes the mode of what it made.
//
// MkdirAll applies the umask, so under the job worker's 077 a fresh directory
// comes out 0700 and nothing inside it can be served.
func MkdirAllMode(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	return os.Chmod(dir, DirMode)
}

// allowedPrefixes bounds where a bundle may write.
//
// A release is fetched over the network and unpacked by root. Without a
// destination allowlist a tarball that named etc/passwd or etc/init.d/ would
// be obeyed, so the archive decides only which of these files it replaces,
// never which places exist to replace.
var allowedPrefixes = []string{
	"usr/lib/openfrp/",
	"usr/libexec/openfrp/",
	"usr/lib/lua/luci/i18n/openfrp.",
	"www/luci-static/resources/openfrp/",
	"www/luci-static/resources/view/openfrp/",
}

// allowedExact are the single files a release replaces outside its own
// directories.
//
// Named in full rather than by prefix. etc/init.d/ has to be reachable so a
// release can update its own service scripts, and a prefix of
// "etc/init.d/openfrp" would also accept etc/init.d/openfrpanything.
// /etc/config/openfrp is deliberately absent: that is the operator's
// configuration and no release may write over it.
var allowedExact = map[string]bool{
	"etc/init.d/openfrp":                          true,
	"etc/init.d/openfrp-cloudflared":              true,
	"usr/bin/openfrpc":                            true,
	"usr/share/luci/menu.d/luci-app-openfrp.json": true,
	"usr/share/rpcd/acl.d/luci-app-openfrp.json":  true,
	"usr/share/rpcd/ucode/openfrp.uc":             true,
}

func permitted(name string) bool {
	if allowedExact[name] {
		return true
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// cleanEntry rejects anything that would escape the staging directory.
func cleanEntry(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("update: the bundle has an unnamed entry")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("update: the bundle names an absolute path %q", name)
	}

	cleaned := filepath.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("update: the bundle names a path outside itself: %q", name)
	}
	return cleaned, nil
}

// Extract unpacks a bundle into dir and reports the files it wrote, relative
// to the bundle root.
func Extract(archive io.Reader, dir string) ([]string, error) {
	gz, err := gzip.NewReader(io.LimitReader(archive, maxBundleSize))
	if err != nil {
		return nil, fmt.Errorf("update: the bundle is not gzip: %w", err)
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	var written []string

	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("update: read the bundle: %w", err)
		}

		name, err := cleanEntry(header.Name)
		if err != nil {
			return nil, err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			continue

		case tar.TypeReg:

		default:
			// A symlink in the archive could point anywhere once installed,
			// and nothing in a release needs one.
			return nil, fmt.Errorf("update: the bundle holds %q, which is not a regular file", name)
		}

		if !permitted(name) {
			return nil, fmt.Errorf("update: the bundle writes to %q, which is "+
				"outside what an update may replace", name)
		}

		target := filepath.Join(dir, name)
		if err := MkdirAllMode(filepath.Dir(target)); err != nil {
			return nil, err
		}

		mode := FileMode
		if header.FileInfo().Mode()&0o111 != 0 {
			mode = ExecMode
		}

		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(file, io.LimitReader(reader, maxBundleSize)); err != nil {
			file.Close()
			return nil, fmt.Errorf("update: unpack %q: %w", name, err)
		}
		if err := file.Close(); err != nil {
			return nil, err
		}

		// The mode passed to open is masked by the process umask, and the job
		// worker that runs an update sets 077. That turned every installed
		// file 0600 and every directory 0700, which a web server answers as
		// 403 — the interface stopped loading while the update reported
		// success. Chmod is not masked, so the mode asked for is the mode set.
		if err := os.Chmod(target, mode); err != nil {
			return nil, err
		}

		written = append(written, name)
	}

	if len(written) == 0 {
		return nil, fmt.Errorf("update: the bundle is empty")
	}
	return written, nil
}
