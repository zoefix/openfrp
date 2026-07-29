package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/zoefix/openfrp/internal/update"
)

func init() {
	register(&Command{
		Name:    "update",
		Summary: "check for a new release, or install one",
		Run:     runUpdate,
	})
}

func runUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)

	check := fs.Bool("check", false, "look for a newer release and report what was found")
	apply := fs.Bool("apply", false, "download and install the newest release")
	asJSON := fs.Bool("json", false, "write the check result as JSON")
	cache := fs.String("cache", update.DefaultCachePath, "where a check result is stored")
	repo := fs.String("repo", update.DefaultRepo, "the GitHub repository to look in")
	root := fs.String("root", "/", "install under this directory")

	if err := fs.Parse(args); err != nil {
		return err
	}

	client := update.NewClient()
	client.Repo = *repo

	switch {
	case *apply:
		return update.Apply(ctx, update.Options{
			Root:   *root,
			Client: client,
			Log:    os.Stdout,
		})

	case *check:
		status := update.Check(ctx, client)

		if *cache != "" {
			if err := update.WriteCache(*cache, status); err != nil {
				fmt.Fprintln(os.Stderr, "openfrpc: could not store the check result:", err)
			}
		}

		if *asJSON {
			encoder := json.NewEncoder(os.Stdout)
			return encoder.Encode(status)
		}

		switch {
		case status.Error != "":
			return fmt.Errorf("update: %s", status.Error)
		case status.Available:
			fmt.Printf("%s is available; running %s\n", status.Latest, status.Current)
		case status.Latest != "":
			fmt.Printf("%s is the newest release; running %s\n", status.Latest, status.Current)
		default:
			fmt.Printf("no releases published; running %s\n", status.Current)
		}
		return nil

	default:
		fs.Usage()
		return fmt.Errorf("update: pass -check or -apply")
	}
}
