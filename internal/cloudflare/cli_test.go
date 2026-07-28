package cloudflare

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeCloudflared writes a shell script that answers like cloudflared does,
// so the parsing is tested against the real message shapes without a network
// or an account. The messages are the format strings from the binary itself.
func fakeCloudflared(t *testing.T, body string) CLI {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the stand-in is a shell script")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "cloudflared")

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return CLI{Binary: path, Dir: filepath.Join(dir, "home")}
}

func TestCreateReadsTheTunnelID(t *testing.T) {
	cli := fakeCloudflared(t, `
echo "Tunnel credentials written to /root/.cloudflared/abc-123.json."
echo "Created tunnel home with id abc-123"
`)

	tunnel, credentials, err := cli.Create(context.Background(), "home")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tunnel.ID != "abc-123" {
		t.Errorf("id read as %q", tunnel.ID)
	}
	if filepath.Base(credentials) != "abc-123.json" {
		t.Errorf("credentials at %q", credentials)
	}
}

// A tunnel that was made but whose id could not be read is worse than a
// failure: the tunnel exists on the account and nothing here can reach it.
func TestCreateSaysWhenTheIDCannotBeRead(t *testing.T) {
	cli := fakeCloudflared(t, `echo "something else entirely"`)

	_, _, err := cli.Create(context.Background(), "home")
	if err == nil {
		t.Fatal("a create whose id could not be read reported success")
	}
	if !strings.Contains(err.Error(), "created") {
		t.Errorf("the error does not say the tunnel was made: %v", err)
	}
}

// The URL has to reach the operator while the command is still running: it
// does not exit until somebody has opened it.
func TestLoginReportsTheURLBeforeItFinishes(t *testing.T) {
	cli := fakeCloudflared(t, `
echo "Please open the following URL and log in with your Cloudflare account:"
echo ""
echo "https://dash.cloudflare.com/argotunnel?aud=&callback=https%3A%2F%2Flogin.example%2Fabc"
echo ""
echo "Leave cloudflared running to download the cert automatically."
mkdir -p "$HOME"
echo "certificate" > "$HOME/cert.pem"
`)

	var got string
	err := cli.Login(context.Background(), func(url string) { got = url }, nil)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if !strings.HasPrefix(got, "https://dash.cloudflare.com/argotunnel?") {
		t.Errorf("reported URL %q", got)
	}
	if !cli.LoggedIn() {
		t.Error("the credential was not seen after a successful login")
	}
}

// A login that exits without writing the credential has not authorised
// anything, whatever its exit status said.
func TestLoginRequiresTheCredentialItWasFor(t *testing.T) {
	cli := fakeCloudflared(t, `echo "https://dash.cloudflare.com/argotunnel?x=1"`)

	err := cli.Login(context.Background(), func(string) {}, nil)
	if err == nil {
		t.Fatal("a login that left no credential reported success")
	}
	if !strings.Contains(err.Error(), "cert.pem") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

// A wildcard that cannot be routed fails for a reason the message does not
// give, so the reason is added where it is known.
func TestRouteExplainsAWildcardRefusal(t *testing.T) {
	cli := fakeCloudflared(t, `echo "failed to add route: record is invalid" >&2; exit 1`)

	err := cli.Route(context.Background(), "tid", "*.example.com")
	if err == nil {
		t.Fatal("a refused route reported success")
	}
	if !strings.Contains(err.Error(), "plan") {
		t.Errorf("a wildcard refusal does not mention the plan: %v", err)
	}

	// An ordinary name gets the refusal as it came, with nothing invented.
	err = cli.Route(context.Background(), "tid", "app.example.com")
	if err == nil {
		t.Fatal("a refused route reported success")
	}
	if strings.Contains(err.Error(), "plan") {
		t.Errorf("an ordinary name was given the wildcard explanation: %v", err)
	}
}

func TestListReadsTheJSONOutput(t *testing.T) {
	cli := fakeCloudflared(t, `echo '[{"id":"a1","name":"home"},{"id":"b2","name":"other"}]'`)

	found, ok, err := cli.Find(context.Background(), "other")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !ok || found.ID != "b2" {
		t.Errorf("found %+v, ok=%v", found, ok)
	}

	if _, ok, _ := cli.Find(context.Background(), "absent"); ok {
		t.Error("a tunnel that is not on the account was found")
	}
}

// cloudflared reads its credentials through its home directory, which on
// OpenWrt is not somewhere that exists.
func TestTheHomeDirectoryIsPinned(t *testing.T) {
	cli := fakeCloudflared(t, `echo "HOME=$HOME"; echo "CERT=$TUNNEL_ORIGIN_CERT"`)

	out, err := cli.run(context.Background(), "anything")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "HOME="+cli.Dir) {
		t.Errorf("home was not pinned: %s", out)
	}
	if !strings.Contains(out, "CERT="+filepath.Join(cli.Dir, "cert.pem")) {
		t.Errorf("the credential path was not pinned: %s", out)
	}
}

// The reason cloudflared gives is at the end of its output; the start is
// startup chatter that buries it.
func TestAFailureKeepsTheReasonNotTheChatter(t *testing.T) {
	cli := fakeCloudflared(t, `
echo "INF starting"
echo "INF loading"
echo "INF connecting"
echo "INF still going"
echo "ERR the actual reason" >&2
exit 1
`)

	_, err := cli.run(context.Background(), "tunnel", "list")
	if err == nil {
		t.Fatal("a failing command reported success")
	}
	if !strings.Contains(err.Error(), "the actual reason") {
		t.Errorf("the reason was cut off: %v", err)
	}
}
