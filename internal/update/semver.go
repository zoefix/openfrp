package update

import (
	"fmt"
	"strconv"
	"strings"
)

type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")

	if i := strings.IndexAny(s, "+ "); i >= 0 {
		s = s[:i]
	}

	var v Version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Pre = s[i+1:]
		s = s[:i]
	}

	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, fmt.Errorf("update: %q is not a version", s)
	}

	fields := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("update: %q is not a version", s)
		}
		*fields[i] = n
	}
	return v, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders two versions, numerically field by field so that 0.10.0 sorts
// above 0.9.0 rather than below it as a string compare would have it.
//
// A prerelease sorts below the release it leads to, so v1.0.0-rc1 does not
// offer itself as an upgrade from v1.0.0.
func (v Version) Compare(other Version) int {
	for _, pair := range [][2]int{
		{v.Major, other.Major},
		{v.Minor, other.Minor},
		{v.Patch, other.Patch},
	} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}

	switch {
	case v.Pre == other.Pre:
		return 0
	case v.Pre == "":
		return 1
	case other.Pre == "":
		return -1
	case v.Pre < other.Pre:
		return -1
	default:
		return 1
	}
}

func (v Version) NewerThan(other Version) bool { return v.Compare(other) > 0 }
