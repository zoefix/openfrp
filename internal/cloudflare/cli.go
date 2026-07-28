// Package cloudflare publishes hostnames through a Cloudflare tunnel.
//
// A Cloudflare tunnel is not one of this project's servers. There is no
// openfrps at the other end, no address to dial and no shared token: the
// cloudflared process here connects outward to Cloudflare's edge, and
// Cloudflare routes a hostname down that connection. It is offered alongside
// the servers because that is where an operator looks for "somewhere to
// publish this", not because it is one.
//
// What that costs is written down where it is enforced rather than here: a
// tunnel published this way is HTTP only, its TLS is terminated by Cloudflare,
// and the visitor's address arrives in a header instead of the PROXY protocol.
package cloudflare

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// CLI drives the cloudflared binary.
//
// Authorisation goes through cloudflared's own login rather than an API token.
// A token has to be created by hand with the right boxes ticked, and the
// wrong set fails as a bare "Authentication error" that reads like a typo —
// the first token tried against this could see no accounts and no zones, and
// said so in a way that named neither problem. Login asks Cloudflare for a
// credential scoped to exactly this, and the operator only has to click.
type CLI struct {
	// Binary is the cloudflared executable.
	Binary string

	// Dir is what cloudflared treats as its home: the login credential and the
	// per-tunnel credentials files live here. Kept off the default, which on
	// OpenWrt is a home directory that may not exist.
	Dir string
}

// LoginTimeout bounds the wait for someone to authorise in a browser.
//
// Long, because the operator has to open a link on another device, sign in and
// choose a zone. Finite, because a job that never ends holds a worker and a
// log file forever, and a login nobody completed is not going to complete.
const LoginTimeout = 10 * time.Minute

// certFile is what a completed login leaves behind.
const certFile = "cert.pem"

// loginURL matches the link cloudflared prints for the operator to open.
var loginURL = regexp.MustCompile(`https://dash\.cloudflare\.com/argotunnel\S*`)

// LoggedIn reports whether an authorisation is already on disk.
func (c CLI) LoggedIn() bool {
	info, err := os.Stat(filepath.Join(c.Dir, certFile))
	return err == nil && info.Size() > 0
}

// Login authorises this router against a Cloudflare account.
//
// The URL is handed to onURL as soon as cloudflared prints it, rather than
// when the command finishes: the command does not finish until someone has
// opened that URL, so waiting for the exit to report it would be waiting for
// the thing it is asking for.
func (c CLI) Login(ctx context.Context, onURL func(string), progress func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, LoginTimeout)
	defer cancel()

	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return fmt.Errorf("cloudflare: %s: %w", c.Dir, err)
	}

	cmd := c.command(ctx, "tunnel", "login")

	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cloudflare: running %s: %w", c.Binary, err)
	}

	var once sync.Once
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		line := scanner.Text()
		if url := loginURL.FindString(line); url != "" && onURL != nil {
			once.Do(func() { onURL(url) })
			continue
		}
		if progress != nil && strings.TrimSpace(line) != "" {
			progress(line)
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("cloudflare: nobody completed the authorisation within %s",
				LoginTimeout)
		}
		return fmt.Errorf("cloudflare: authorisation failed: %w", err)
	}

	if !c.LoggedIn() {
		return fmt.Errorf("cloudflare: the authorisation finished without leaving a credential at %s",
			filepath.Join(c.Dir, certFile))
	}
	return nil
}

// Tunnel names one of the account's tunnels, as cloudflared reports them.
type NamedTunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// List returns the account's tunnels.
func (c CLI) List(ctx context.Context) ([]NamedTunnel, error) {
	out, err := c.run(ctx, "tunnel", "--output", "json", "list")
	if err != nil {
		return nil, err
	}

	var tunnels []NamedTunnel
	if err := json.Unmarshal([]byte(out), &tunnels); err != nil {
		return nil, fmt.Errorf("cloudflare: could not read the tunnel list: %w", err)
	}
	return tunnels, nil
}

