package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// message is one catalogue entry.
type message struct {
	id  string
	str string

	// references are the source locations the string was extracted from,
	// carried through to the .pot so a translator can see the context.
	references []string
}

// parsePO reads a gettext catalogue.
//
// Only what LuCI actually uses is supported: msgid, msgstr and their
// continuation lines. Plural forms are skipped rather than mistranslated —
// this app has none, and a half-understood plural is worse than an untouched
// English string.
func parsePO(path string) ([]message, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var (
		messages []message
		current  message
		field    string
		plural   bool
	)

	flush := func() {
		// The header entry has an empty msgid; it carries metadata, not a
		// translation, so it never belongs in the compiled catalogue. An
		// untranslated entry is dropped too, so the UI falls back to English
		// rather than rendering an empty label.
		if current.id != "" && current.str != "" && !plural {
			messages = append(messages, current)
		}
		current = message{}
		plural = false
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		switch {
		case line == "":
			flush()
			field = ""

		case strings.HasPrefix(line, "#"):
			// Comments, including the "#:" source references.

		case strings.HasPrefix(line, "msgid_plural"), strings.HasPrefix(line, "msgstr["):
			plural = true
			field = ""

		case strings.HasPrefix(line, "msgid"):
			flush()
			field = "id"
			value, err := unquotePO(strings.TrimSpace(line[len("msgid"):]))
			if err != nil {
				return nil, err
			}
			current.id = value

		case strings.HasPrefix(line, "msgstr"):
			field = "str"
			value, err := unquotePO(strings.TrimSpace(line[len("msgstr"):]))
			if err != nil {
				return nil, err
			}
			current.str = value

		case strings.HasPrefix(line, `"`):
			// A continuation of whichever field is open.
			value, err := unquotePO(line)
			if err != nil {
				return nil, err
			}
			switch field {
			case "id":
				current.id += value
			case "str":
				current.str += value
			}
		}
	}
	flush()

	return messages, scanner.Err()
}

// unquotePO unwraps a quoted .po string, honouring the escapes gettext uses.
func unquotePO(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !strings.HasPrefix(s, `"`) || !strings.HasSuffix(s, `"`) {
		return "", fmt.Errorf("malformed po string: %s", s)
	}

	// strconv.Unquote handles the escape set gettext shares with C, and
	// rejects anything malformed rather than silently mangling it.
	unquoted, err := strconv.Unquote(s)
	if err != nil {
		// Fall back for escapes strconv rejects but gettext permits.
		body := s[1 : len(s)-1]
		replacer := strings.NewReplacer(
			`\n`, "\n", `\t`, "\t", `\r`, "\r",
			`\"`, `"`, `\\`, `\`,
		)
		return replacer.Replace(body), nil
	}
	return unquoted, nil
}

// quotePO renders a string as a .po literal, wrapping long ones the way
// gettext does so the file stays readable in a diff.
func quotePO(s string) string {
	escape := func(in string) string {
		var b strings.Builder
		for _, r := range in {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\n':
				b.WriteString(`\n`)
			case '\t':
				b.WriteString(`\t`)
			default:
				b.WriteRune(r)
			}
		}
		return b.String()
	}

	if len(s) <= 72 {
		return `"` + escape(s) + `"`
	}

	// Break on word boundaries into ~72 column chunks.
	var (
		lines []string
		line  strings.Builder
	)
	for _, word := range strings.SplitAfter(s, " ") {
		if line.Len() > 0 && line.Len()+len(word) > 72 {
			lines = append(lines, line.String())
			line.Reset()
		}
		line.WriteString(word)
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}

	var b strings.Builder
	b.WriteString(`""`)
	for _, l := range lines {
		b.WriteString("\n\"" + escape(l) + "\"")
	}
	return b.String()
}

// writePOT renders extracted messages as a template catalogue.
func writePOT(path string, messages []message) error {
	sort.Slice(messages, func(i, j int) bool { return messages[i].id < messages[j].id })

	var b strings.Builder
	b.WriteString("# Translation template for luci-app-openfrp.\n")
	b.WriteString("#\n")
	b.WriteString("# Regenerate with:  go run ./tools/luci-i18n extract \\\n")
	b.WriteString("#   openwrt/luci/luci-app-openfrp/po/templates/openfrp.pot \\\n")
	b.WriteString("#   openwrt/luci/luci-app-openfrp/htdocs\n")
	b.WriteString("#\n")
	b.WriteString("msgid \"\"\n")
	b.WriteString("msgstr \"\"\n")
	b.WriteString("\"Content-Type: text/plain; charset=UTF-8\\n\"\n")
	b.WriteString("\"Content-Transfer-Encoding: 8bit\\n\"\n")

	for _, msg := range messages {
		b.WriteString("\n")
		for _, ref := range msg.references {
			b.WriteString("#: " + ref + "\n")
		}
		b.WriteString("msgid " + quotePO(msg.id) + "\n")
		b.WriteString("msgstr \"\"\n")
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %d strings\n", path, len(messages))
	return nil
}
