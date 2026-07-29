package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	mode int64
	kind byte
	body string
	link string
}

func buildBundle(t *testing.T, entries []entry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:     e.name,
			Mode:     mode,
			Size:     int64(len(e.body)),
			Typeflag: kind,
			Linkname: e.link,
		}
		if kind != tar.TypeReg {
			header.Size = 0
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}

	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractWritesTheBundle(t *testing.T) {
	archive := buildBundle(t, []entry{
		{name: "usr/bin/openfrpc", mode: 0o755, body: "binary"},
		{name: "www/luci-static/resources/view/openfrp/status.js", body: "js"},
		{name: "usr/lib/lua/luci/i18n/openfrp.zh-cn.lmo", body: "lmo"},
	})

	dir := t.TempDir()
	written, err := Extract(bytes.NewReader(archive), dir)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(written) != 3 {
		t.Errorf("wrote %d files, want 3: %v", len(written), written)
	}

	info, err := os.Stat(filepath.Join(dir, "usr/bin/openfrpc"))
	if err != nil {
		t.Fatalf("stat the binary: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("the binary came out without its executable bit; the service " +
			"would not start after an update")
	}
}

// TestExtractRefusesToEscape is the one that matters.
//
// A bundle is fetched over the network and unpacked by root. An archive that
// names ../../etc or an absolute path would, unchecked, be obeyed.
func TestExtractRefusesToEscape(t *testing.T) {
	cases := []struct {
		name    string
		entries []entry
		want    string
	}{
		{
			name:    "parent traversal",
			entries: []entry{{name: "../../etc/passwd", body: "root::0:0"}},
			want:    "outside",
		},
		{
			name:    "absolute path",
			entries: []entry{{name: "/etc/passwd", body: "root::0:0"}},
			want:    "absolute",
		},
		{
			name:    "traversal in the middle",
			entries: []entry{{name: "usr/bin/../../../etc/passwd", body: "x"}},
			want:    "outside",
		},
		{
			name: "another package's init script",
			entries: []entry{
				{name: "etc/init.d/dropbear", mode: 0o755, body: "#!/bin/sh"},
			},
			want: "outside what an update may replace",
		},
		{
			// A release replaces etc/init.d/openfrp, and a prefix match on that
			// would have taken this too.
			name: "an init script merely starting with our name",
			entries: []entry{
				{name: "etc/init.d/openfrpevil", mode: 0o755, body: "#!/bin/sh"},
			},
			want: "outside what an update may replace",
		},
		{
			// The tunnels, tokens and limits an operator configured. A release
			// that could write here would silently discard them.
			name: "the operator's configuration",
			entries: []entry{
				{name: "etc/config/openfrp", body: "config global 'global'"},
			},
			want: "outside what an update may replace",
		},
		{
			name:    "a path with no relation to us at all",
			entries: []entry{{name: "etc/passwd", body: "root::0:0"}},
			want:    "outside what an update may replace",
		},
		{
			name: "symlink",
			entries: []entry{
				{name: "usr/bin/openfrpc", kind: tar.TypeSymlink, link: "/etc/shadow"},
			},
			want: "not a regular file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := Extract(bytes.NewReader(buildBundle(t, tc.entries)), dir)
			if err == nil {
				t.Fatalf("a bundle containing %q was unpacked; it must be refused",
					tc.entries[0].name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refused with %q, wanted something mentioning %q", err, tc.want)
			}

			if _, err := os.Stat("/etc/passwd.openfrp-test"); err == nil {
				t.Fatal("the extract wrote outside its directory")
			}
		})
	}
}

// TestExtractAcceptsTheServiceScripts pins the other half: a release has to be
// able to replace its own init scripts, or a fresh install has nothing to
// start.
func TestExtractAcceptsTheServiceScripts(t *testing.T) {
	archive := buildBundle(t, []entry{
		{name: "etc/init.d/openfrp", mode: 0o755, body: "#!/bin/sh"},
		{name: "etc/init.d/openfrp-cloudflared", mode: 0o755, body: "#!/bin/sh"},
		{name: "usr/share/luci/menu.d/luci-app-openfrp.json", body: "{}"},
		{name: "usr/share/rpcd/acl.d/luci-app-openfrp.json", body: "{}"},
	})

	written, err := Extract(bytes.NewReader(archive), t.TempDir())
	if err != nil {
		t.Fatalf("a bundle holding the files a fresh install needs was refused: %v", err)
	}
	if len(written) != 4 {
		t.Errorf("wrote %d of 4: %v", len(written), written)
	}
}

func TestExtractRejectsAnEmptyBundle(t *testing.T) {
	archive := buildBundle(t, nil)
	if _, err := Extract(bytes.NewReader(archive), t.TempDir()); err == nil {
		t.Error("an empty bundle was accepted; installing it would replace nothing " +
			"and report success")
	}
}
