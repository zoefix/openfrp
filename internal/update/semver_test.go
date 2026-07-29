package update

import "testing"

func TestVersionOrdersNumericallyNotAsText(t *testing.T) {
	cases := []struct {
		lower, higher string
	}{
		{"v0.9.0", "v0.10.0"},
		{"v0.3.0", "v0.3.1"},
		{"v0.3.9", "v0.4.0"},
		{"v1.0.0-rc1", "v1.0.0"},
		{"v1.0.0-rc1", "v1.0.0-rc2"},
		{"v0.3.0", "v1.0.0"},
	}

	for _, tc := range cases {
		lower, err := ParseVersion(tc.lower)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.lower, err)
		}
		higher, err := ParseVersion(tc.higher)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.higher, err)
		}

		if !higher.NewerThan(lower) {
			t.Errorf("%s was not treated as newer than %s; a text compare puts "+
				"0.10 below 0.9 and would hide the upgrade", tc.higher, tc.lower)
		}
		if lower.NewerThan(higher) {
			t.Errorf("%s was treated as newer than %s", tc.lower, tc.higher)
		}
	}
}

func TestSameVersionIsNotAnUpgrade(t *testing.T) {
	for _, s := range []string{"v0.3.0", "0.3.0", "v0.3.0+abc1234", "v0.3.0 (2026-01-01)"} {
		got, err := ParseVersion(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		running, _ := ParseVersion("v0.3.0")
		if got.NewerThan(running) || running.NewerThan(got) {
			t.Errorf("%q parsed to %s, which does not equal the running v0.3.0; "+
				"the build metadata a running binary carries must not read as a "+
				"different release", s, got)
		}
	}
}

func TestShortVersionsFillIn(t *testing.T) {
	v, err := ParseVersion("v1.2")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Major != 1 || v.Minor != 2 || v.Patch != 0 {
		t.Errorf("v1.2 parsed as %s, want v1.2.0", v)
	}
}

func TestRubbishIsRejected(t *testing.T) {
	for _, s := range []string{"", "latest", "v", "1.2.3.4", "v-1.0.0", "vx.y.z"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) succeeded; an unparsable tag must not be "+
				"offered as an update", s)
		}
	}
}
