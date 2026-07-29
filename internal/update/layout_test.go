package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestReleaseBundleIsInstallable builds a real bundle and installs it through
// the updater.
//
// The release script and the installer are two halves of one agreement about
// what a bundle contains and where it may write, and they live in different
// languages in different directories. Nothing else notices when they drift —
// the failure would appear on a user's router at the moment they press update,
// with the old version already replaced.
func TestReleaseBundleIsInstallable(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the release bundle")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the bundle script is POSIX shell")
	}

	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locate the repository: %v", err)
	}
	script := filepath.Join(repo, "scripts", "bundle.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("no bundle script: %v", err)
	}

	dist := t.TempDir()
	cmd := exec.Command("sh", script)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"VERSION=0.0.1",
		"COMMIT=test",
		"DATE=2026-01-01T00:00:00Z",
		"PLATFORMS=linux/"+runtime.GOARCH,
		"DIST="+dist,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the bundle: %v\n%s", err, out)
	}

	name := BundleName("v0.0.1", "linux", runtime.GOARCH)
	bundle, err := os.Open(filepath.Join(dist, name))
	if err != nil {
		t.Fatalf("the script did not produce %s: %v", name, err)
	}
	defer bundle.Close()

	unpacked := t.TempDir()
	written, err := Extract(bundle, unpacked)
	if err != nil {
		t.Fatalf("the installer refused the bundle the release script builds: %v", err)
	}

	// Every file the router runs has to be in there. A bundle missing the
	// interface would install a new backend under an old form.
	required := []string{
		"usr/bin/openfrpc",
		"usr/lib/openfrp/openfrps",
		"usr/libexec/openfrp/render",
		"usr/libexec/openfrp/job",
		"usr/share/rpcd/ucode/openfrp.uc",
		"www/luci-static/resources/view/openfrp/status.js",
		"www/luci-static/resources/view/openfrp/tunnels.js",
		"www/luci-static/resources/openfrp/schema-form.js",
		"usr/lib/lua/luci/i18n/openfrp.zh-cn.lmo",
	}
	for _, want := range required {
		if !slices.Contains(written, want) {
			t.Errorf("the bundle has no %s; an update would leave the router "+
				"with a mix of versions", want)
		}
	}

	for _, name := range []string{"usr/bin/openfrpc", "usr/libexec/openfrp/render", "usr/libexec/openfrp/job"} {
		info, err := os.Stat(filepath.Join(unpacked, name))
		if err != nil {
			continue
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("%s came out of the bundle without its executable bit", name)
		}
	}

	sums, err := os.ReadFile(filepath.Join(dist, ChecksumsName))
	if err != nil {
		t.Fatalf("the script published no %s, which the installer requires: %v",
			ChecksumsName, err)
	}
	parsed := ParseChecksums(strings.NewReader(string(sums)))
	if _, ok := parsed[name]; !ok {
		t.Errorf("%s does not list %s, so the installer would refuse it",
			ChecksumsName, name)
	}
}
