package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/zoefix/openfrp/internal/cloudflare"
	"github.com/zoefix/openfrp/internal/config"
)

func init() {
	register(&Command{
		Name:    "cftunnel",
		Summary: "publish through a Cloudflare tunnel",
		Run:     runCFTunnel,
	})
}

// DefaultCloudflaredDir is where the login credential, the per-tunnel
// credentials and the generated configuration live.
const DefaultCloudflaredDir = "/etc/openfrp/cloudflared"

// DefaultCloudflaredBinary is where the downloaded binary is installed.
const DefaultCloudflaredBinary = "/usr/lib/openfrp/cloudflared"

func runCFTunnel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cftunnel", flag.ExitOnError)

	var (
		dir    = fs.String("dir", DefaultCloudflaredDir, "where cloudflared keeps its credentials")
		binary = fs.String("binary", DefaultCloudflaredBinary, "the cloudflared executable")
		name   = fs.String("name", "", "tunnel name on the Cloudflare account")
		server = fs.String("server", "", "the server section these tunnels name")
		cfg    = fs.String("config", "/var/etc/openfrp.json", "rendered client configuration")
	)
	// The action comes before its flags, as the other subcommands spell it.
	// Parsing the whole slice would stop at "setup" and read no flags at all,
	// which is how a -name that was clearly given arrived empty.
	if len(args) == 0 {
		return fmt.Errorf("cftunnel: expected setup, apply or status")
	}
	action := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cli := cloudflare.CLI{Binary: *binary, Dir: *dir}

	switch action {
	case "setup":
		return cfSetup(ctx, cli, *name)
	case "apply":
		return cfApply(ctx, cli, *cfg, *server)
	case "status":
		return cfStatus(ctx, cli)
	default:
		return fmt.Errorf("cftunnel: unknown action %q; use setup, apply or status", action)
	}
}

// progress writes a step to stderr, where the job worker's log picks it up.
func progress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "==> "+format+"\n", args...)
}

// cfSetup installs cloudflared, authorises it, and creates the tunnel.
//
// Everything here is idempotent: an install that is already there, an
// authorisation already held, a tunnel already made. Setting up twice is what
// happens when the first attempt failed halfway, which is exactly when
// starting from scratch would be worst.
func cfSetup(ctx context.Context, cli cloudflare.CLI, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("cftunnel: setup needs -name")
	}

	if !cloudflare.Supported(runtime.GOARCH) {
		return fmt.Errorf("cftunnel: Cloudflare publishes no cloudflared build for "+
			"%s, so this router cannot run a tunnel of theirs", runtime.GOARCH)
	}

	if err := cloudflare.Install(ctx, cli.Binary, func(line string) {
		progress("%s", line)
	}); err != nil {
		return err
	}

	if cli.LoggedIn() {
		progress("already authorised with Cloudflare")
	} else {
		progress("waiting for authorisation — open the link below and pick the " +
			"domain this router should publish")
		err := cli.Login(ctx,
			func(url string) { progress("open: %s", url) },
			func(line string) { progress("%s", line) })
		if err != nil {
			return err
		}
		progress("authorised")
	}

	tunnel, found, err := cli.Find(ctx, name)
	if err != nil {
		return err
	}
	if found {
		progress("tunnel %s already exists (%s)", name, tunnel.ID)
	} else {
		var credentials string
		tunnel, credentials, err = cli.Create(ctx, name)
		if err != nil {
			return err
		}
		progress("created tunnel %s (%s), credentials at %s",
			tunnel.Name, tunnel.ID, credentials)
	}

	// The result line is what the job worker reads to record the tunnel id
	// against the server section. Everything above it is for a person.
	return emitJSON(map[string]any{
		"result": map[string]string{
			"tunnel_id":   tunnel.ID,
			"tunnel_name": tunnel.Name,
			"binary":      cli.Binary,
			"dir":         cli.Dir,
		},
	})
}

// cfApply renders the configuration and routes the hostnames.
//
// Run whenever the tunnel list changes, not only at setup: a domain added to a
// tunnel afterwards needs its own DNS record, and cloudflared needs to be told
// where the new hostname goes.
func cfApply(ctx context.Context, cli cloudflare.CLI, configPath, server string) error {
	if !cli.LoggedIn() {
		return fmt.Errorf("cftunnel: this router is not authorised with Cloudflare yet")
	}

	client, err := config.LoadClient(configPath)
	if err != nil {
		return fmt.Errorf("cftunnel: %s: %w", configPath, err)
	}

	upstream, ok := client.Upstream(server)
	if !ok {
		return fmt.Errorf("cftunnel: no server named %q", server)
	}
	if upstream.TunnelID == "" {
		return fmt.Errorf("cftunnel: server %q has no Cloudflare tunnel yet", server)
	}

	tunnels := client.TunnelsFor(upstream.Name)
	rules, skipped := cloudflare.RulesFor(tunnels)

	for _, name := range skipped {
		// Said out loud rather than dropped: a tunnel that quietly does not
		// exist is indistinguishable from one that is broken.
		progress("skipped %s — a Cloudflare tunnel publishes hostnames over "+
			"HTTP, nothing else", name)
	}

	path, err := cli.WriteConfig(upstream.TunnelID,
		cli.CredentialsPath(upstream.TunnelID), rules)
	if err != nil {
		return err
	}
	progress("wrote %s with %d hostname(s)", path, len(rules))

	// Routing is per hostname and outlives a config rewrite, so a name already
	// pointed at this tunnel costs one call that changes nothing.
	var failures []string
	for _, rule := range rules {
		if err := cli.Route(ctx, upstream.TunnelID, rule.Hostname); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		progress("%s routes to this tunnel", rule.Hostname)
	}

	if len(failures) > 0 {
		return fmt.Errorf("cftunnel: %s", strings.Join(failures, "; "))
	}
	return emitJSON(map[string]any{
		"result": map[string]any{"config": path, "hostnames": len(rules)},
	})
}

// cfStatus reports what is installed and authorised.
func cfStatus(ctx context.Context, cli cloudflare.CLI) error {
	state := map[string]any{
		"binary":     cli.Binary,
		"dir":        cli.Dir,
		"installed":  false,
		"authorised": cli.LoggedIn(),
	}

	if info, err := os.Stat(cli.Binary); err == nil && info.Size() > 0 {
		state["installed"] = true
	}
	if cli.LoggedIn() {
		if tunnels, err := cli.List(ctx); err == nil {
			state["tunnels"] = tunnels
		}
	}
	return emitJSON(map[string]any{"result": state})
}

// emitJSON writes one machine-readable line to stdout.
func emitJSON(value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
