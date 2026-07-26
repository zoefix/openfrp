package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Credentials describe how to reach and authenticate to the server.
//
// The password is deliberately not persisted anywhere by this package. It
// arrives on stdin, is used, and is dropped. UCI never sees it, and it never
// appears in a command line — /proc/*/cmdline is readable by every local
// process on the router.
type Credentials struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	User string `json:"user,omitempty"`

	Password string `json:"password,omitempty"`
	// KeyPath points at a private key on this machine.
	KeyPath string `json:"key_path,omitempty"`
	// KeyPassphrase unlocks an encrypted private key.
	KeyPassphrase string `json:"key_passphrase,omitempty"`

	// KnownFingerprint is the SHA256 fingerprint recorded on a previous
	// connection. Empty means trust-on-first-use.
	KnownFingerprint string `json:"host_fingerprint,omitempty"`
}

// Session is an authenticated SSH connection to the server.
type Session struct {
	client *ssh.Client
	sftp   *sftp.Client

	// Fingerprint is the host key actually presented, for recording after a
	// first connection.
	Fingerprint string
}

// ErrHostKeyChanged means the server presented a different key than recorded.
var ErrHostKeyChanged = errors.New("deploy: host key has changed")

// Connect dials and authenticates.
func Connect(ctx context.Context, creds Credentials) (*Session, error) {
	if creds.Host == "" {
		return nil, errors.New("deploy: no host given")
	}
	port := creds.Port
	if port == 0 {
		port = 22
	}
	user := creds.User
	if user == "" {
		user = "root"
	}

	auth, err := authMethods(creds)
	if err != nil {
		return nil, err
	}

	var presented string
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		Timeout:         20 * time.Second,
		HostKeyCallback: recordingHostKey(creds.KnownFingerprint, &presented),
	}

	addr := net.JoinHostPort(creds.Host, strconv.Itoa(port))

	// ssh.Dial is not context aware, so dial the socket ourselves and let the
	// context cancel it.
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("deploy: dial %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("deploy: authenticate to %s: %w", addr, err)
	}

	return &Session{
		client:      ssh.NewClient(sshConn, chans, reqs),
		Fingerprint: presented,
	}, nil
}

func authMethods(creds Credentials) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if creds.KeyPath != "" {
		pem, err := os.ReadFile(creds.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("deploy: read key %s: %w", creds.KeyPath, err)
		}

		var signer ssh.Signer
		if creds.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(creds.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return nil, fmt.Errorf("deploy: parse key %s: %w", creds.KeyPath, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if creds.Password != "" {
		methods = append(methods, ssh.Password(creds.Password))
		// Some sshd builds advertise "password" but drive it through the
		// keyboard-interactive machinery. Answering both costs nothing and
		// avoids an authentication failure that looks like a wrong password.
		methods = append(methods, ssh.KeyboardInteractive(
			func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = creds.Password
				}
				return answers, nil
			}))
	}

	if len(methods) == 0 {
		return nil, errors.New("deploy: no password and no key supplied")
	}
	return methods, nil
}

// recordingHostKey verifies against a known fingerprint and records what was
// actually presented.
//
// With no known fingerprint this is trust-on-first-use: the key is accepted and
// returned so the caller can store it. Every connection after that is checked,
// and a change is refused rather than warned about.
func recordingHostKey(known string, out *string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint := ssh.FingerprintSHA256(key)
		*out = fingerprint

		if known == "" {
			return nil
		}
		if known != fingerprint {
			return fmt.Errorf("%w: expected %s, server presented %s",
				ErrHostKeyChanged, known, fingerprint)
		}
		return nil
	}
}

// debugSSH enables per-command tracing. Set OPENFRP_DEBUG_SSH=1 to see every
// command, its output and its error — which is the only practical way to tell
// a failing probe from an empty result.
var debugSSH = os.Getenv("OPENFRP_DEBUG_SSH") != ""

