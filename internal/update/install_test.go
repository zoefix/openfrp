package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeRelease struct {
	tag      string
	bundle   []byte
	sums     string
	omitSums bool
}

func newBundle(t *testing.T, clientBody string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	files := []struct {
		name, body string
		mode       int64
	}{
		{"usr/bin/openfrpc", clientBody, 0o755},
		{"www/luci-static/resources/view/openfrp/status.js", "new-status-js", 0o644},
		{"usr/libexec/openfrp/render", "#!/bin/sh\nexit 0\n", 0o755},
	}
	for _, f := range files {
		tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
		})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func startFakeGitHub(t *testing.T, rel fakeRelease) *Client {
	t.Helper()

	bundleName := BundleName(rel.tag, runtime.GOOS, runtime.GOARCH)

	mux := http.NewServeMux()
	var base string

	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]any{
			{"name": bundleName, "browser_download_url": base + "/dl/" + bundleName,
				"size": len(rel.bundle)},
		}
		if !rel.omitSums {
			assets = append(assets, map[string]any{
				"name": ChecksumsName, "browser_download_url": base + "/dl/" + ChecksumsName,
			})
		}
		json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": rel.tag, "name": rel.tag, "body": "what changed",
			"draft": false, "prerelease": false,
			"published_at": time.Now().Format(time.RFC3339),
			"html_url":     "https://example.invalid/release",
			"assets":       assets,
		}})
	})

	mux.HandleFunc("/dl/"+bundleName, func(w http.ResponseWriter, r *http.Request) {
		w.Write(rel.bundle)
	})
	mux.HandleFunc("/dl/"+ChecksumsName, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, rel.sums)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL

	return &Client{
		Repo:    "zoefix/openfrp",
		BaseURL: srv.URL,
		HTTP:    srv.Client(),
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
	}
}

func checksumsFor(tag string, bundle []byte) string {
	sum := sha256.Sum256(bundle)
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]),
		BundleName(tag, runtime.GOOS, runtime.GOARCH))
}

// existingRoot lays down what an installed router looks like, so an update has
// something to replace and something to roll back to.
func existingRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"usr/bin/openfrpc": "#!/bin/sh\necho openfrpc v0.3.0\n",
		"www/luci-static/resources/view/openfrp/status.js": "old-status-js",
		"usr/libexec/openfrp/render":                       "#!/bin/sh\nexit 0\n",
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, "openfrpc") || strings.HasSuffix(name, "render") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	return root
}

func workingClient() string { return "#!/bin/sh\necho openfrpc v0.4.0\n" }
func brokenClient() string  { return "#!/bin/sh\nexit 1\n" }
func restartScript(t *testing.T, ok bool) []string {
	t.Helper()

	body := "#!/bin/sh\nexit 0\n"
	if !ok {
		body = "#!/bin/sh\nexit 1\n"
	}
	path := filepath.Join(t.TempDir(), "service")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write service script: %v", err)
	}
	return []string{path, "restart"}
}

func TestApplyInstallsAVerifiedRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake client is a shell script")
	}

	bundle := newBundle(t, workingClient())
	client := startFakeGitHub(t, fakeRelease{
		tag: "v0.4.0", bundle: bundle, sums: checksumsFor("v0.4.0", bundle),
	})

	root := existingRoot(t)
	var log bytes.Buffer

	err := Apply(context.Background(), Options{
		Root: root, Client: client, StageDir: t.TempDir(),
		ServiceCommand: restartScript(t, true), Log: &log,
	})
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, log.String())
	}

	js, _ := os.ReadFile(filepath.Join(root, "www/luci-static/resources/view/openfrp/status.js"))
	if string(js) != "new-status-js" {
		t.Errorf("the interface was left at %q; an update replaces the whole "+
			"file set, not just the binary", js)
	}
	binary, _ := os.ReadFile(filepath.Join(root, "usr/bin/openfrpc"))
	if !strings.Contains(string(binary), "v0.4.0") {
		t.Error("the client was not replaced")
	}
}

