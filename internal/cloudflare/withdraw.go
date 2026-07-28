package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// routedFile remembers what this router published, per server.
//
// Kept because a name that has been removed from the configuration is, by
// then, no longer in the configuration — there is nothing left to compare
// against, and the DNS record would simply stay. Cloudflare's zone is not a
// safe place to look either: it holds records this router never made, and
// deleting by pattern would eventually take one of them.
const routedFile = "routed.json"

// Withdraw removes the DNS records for hostnames no longer published.
//
// Failures are reported and not returned. A record that could not be removed
// leaves a name answering with an error, which is untidy; refusing the whole
// apply over it would leave every other name unpublished, which is an outage.
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
					// Already gone, or never ours. Either way it is now in the
					// state being asked for.
					withdrawn = append(withdrawn, hostname)
				}
			}
		}
	}

	// Recorded after the withdrawal, so a run that failed halfway tries the
	// same names again rather than forgetting them.
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
		// A file that cannot be read is treated as an empty one rather than a
		// failure: the worst it costs is a stale record left behind, and the
		// alternative is an apply that never runs again.
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
