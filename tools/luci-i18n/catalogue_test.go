package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

const appDir = "../../openwrt/luci/luci-app-openfrp"

var languages = []string{"zh_Hans", "zh_Hant", "ja"}

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

var formatSpecifier = regexp.MustCompile(`%[sdq]|\{arch\}|\{os\}`)

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

func TestKeyIsHashedTheWayTheBrowserAsksForIt(t *testing.T) {
	multiline := "Configure the service first.\n\nFor nginx:\n" +
		"    listen PORT proxy_protocol;\n    real_ip_header proxy_protocol;"
	collapsed := "Configure the service first. For nginx: " +
		"listen PORT proxy_protocol; real_ip_header proxy_protocol;"

	if hashKey(multiline) != hashKey(collapsed) {
		t.Errorf("a multi-line message hashes to %08x but is asked for as %08x",
			hashKey(multiline), hashKey(collapsed))
	}
}

func TestWhitespaceCollapseMatchesTrimws(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  padded  ", "padded"},
		{"one  two", "one two"},
		{"tab\tseparated", "tab separated"},
		{"line\nbreak", "line break"},
		{"mixed \t\n run", "mixed run"},
		{"\n\nleading and trailing\n\n", "leading and trailing"},

		{"kept\rhere", "kept\rhere"},
		{"already single spaced", "already single spaced"},
	}

	for _, c := range cases {
		if got := collapseWhitespace(c.in); got != c.want {
			t.Errorf("collapseWhitespace(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCommentsInsideAMessageDoNotLoseIt(t *testing.T) {
	cases := []struct{ name, source string }{
		{"line comment between the halves", `
			o.description = _('first half ' +
				// why the second half reads oddly
				'second half');
		`},
		{"block comment between the halves", `
			o.description = _('first half ' /* aside */ + 'second half');
		`},
		{"comment before the first half", `
			o.description = _(/* aside */ 'first half ' + 'second half');
		`},
	}

	for _, c := range cases {
		found := scanJS(c.source)
		if len(found) != 1 {
			t.Errorf("%s: extracted %d messages, want 1", c.name, len(found))
			continue
		}
		if found[0].text != "first half second half" {
			t.Errorf("%s: extracted %q", c.name, found[0].text)
		}
	}
}
