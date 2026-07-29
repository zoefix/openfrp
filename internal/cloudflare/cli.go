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

type CLI struct {
	Binary string

	Dir string
}

func (c CLI) configDir() string { return filepath.Join(c.Dir, ".cloudflared") }

func (c CLI) CertPath() string { return filepath.Join(c.configDir(), certFile) }

func (c CLI) CredentialsPath(tunnelID string) string {
	return filepath.Join(c.configDir(), tunnelID+".json")
}

const LoginTimeout = 10 * time.Minute

const certFile = "cert.pem"

var loginURL = regexp.MustCompile(`https://dash\.cloudflare\.com/argotunnel\S*`)

func (c CLI) LoggedIn() bool {
	info, err := os.Stat(c.CertPath())
	return err == nil && info.Size() > 0
}

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
			c.CertPath())
	}
	return nil
}

type NamedTunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

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

var createdTunnel = regexp.MustCompile(`Created tunnel \S+ with id (\S+)`)

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

	return NamedTunnel{ID: id, Name: name}, c.CredentialsPath(id), nil
}

func (c CLI) Delete(ctx context.Context, id string) error {
	_, err := c.run(ctx, "tunnel", "delete", "--force", id)
	return err
}

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

func (c CLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Binary, args...)

	cmd.Env = append(os.Environ(),
		"HOME="+c.Dir,
		"TUNNEL_ORIGIN_CERT="+c.CertPath())
	return cmd
}

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

func lastLines(message string, n int) string {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "; ")
	}
	return strings.Join(lines[len(lines)-n:], "; ")
}

func (c CLI) ConfigPath(server string) string {
	return filepath.Join(c.Dir, "config-"+server+".yml")
}

func (c CLI) WriteConfig(server, tunnelID, credentialsPath string, rules []Rule) (string, error) {
	if !validSection(server) {
		return "", fmt.Errorf("cloudflare: %q is not a usable server name", server)
	}

	path := c.ConfigPath(server)
	body := RenderConfig(tunnelID, credentialsPath, rules)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("cloudflare: %s: %w", path, err)
	}
	return path, nil
}

func validSection(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

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

	return os.Rename(target+".new", target)
}
