// Package version reports the build identity of both daemons.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version is the release number, and this line is its single source of
// truth: the Makefile and the OpenWrt package read it from here rather than
// deriving something from git. It is a plain x.y.z so that every surface
// that shows it — the LuCI overview, the per-server version column, logs on
// both ends — shows a version a person can compare, not a commit hash.
//
// Being a source default rather than only a link-time stamp is what makes an
// unstamped `go build` report "0.3.0" instead of "dev" — which is exactly
// what the servers deployed from early unstamped builds have been answering
// at login ever since.
//
// Bump it in the release commit. Commit and Date remain link-time stamps:
//
//	-ldflags "-X github.com/zoefix/openfrp/internal/version.Commit=abc1234"
var (
	Version = "0.3.0"
	Commit  = ""
	Date    = ""
)

// String returns a one-line build identity suitable for logs and --version.
func String() string {
	commit := Commit
	if commit == "" {
		commit = vcsRevision()
	}

	s := Version
	if commit != "" {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		s += "+" + commit
	}
	if Date != "" {
		s += " (" + Date + ")"
	}
	return fmt.Sprintf("%s %s/%s %s", s, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// Short returns just the version string.
func Short() string { return Version }

// vcsRevision recovers the commit from the build info Go embeds automatically,
// so a plain `go build` still reports something useful.
func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}
