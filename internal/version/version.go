package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

var (
	Version = "1.1.0"
	Commit  = ""
	Date    = ""
)

func String() string {
	commit := Commit
	if commit == "" {
		commit = vcsRevision()
	}

	s := Short()
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

func Short() string {
	if len(Version) > 0 && Version[0] >= '0' && Version[0] <= '9' {
		return "v" + Version
	}
	return Version
}

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
