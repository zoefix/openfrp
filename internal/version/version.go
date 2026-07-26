// Package version reports the build identity of both daemons.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These are set at link time:
//
//	-ldflags "-X github.com/zoefix/openfrp/internal/version.Version=1.2.3"
var (
	Version = "dev"
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
