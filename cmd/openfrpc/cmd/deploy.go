package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/zoefix/openfrp/internal/deploy"
)

func init() {
	register(&Command{
		Name:    "deploy",
		Summary: "install and configure the server over SSH",
		Run:     runDeploy,
	})
}

// stdinArgs is the JSON the LuCI job worker pipes in.
//
// Credentials arrive this way rather than as flags because /proc/*/cmdline is
// readable by every local process on the router. A password passed as an
// argument is a password published to anyone with a shell.
type stdinArgs struct {
	deploy.Credentials
	Token          string `json:"token,omitempty"`
	BindPort       int    `json:"bind_port,omitempty"`
	VhostHTTPPort  int    `json:"vhost_http_port,omitempty"`
	VhostHTTPSPort int    `json:"vhost_https_port,omitempty"`
	LocalBinary    string `json:"local_binary,omitempty"`
	ReleaseURL     string `json:"release_url,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	EnableBBR      *bool  `json:"enable_bbr,omitempty"`
}

func runDeploy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)

	var (
		jsonOut   = fs.Bool("json", false, "emit line-delimited JSON progress instead of text")
		dryRun    = fs.Bool("dry-run", false, "report what would change without changing it")
		host      = fs.String("host", "", "server address")
		port      = fs.Int("port", 22, "SSH port")
		user      = fs.String("user", "root", "SSH user")
		keyPath   = fs.String("key", "", "private key on this machine")
		binary    = fs.String("binary", "", "server binary to upload")
		release   = fs.String("release-url", "", "download URL, with {os} and {arch} substituted")
		bindPort  = fs.Int("bind-port", 7000, "control port the server will listen on")
		httpPort  = fs.Int("vhost-http-port", 80, "shared HTTP port, 0 to disable")
		httpsPort = fs.Int("vhost-https-port", 443, "shared HTTPS port, 0 to disable")
		token     = fs.String("token", "", "shared secret; generated when empty")
		enableBBR = fs.Bool("bbr", true, "load tcp_bbr and switch congestion control to BBR")
		readStdin = fs.Bool("stdin", false, "read all settings as JSON on stdin")
	)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"Usage: openfrpc deploy [flags]\n\n"+
				"Installs the OpenFrp server on a remote host over SSH: detects the\n"+
				"distribution and init system, installs the binary and configuration,\n"+
				"creates a service, opens the firewall and verifies the result.\n\n"+
				"Re-running upgrades in place; every step is idempotent.\n\n"+
				"A password is never accepted as a flag, because /proc/*/cmdline is\n"+
				"readable by any local process. Pass credentials as JSON on stdin\n"+
				"with -stdin, or use key authentication.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	opts := deploy.Options{
		Credentials: deploy.Credentials{
			Host:    *host,
			Port:    *port,
			User:    *user,
			KeyPath: *keyPath,
		},
		Token:          *token,
		BindPort:       *bindPort,
		VhostHTTPPort:  *httpPort,
		VhostHTTPSPort: *httpsPort,
		LocalBinary:    *binary,
		ReleaseURL:     *release,
		EnableBBR:      *enableBBR,
		DryRun:         *dryRun,
	}

	// The LuCI worker always uses this path. It is also how a password reaches
	// us from an interactive shell without ever touching argv.
	if *readStdin {
		payload, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read settings from stdin: %w", err)
		}
		if err := applyStdin(&opts, payload); err != nil {
			return err
		}
	}

	if opts.Credentials.Host == "" {
		fs.Usage()
		return errors.New("no server address given")
	}

	var reporter deploy.Reporter
	if *jsonOut {
		reporter = deploy.NewJSONReporter(os.Stdout)
	} else {
		reporter = deploy.NewTextReporter(os.Stdout)
	}

	result, err := deploy.New(opts, reporter).Run(ctx)
	if err != nil {
		return err
	}

	if *jsonOut {
		// The final line is the machine-readable result: the caller needs the
		// generated token and the host fingerprint to store in UCI.
		summary, _ := json.Marshal(map[string]any{"result": result})
		fmt.Println(string(summary))
	} else if !opts.DryRun {
		fmt.Printf("\nToken:       %s\n", result.Token)
		fmt.Printf("Fingerprint: %s\n", result.Fingerprint)
		fmt.Printf("\nPut the token in /etc/config/openfrp and enable the service.\n")
	}

	return nil
}

// applyStdin merges JSON settings over the flag-derived options.
func applyStdin(opts *deploy.Options, payload []byte) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}

	var in stdinArgs
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return fmt.Errorf("parse settings from stdin: %w", err)
	}

	if in.Host != "" {
		opts.Credentials.Host = in.Host
	}
	if in.Port != 0 {
		opts.Credentials.Port = in.Port
	}
	if in.User != "" {
		opts.Credentials.User = in.User
	}
	if in.Password != "" {
		opts.Credentials.Password = in.Password
	}
	if in.KeyPath != "" {
		opts.Credentials.KeyPath = in.KeyPath
	}
	if in.KeyPassphrase != "" {
		opts.Credentials.KeyPassphrase = in.KeyPassphrase
	}
	if in.KnownFingerprint != "" {
		opts.Credentials.KnownFingerprint = in.KnownFingerprint
	}

	if in.Token != "" {
		opts.Token = in.Token
	}
	if in.BindPort != 0 {
		opts.BindPort = in.BindPort
	}
	if in.VhostHTTPPort != 0 {
		opts.VhostHTTPPort = in.VhostHTTPPort
	}
	if in.VhostHTTPSPort != 0 {
		opts.VhostHTTPSPort = in.VhostHTTPSPort
	}
	if in.LocalBinary != "" {
		opts.LocalBinary = in.LocalBinary
	}
	if in.ReleaseURL != "" {
		opts.ReleaseURL = in.ReleaseURL
	}
	if in.SHA256 != "" {
		opts.SHA256 = in.SHA256
	}
	if in.EnableBBR != nil {
		opts.EnableBBR = *in.EnableBBR
	}

	return nil
}
