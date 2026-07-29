package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBinaryMatchesArch(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiling two binaries is slow")
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	build := func(goarch string) string {
		out := filepath.Join(dir, "openfrps_"+goarch)
		cmd := exec.Command("go", "build", "-o", out, source)
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", goarch, err, output)
		}
		return out
	}

	amd64Binary := build("amd64")
	arm64Binary := build("arm64")

	cases := []struct {
		name   string
		binary string
		arch   string
		want   bool
	}{
		{"amd64 binary on amd64 server", amd64Binary, "amd64", true},
		{"arm64 binary on arm64 server", arm64Binary, "arm64", true},
		{"amd64 binary on arm64 server", amd64Binary, "arm64", false},
		{"arm64 binary on amd64 server", arm64Binary, "amd64", false},

		{"unknown architecture", amd64Binary, "sparc64", true},
		{"not an ELF file", source, "amd64", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := binaryMatchesArch(tc.binary, tc.arch)
			if got != tc.want {
				t.Errorf("binaryMatchesArch(%s, %s) = %v (%v), want %v",
					filepath.Base(tc.binary), tc.arch, got, err, tc.want)
			}
			if !got && err == nil {
				t.Error("a refusal must explain itself")
			}
		})
	}
}
