package cmd

import (
	"context"
	"fmt"

	"github.com/zoefix/openfrp/internal/version"
)

func init() {
	register(&Command{
		Name:    "version",
		Summary: "print the version and exit",
		Run: func(context.Context, []string) error {
			fmt.Println("openfrpc", version.String())
			return nil
		},
	})
}
