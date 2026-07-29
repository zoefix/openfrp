package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const routedFile = "routed.json"

func (c CLI) Withdraw(ctx context.Context, server string, current []Rule,
	progress func(string)) []string {

	if progress == nil {
		progress = func(string) {}
	}

	previous, err := c.readRouted()
	if err != nil {
		progress(fmt.Sprintf("could not read what was published before: %v", err))
		return nil
	}

	live := map[string]bool{}
	for _, rule := range current {
		live[rule.Hostname] = true
	}

	var stale []string
	for _, hostname := range previous[server] {
		if !live[hostname] {
			stale = append(stale, hostname)
		}
	}

	var withdrawn []string
	if len(stale) > 0 {
		credential, err := c.ReadCredential()
		if err != nil {
			progress(fmt.Sprintf("cannot withdraw %v: %v", stale, err))
		} else {
			for _, hostname := range stale {
				removed, err := credential.DeleteRecord(ctx, hostname)
				switch {
				case err != nil:
					progress(fmt.Sprintf("could not withdraw %s: %v", hostname, err))
				case removed:
					progress(hostname + " no longer resolves here")
					withdrawn = append(withdrawn, hostname)
				default:

					withdrawn = append(withdrawn, hostname)
				}
			}
		}
	}

	names := make([]string, 0, len(current))
	for _, rule := range current {
		names = append(names, rule.Hostname)
	}
	sort.Strings(names)

	previous[server] = names
	if err := c.writeRouted(previous); err != nil {
		progress(fmt.Sprintf("could not record what is published: %v", err))
	}

	return withdrawn
}

func (c CLI) routedPath() string { return filepath.Join(c.Dir, routedFile) }

func (c CLI) readRouted() (map[string][]string, error) {
	raw, err := os.ReadFile(c.routedPath())
	if os.IsNotExist(err) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, err
	}

	state := map[string][]string{}
	if err := json.Unmarshal(raw, &state); err != nil {

		return map[string][]string{}, nil
	}
	return state, nil
}

func (c CLI) writeRouted(state map[string][]string) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.routedPath(), encoded, 0o600)
}