// TestApplyRefusesAnAlteredDownload is the security case.
func TestApplyRefusesAnAlteredDownload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake client is a shell script")
	}

	bundle := newBundle(t, "#!/bin/sh\necho tampered\n")
	honest := newBundle(t, workingClient())

	client := startFakeGitHub(t, fakeRelease{
		tag: "v0.4.0", bundle: bundle, sums: checksumsFor("v0.4.0", honest),
	})

	root := existingRoot(t)
	err := Apply(context.Background(), Options{
		Root: root, Client: client, StageDir: t.TempDir(),
		ServiceCommand: restartScript(t, true),
	})
	if err == nil {
		t.Fatal("a bundle whose checksum did not match the release was installed")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("refused with %q, want a checksum complaint", err)
	}

	binary, _ := os.ReadFile(filepath.Join(root, "usr/bin/openfrpc"))
	if !strings.Contains(string(binary), "v0.3.0") {
		t.Error("the running install was touched despite the checksum failing")
	}
}

func TestApplyRefusesAReleaseWithNoChecksums(t *testing.T) {
	bundle := newBundle(t, workingClient())
	client := startFakeGitHub(t, fakeRelease{
		tag: "v0.4.0", bundle: bundle, omitSums: true,
	})

	err := Apply(context.Background(), Options{
		Root: existingRoot(t), Client: client, StageDir: t.TempDir(),
		ServiceCommand: restartScript(t, true),
	})
	if err == nil || !strings.Contains(err.Error(), ChecksumsName) {
		t.Fatalf("a release publishing no checksums was accepted (%v); an "+
			"unverifiable download must not be installed", err)
	}
}

// TestApplyRollsBackWhenTheServiceWillNotStart is what keeps a bad release
// from taking the router off the air.
func TestApplyRollsBackWhenTheServiceWillNotStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake client is a shell script")
	}

	bundle := newBundle(t, workingClient())
	client := startFakeGitHub(t, fakeRelease{
		tag: "v0.4.0", bundle: bundle, sums: checksumsFor("v0.4.0", bundle),
	})

	root := existingRoot(t)
	var log bytes.Buffer

	err := Apply(context.Background(), Options{
		Root: root, Client: client, StageDir: t.TempDir(),
		ServiceCommand: restartScript(t, false), Log: &log,
	})
	if err == nil {
		t.Fatal("an update whose service would not start reported success")
	}

	// Without this the test passes when the update never got as far as
	// installing, which is not the same as rolling back and looks identical
	// from the files alone.
	if !strings.Contains(log.String(), "rolling back") {
		t.Fatalf("the update never reached the install, so the rollback was "+
			"never exercised; log was:\n%s", log.String())
	}

	binary, _ := os.ReadFile(filepath.Join(root, "usr/bin/openfrpc"))
	if !strings.Contains(string(binary), "v0.3.0") {
		t.Errorf("after a failed start the client is %q; the version that was "+
			"working must be put back", binary)
	}
	js, _ := os.ReadFile(filepath.Join(root, "www/luci-static/resources/view/openfrp/status.js"))
	if string(js) != "old-status-js" {
		t.Errorf("the interface was left at %q after a rollback; every file the "+
			"update replaced has to go back, not just the binary", js)
	}
}

func TestApplyRejectsABundleForAnotherArchitecture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake client is a shell script")
	}

	bundle := newBundle(t, brokenClient())
	client := startFakeGitHub(t, fakeRelease{
		tag: "v0.4.0", bundle: bundle, sums: checksumsFor("v0.4.0", bundle),
	})

	root := existingRoot(t)
	err := Apply(context.Background(), Options{
		Root: root, Client: client, StageDir: t.TempDir(),
		ServiceCommand: restartScript(t, true),
	})
	if err == nil {
		t.Fatal("a client that cannot run here was installed")
	}
	if !strings.Contains(err.Error(), "does not run here") {
		t.Errorf("refused with %q, want the smoke test to be what caught it", err)
	}

	binary, _ := os.ReadFile(filepath.Join(root, "usr/bin/openfrpc"))
	if !strings.Contains(string(binary), "v0.3.0") {
		t.Error("the working client was replaced before the new one was tried")
	}
}

func TestParseChecksumsReadsTheUsualFormat(t *testing.T) {
	sums := ParseChecksums(strings.NewReader(
		"abc123  openfrp-0.4.0-linux-amd64.tar.gz\n" +
			"def456 *openfrp-0.4.0-linux-arm64.tar.gz\n" +
			"garbage line\n"))

	if sums["openfrp-0.4.0-linux-amd64.tar.gz"] != "abc123" {
		t.Error("a plain sha256sum line was not read")
	}
	if sums["openfrp-0.4.0-linux-arm64.tar.gz"] != "def456" {
		t.Error("the binary-mode * prefix sha256sum writes was not handled")
	}
}
