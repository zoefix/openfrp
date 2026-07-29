package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Command struct {
	Name    string
	Summary string

	Run func(ctx context.Context, args []string) error
}

var commands = map[string]*Command{}

func register(c *Command) {
	if _, exists := commands[c.Name]; exists {
		panic(fmt.Sprintf("cmd: %q registered twice", c.Name))
	}
	commands[c.Name] = c
}

func Execute(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		Usage(os.Stderr)
		return fmt.Errorf("no command given")
	}

	name := argv[0]
	switch name {
	case "-h", "--help", "help":
		Usage(os.Stdout)
		return nil
	}

	c, known := commands[name]
	if !known {
		Usage(os.Stderr)
		return fmt.Errorf("unknown command %q", name)
	}
	err := c.Run(ctx, argv[1:])
	if errors.Is(err, errReported) {

		os.Exit(1)
	}
	return err
}

func Usage(w *os.File) {
	var b strings.Builder
	b.WriteString("openfrpc — OpenFrp client\n\nUsage:\n  openfrpc <command> [flags]\n\nCommands:\n")

	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)

	width := 0
	for _, name := range names {
		width = max(width, len(name))
	}
	for _, name := range names {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, name, commands[name].Summary)
	}
	b.WriteString("\nRun 'openfrpc <command> -h' for command flags.\n")

	fmt.Fprint(w, b.String())
}