// Run executes a command and returns its combined output.
//
// Commands are run through `sh -c`, so the caller is responsible for quoting.
// Use ShellQuote for anything derived from user input.
func (s *Session) Run(ctx context.Context, command string) (out string, err error) {
	if debugSSH {
		defer func() {
			fmt.Fprintf(os.Stderr, "[ssh] %s\n     -> %q err=%v\n",
				truncate(command, 100), truncate(out, 120), err)
		}()
	}
	return s.run(ctx, command)
}

func (s *Session) run(ctx context.Context, command string) (string, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("deploy: open session: %w", err)
	}
	defer session.Close()

	// Stdout and stderr MUST NOT share a buffer. The ssh package copies them
	// in separate goroutines, so pointing both at one bytes.Buffer is an
	// unsynchronised concurrent write — which silently loses output rather
	// than crashing, and does so only for commands that happen to write to
	// both streams. That produced a detection failure that looked like the
	// remote shell mis-parsing perfectly ordinary commands.
	var stdout, stderr syncBuffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGTERM)
		return stdout.String(), ctx.Err()
	case err := <-done:
		if err != nil {
			return stdout.String(), fmt.Errorf("deploy: %q: %w: %s",
				truncate(command, 60), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), nil
	}
}

// syncBuffer is a bytes.Buffer safe for the ssh package's concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Output runs a command and returns its trimmed output, discarding the error.
// For probes where "did not work" and "empty" mean the same thing.
func (s *Session) Output(ctx context.Context, command string) string {
	out, _ := s.Run(ctx, command)
	return strings.TrimSpace(out)
}

// Exists reports whether a path is present on the server.
func (s *Session) Exists(ctx context.Context, path string) bool {
	out := s.Output(ctx, "test -e "+ShellQuote(path)+" && echo yes || echo no")
	return out == "yes"
}

// HasCommand reports whether a command is on the server's PATH.
func (s *Session) HasCommand(ctx context.Context, name string) bool {
	out := s.Output(ctx, "command -v "+ShellQuote(name)+" >/dev/null 2>&1 && echo yes || echo no")
	return out == "yes"
}

// WriteFile uploads content over SFTP with the given mode.
func (s *Session) WriteFile(ctx context.Context, path string, content []byte, mode os.FileMode) error {
	client, err := s.sftpClient()
	if err != nil {
		return err
	}

	if dir := parentDir(path); dir != "" {
		client.MkdirAll(dir)
	}

	// Write to a temporary path and rename, so a reader never sees a partial
	// file and a failed upload cannot leave a broken binary in place.
	tmp := path + ".openfrp-tmp"

	f, err := client.Create(tmp)
	if err != nil {
		return fmt.Errorf("deploy: create %s: %w", tmp, err)
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		client.Remove(tmp)
		return fmt.Errorf("deploy: write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		client.Remove(tmp)
		return fmt.Errorf("deploy: close %s: %w", tmp, err)
	}
	if err := client.Chmod(tmp, mode); err != nil {
		client.Remove(tmp)
		return fmt.Errorf("deploy: chmod %s: %w", tmp, err)
	}

	// SFTP rename fails if the target exists on some servers; remove first.
	client.Remove(path)
	if err := client.Rename(tmp, path); err != nil {
		client.Remove(tmp)
		return fmt.Errorf("deploy: install %s: %w", path, err)
	}
	return nil
}

func (s *Session) sftpClient() (*sftp.Client, error) {
	if s.sftp != nil {
		return s.sftp, nil
	}
	client, err := sftp.NewClient(s.client)
	if err != nil {
		return nil, fmt.Errorf("deploy: start sftp: %w", err)
	}
	s.sftp = client
	return client, nil
}

// Close releases the connection.
func (s *Session) Close() error {
	if s.sftp != nil {
		s.sftp.Close()
	}
	return s.client.Close()
}

// ShellQuote wraps a value for safe use inside a POSIX shell command.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// EncodeForShell base64-encodes content so it can be transferred through a
// command line without any quoting concerns at all. Used where SFTP is
// unavailable.
func EncodeForShell(content []byte) string {
	return base64.StdEncoding.EncodeToString(content)
}

func parentDir(path string) string {
	if idx := strings.LastIndexByte(path, '/'); idx > 0 {
		return path[:idx]
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
