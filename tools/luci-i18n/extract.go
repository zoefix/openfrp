package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func extract(output string, roots []string) error {
	found := map[string]*message{}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".js" {
				return nil
			}

			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			for _, hit := range scanJS(string(source)) {
				ref := fmt.Sprintf("%s:%d", filepath.ToSlash(path), hit.line)
				if existing, ok := found[hit.text]; ok {
					existing.references = append(existing.references, ref)
					continue
				}
				found[hit.text] = &message{id: hit.text, references: []string{ref}}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	messages := make([]message, 0, len(found))
	for _, msg := range found {
		sort.Strings(msg.references)
		messages = append(messages, *msg)
	}

	if len(messages) == 0 {
		return fmt.Errorf("no translatable strings found under %s", strings.Join(roots, ", "))
	}
	return writePOT(output, messages)
}

type occurrence struct {
	text string
	line int
}

func scanJS(source string) []occurrence {
	var out []occurrence

	line := 1
	for i := 0; i < len(source); i++ {
		c := source[i]

		switch {
		case c == '\n':
			line++
			continue

		case c == '/' && i+1 < len(source) && source[i+1] == '/':
			for i < len(source) && source[i] != '\n' {
				i++
			}
			line++
			continue

		case c == '/' && i+1 < len(source) && source[i+1] == '*':
			i += 2
			for i+1 < len(source) && !(source[i] == '*' && source[i+1] == '/') {
				if source[i] == '\n' {
					line++
				}
				i++
			}
			i++
			continue

		case c == '\'' || c == '"' || c == '`':
			_, next, newlines := readJSString(source, i)
			line += newlines
			i = next - 1
			continue

		case c == '_':

			if i > 0 && isIdentChar(source[i-1]) {
				continue
			}
			j := skipSpace(source, i+1)
			if j >= len(source) || source[j] != '(' {
				continue
			}

			if text, ok, _ := readConcatenatedStrings(source, j+1); ok {
				out = append(out, occurrence{text: text, line: line})
			}
			continue
		}
	}

	return out
}

func readConcatenatedStrings(source string, pos int) (string, bool, int) {
	var (
		parts    strings.Builder
		newlines int
		wantStr  = true
	)

	for {
		pos = skipSpace(source, pos)
		if pos >= len(source) {
			return "", false, newlines
		}

		if wantStr {
			c := source[pos]

			if c != '\'' && c != '"' {
				return "", false, newlines
			}
			text, next, n := readJSString(source, pos)
			parts.WriteString(text)
			newlines += n
			pos = next
			wantStr = false
			continue
		}

		switch source[pos] {
		case '+':
			pos++
			wantStr = true
		case ')', ',':

			return parts.String(), parts.Len() > 0, newlines
		default:
			return "", false, newlines
		}
	}
}

func readJSString(source string, pos int) (string, int, int) {
	quote := source[pos]
	pos++

	var (
		b        strings.Builder
		newlines int
	)

	for pos < len(source) {
		c := source[pos]

		if c == '\\' && pos+1 < len(source) {
			switch source[pos+1] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\n':

				newlines++
			default:
				b.WriteByte(source[pos+1])
			}
			pos += 2
			continue
		}

		if c == quote {
			return b.String(), pos + 1, newlines
		}
		if c == '\n' {
			newlines++
		}
		b.WriteByte(c)
		pos++
	}

	return b.String(), pos, newlines
}

func skipSpace(source string, pos int) int {
	for pos < len(source) {
		switch {
		case source[pos] == ' ' || source[pos] == '\t' ||
			source[pos] == '\n' || source[pos] == '\r':
			pos++

		case strings.HasPrefix(source[pos:], "//"):
			for pos < len(source) && source[pos] != '\n' {
				pos++
			}

		case strings.HasPrefix(source[pos:], "/*"):
			end := strings.Index(source[pos+2:], "*/")
			if end < 0 {
				return len(source)
			}
			pos += 2 + end + 2

		default:
			return pos
		}
	}
	return pos
}

func isIdentChar(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
