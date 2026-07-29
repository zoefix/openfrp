package schema

import (
	"fmt"
	"strings"
)

func Visible(condition string, values map[string]string) (bool, error) {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true, nil
	}

	for _, term := range splitTop(condition, "||") {
		ok, err := evalTerm(term, values)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func evalTerm(term string, values map[string]string) (bool, error) {
	for _, atom := range splitTop(term, "&&") {
		ok, err := evalAtom(atom, values)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func evalAtom(atom string, values map[string]string) (bool, error) {
	atom = strings.TrimSpace(atom)

	for _, op := range []string{"!=", "=="} {
		field, literal, found := strings.Cut(atom, op)
		if !found {
			continue
		}

		name := strings.TrimSpace(field)
		if name == "" {
			return false, fmt.Errorf("condition %q: missing field name", atom)
		}

		want := unquote(strings.TrimSpace(literal))
		got := values[name]

		if op == "==" {
			return got == want, nil
		}
		return got != want, nil
	}

	return false, fmt.Errorf("condition %q: expected == or !=", atom)
}

func splitTop(input, sep string) []string {
	var (
		parts []string
		start int
		quote byte
	)

	for i := 0; i < len(input); i++ {
		c := input[i]

		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}

		if strings.HasPrefix(input[i:], sep) {
			parts = append(parts, input[start:i])
			i += len(sep) - 1
			start = i + 1
		}
	}

	return append(parts, input[start:])
}

func unquote(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			return value[1 : len(value)-1]
		}
	}
	return value
}
