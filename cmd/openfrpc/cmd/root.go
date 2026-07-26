// Package cmd holds the openfrpc subcommands, one per file.
//
// Dispatch is hand-rolled on top of the standard flag package rather than a
// CLI framework. The command set is small and fixed, and this keeps the binary
// free of a dependency that would earn its keep only on a much larger surface.
package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Command is one openfrpc subcommand.
type Command struct {
	Name    string
	Summary string
	// Run receives the arguments after the subcommand name.
	Run func(ctx context.Context, args []string) error
}

var commands = map[string]*Command{}

// register adds a subcommand. Called from each command's init.
func register(c *Command) {
	if _, exists := commands[c.Name]; exists {
		panic(fmt.Sprintf("cmd: %q registered twice", c.Name))
	}
	commands[c.Name] = c
}

// Execute dispatches argv to a subcommand.
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
	return c.Run(ctx, argv[1:])
}

// Usage writes the command list.
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