// Find returns the tunnel with a name, if the account has one.
func (c CLI) Find(ctx context.Context, name string) (NamedTunnel, bool, error) {
	tunnels, err := c.List(ctx)
	if err != nil {
		return NamedTunnel{}, false, err
	}
	for _, tunnel := range tunnels {
		if tunnel.Name == name {
			return tunnel, true, nil
		}
	}
	return NamedTunnel{}, false, nil
}

// createdTunnel pulls the id out of "Created tunnel <name> with id <uuid>".
var createdTunnel = regexp.MustCompile(`Created tunnel \S+ with id (\S+)`)

// Create makes a tunnel and returns its id and credentials file.
func (c CLI) Create(ctx context.Context, name string) (NamedTunnel, string, error) {
	out, err := c.run(ctx, "tunnel", "create", name)
	if err != nil {
		return NamedTunnel{}, "", err
	}

	match := createdTunnel.FindStringSubmatch(out)
	if len(match) != 2 {
		return NamedTunnel{}, "", fmt.Errorf(
			"cloudflare: the tunnel was created but its id could not be read from: %s",
			strings.TrimSpace(out))
	}

	id := match[1]
	// cloudflared names the credentials file after the tunnel id, in its home.
	// Reading it back from the output would be more faithful, but the message
	// wraps the path in a sentence and the path may contain spaces.
	return NamedTunnel{ID: id, Name: name}, filepath.Join(c.Dir, id+".json"), nil
}

// Delete removes a tunnel, along with any connections it still holds.
func (c CLI) Delete(ctx context.Context, id string) error {
	_, err := c.run(ctx, "tunnel", "delete", "--force", id)
	return err
}

// Route points a hostname at a tunnel.
//
// cloudflared writes a proxied CNAME to <id>.cfargotunnel.com, which is the
// only record that reaches a tunnel. A wildcard is worth calling out when it
// fails: proxied wildcard records are not on every Cloudflare plan, and the
// refusal that comes back does not say that is what happened.
func (c CLI) Route(ctx context.Context, id, hostname string) error {
	_, err := c.run(ctx, "tunnel", "route", "dns", "--overwrite-dns", id, hostname)
	if err == nil {
		return nil
	}

	if strings.HasPrefix(hostname, "*.") {
		return fmt.Errorf("%w — a proxied wildcard record is not available on "+
			"every Cloudflare plan, so %s may have to be listed as individual "+
			"names instead", err, hostname)
	}
	return err
}

// command builds a cloudflared invocation with its home pinned.
func (c CLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Binary, args...)

	// cloudflared finds both the login credential and the credentials files it
	// writes through its home directory. Pinning it here keeps every one of
	// them under this router's own directory rather than root's home, which on
	// OpenWrt may not exist at all.
	cmd.Env = append(os.Environ(),
		"HOME="+c.Dir,
		"TUNNEL_ORIGIN_CERT="+filepath.Join(c.Dir, certFile))
	return cmd
}

// run executes cloudflared and returns its combined output.
func (c CLI) run(ctx context.Context, args ...string) (string, error) {
	cmd := c.command(ctx, args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("cloudflare: %s %s: %s",
			filepath.Base(c.Binary), strings.Join(args, " "), lastLines(message, 4))
	}
	return string(out), nil
}

// lastLines keeps the tail of a message, which is where cloudflared puts the
// reason. The rest is startup chatter that buries it.
func lastLines(message string, n int) string {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "; ")
	}
	return strings.Join(lines[len(lines)-n:], "; ")
}

// WriteConfig renders the cloudflared configuration and returns its path.
func (c CLI) WriteConfig(tunnelID, credentialsPath string, rules []Rule) (string, error) {
	path := filepath.Join(c.Dir, "config.yml")
	body := RenderConfig(tunnelID, credentialsPath, rules)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("cloudflare: %s: %w", path, err)
	}
	return path, nil
}

// copyBinary installs cloudflared from a local file.
func copyBinary(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(target+".new", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(target + ".new")
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(target + ".new")
		return err
	}

	// Renamed into place so a half-written binary is never the one that runs.
	return os.Rename(target+".new", target)
}
