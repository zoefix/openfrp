package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// The app's translations live here, relative to this package.
const appDir = "../../openwrt/luci/luci-app-openfrp"

// languages are the catalogues that must stay complete. English is the source
// language and needs none: LuCI falls back to the msgid.
var languages = []string{"zh_Hans", "zh_Hant", "ja"}

// TestHashMatchesLuCI pins SuperFastHash to values taken from a catalogue LuCI
// itself compiled.
//
// These are not self-generated: each is the key under which a stock
// base.zh-cn.lmo from a live OpenWrt router stores that string. If this test
// fails, every catalogue this tool produces silently translates nothing, which
// is a failure with no error message anywhere at runtime.
func TestHashMatchesLuCI(t *testing.T) {
	golden := map[string]uint32{
		"Save":      0x219966c7,
		"Hostname":  0xd5a3a8d6,
		"Interface": 0xd3e54c60,
		"Password":  0x8f9289a4,
		"Reboot":    0xc755ff17,
		"Firewall":  0x34f9da4c,
	}

	for msgid, want := range golden {
		if got := sfhHash([]byte(msgid)); got != want {
			t.Errorf("sfhHash(%q) = %#08x, want %#08x", msgid, got, want)
		}
	}
}

// TestCataloguesAreComplete fails when a string reachable from the UI has no
// translation in some language.
//
// The source of truth is the JavaScript, not the .pot: a stale template would
// otherwise let a newly added label pass unnoticed and render as English in an
// otherwise translated page.
func TestCataloguesAreComplete(t *testing.T) {
	wanted := extractedMsgids(t)

	for _, lang := range languages {
		path := filepath.Join(appDir, "po", lang, "openfrp.po")
		messages, err := parsePO(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		have := make(map[string]string, len(messages))
		for _, msg := range messages {
			have[msg.id] = msg.str
		}

		var missing []string
		for _, msgid := range wanted {
			if have[msgid] == "" {
				missing = append(missing, msgid)
			}
		}
		sort.Strings(missing)

		for _, msgid := range missing {
			t.Errorf("%s: no translation for %q", lang, msgid)
		}
	}
}

// formatSpecifier matches the placeholders LuCI's .format() substitutes, plus
// the {arch}/{os} template slots the deploy view uses.
var formatSpecifier = regexp.MustCompile(`%[sdq]|\{arch\}|\{os\}`)

// TestFormatSpecifiersSurviveTranslation checks that a translation carries the
// same placeholders as its source string.
//
// Dropping a %s from a translated string does not fail loudly; it renders a
// label with a value missing, or shifts every later argument by one. Adding
// one that the caller never supplies is worse.
func TestFormatSpecifiersSurviveTranslation(t *testing.T) {
	for _, lang := range languages {
		path := filepath.Join(appDir, "po", lang, "openfrp.po")
		messages, err := parsePO(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		for _, msg := range messages {
			want := formatSpecifier.FindAllString(msg.id, -1)
			got := formatSpecifier.FindAllString(msg.str, -1)

			// Order matters: .format() fills positionally, so swapping two
			// specifiers silently swaps two values in the rendered string.
			if len(want) != len(got) {
				t.Errorf("%s: %q has %v but its translation %q has %v",
					lang, msg.id, want, msg.str, got)
				continue
			}
			for i := range want {
				if want[i] != got[i] {
					t.Errorf("%s: %q specifier %d is %s, translation has %s",
						lang, msg.id, i, want[i], got[i])
				}
			}
		}
	}
}

// TestCataloguesCompile builds each catalogue and reads its strings back,
// which is the only way to catch a hash collision between two real msgids.
func TestCataloguesCompile(t *testing.T) {
	for _, lang := range languages {
		source := filepath.Join(appDir, "po", lang, "openfrp.po")
		output := filepath.Join(t.TempDir(), lang+".lmo")

		if err := compile(source, output); err != nil {
			t.Fatalf("%s: %v", lang, err)
		}

		raw, index, err := readCatalogue(output)
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}

		messages, err := parsePO(source)
		if err != nil {
			t.Fatal(err)
		}

		for _, msg := range messages {
			got, ok := lookupOne(raw, index, msg.id)
			if !ok {
				t.Errorf("%s: %q is missing from the compiled catalogue", lang, msg.id)
				continue
			}
			if got != msg.str {
				t.Errorf("%s: %q compiled to %q, want %q", lang, msg.id, got, msg.str)
			}
		}
	}
}

// extractedMsgids scans the app's JavaScript the way the extract command does.
func extractedMsgids(t *testing.T) []string {
	t.Helper()

	root := filepath.Join(appDir, "htdocs")
	seen := map[string]bool{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, hit := range scanJS(string(source)) {
			seen[hit.text] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	if len(seen) == 0 {
		t.Fatalf("no translatable strings found under %s", root)
	}

	out := make([]string, 0, len(seen))
	for msgid := range seen {
		out = append(out, msgid)
	}
	sort.Strings(out)
	return out
}
