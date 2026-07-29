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

const DefaultCloudflaredDir = "/etc/openfrp/cloudflared"

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

func progress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "==> "+format+"\n", args...)
}

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

	zone := ""
	if credential, err := cli.ReadCredential(); err == nil {
		if name, err := credential.ZoneName(ctx); err == nil {
			zone = name
			progress("publishing under %s", zone)
		} else {
			progress("could not read which domain was authorised: %v", err)
		}
	}

	return emitJSON(map[string]any{
		"result": map[string]string{
			"tunnel_id":   tunnel.ID,
			"tunnel_name": tunnel.Name,
			"zone":        zone,
			"binary":      cli.Binary,
			"dir":         cli.Dir,
		},
	})
}

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

		progress("skipped %s — a Cloudflare tunnel publishes hostnames over "+
			"HTTP, nothing else", name)
	}

	path, err := cli.WriteConfig(upstream.Name, upstream.TunnelID,
		cli.CredentialsPath(upstream.TunnelID), rules)
	if err != nil {
		return err
	}
	progress("wrote %s with %d hostname(s)", path, len(rules))

	var failures []string
	for _, rule := range rules {
		if err := cli.Route(ctx, upstream.TunnelID, rule.Hostname); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		progress("%s routes to this tunnel", rule.Hostname)
	}

	withdrawn := cli.Withdraw(ctx, upstream.Name, rules, func(line string) {
		progress("%s", line)
	})

	if len(failures) > 0 {
		return fmt.Errorf("cftunnel: %s", strings.Join(failures, "; "))
	}
	return emitJSON(map[string]any{
		"result": map[string]any{
			"config": path, "hostnames": len(rules), "withdrawn": withdrawn,
		},
	})
}

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

func emitJSON(value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
