package cloudflare

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withdrawHarness is a CLI whose credential and API both answer.
func withdrawHarness(t *testing.T, records string) (CLI, *[]string) {
	t.Helper()

	dir := t.TempDir()
	cli := CLI{Binary: filepath.Join(dir, "cloudflared"), Dir: filepath.Join(dir, "home")}

	if err := os.MkdirAll(cli.configDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(
		[]byte(`{"zoneID":"z1","accountID":"a1","apiToken":"tok"}`))
	pem := "-----BEGIN ARGO TUNNEL TOKEN-----\n" + payload +
		"\n-----END ARGO TUNNEL TOKEN-----\n"
	if err := os.WriteFile(cli.CertPath(), []byte(pem), 0o600); err != nil {
		t.Fatal(err)
	}

	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			deleted = append(deleted, r.URL.Path)
			w.Write([]byte(`{"success":true}`))
			return
		}
		w.Write([]byte(records))
	}))
	t.Cleanup(server.Close)

	previous := api
	api = server.URL
	t.Cleanup(func() { api = previous })

	return cli, &deleted
}

const oneRecord = `{"success":true,"result":[` +
	`{"id":"r1","type":"CNAME","content":"tid.cfargotunnel.com"}]}`

// A name that was published and is no longer in the configuration has to lose
// its record: nothing else will remove it, and it would keep answering.
func TestWithdrawRemovesANameThatWasDeleted(t *testing.T) {
	cli, deleted := withdrawHarness(t, oneRecord)
	ctx := context.Background()

	cli.Withdraw(ctx, "cf", []Rule{{Hostname: "a.example.com"}}, nil)
	if len(*deleted) != 0 {
		t.Fatalf("something was deleted on the first run: %v", *deleted)
	}

	withdrawn := cli.Withdraw(ctx, "cf", nil, nil)
	if len(withdrawn) != 1 || withdrawn[0] != "a.example.com" {
		t.Errorf("withdrew %v, want the name that was removed", withdrawn)
	}
	if len(*deleted) != 1 {
		t.Errorf("deleted %v", *deleted)
	}
}

// A name still published is left alone, however many times apply runs.
func TestWithdrawLeavesNamesStillPublished(t *testing.T) {
	cli, deleted := withdrawHarness(t, oneRecord)
	ctx := context.Background()

	rules := []Rule{{Hostname: "a.example.com"}, {Hostname: "b.example.com"}}
	cli.Withdraw(ctx, "cf", rules, nil)
	cli.Withdraw(ctx, "cf", rules, nil)

	if len(*deleted) != 0 {
		t.Errorf("a published name was withdrawn: %v", *deleted)
	}
}

// One server's names are not withdrawn because another server stopped
// publishing them.
func TestWithdrawIsScopedToOneServer(t *testing.T) {
	cli, deleted := withdrawHarness(t, oneRecord)
	ctx := context.Background()

	cli.Withdraw(ctx, "cf1", []Rule{{Hostname: "a.example.com"}}, nil)
	cli.Withdraw(ctx, "cf2", []Rule{{Hostname: "b.example.com"}}, nil)
	cli.Withdraw(ctx, "cf2", nil, nil)

	if len(*deleted) != 1 {
		t.Fatalf("deleted %v, want only the second server's name", *deleted)
	}

	// The first server's name is still recorded, so it is not withdrawn later.
	cli.Withdraw(ctx, "cf1", []Rule{{Hostname: "a.example.com"}}, nil)
	if len(*deleted) != 1 {
		t.Errorf("the other server's name was withdrawn: %v", *deleted)
	}
}

// A failure to withdraw is reported and does not stop the apply, because the
// alternative is leaving every other name unpublished.
func TestWithdrawReportsAFailureWithoutFailing(t *testing.T) {
	cli, _ := withdrawHarness(t,
		`{"success":false,"errors":[{"code":9109,"message":"no access"}]}`)
	ctx := context.Background()

	cli.Withdraw(ctx, "cf", []Rule{{Hostname: "a.example.com"}}, nil)

	var said []string
	cli.Withdraw(ctx, "cf", nil, func(line string) { said = append(said, line) })

	joined := strings.Join(said, " ")
	if !strings.Contains(joined, "a.example.com") {
		t.Errorf("the failure did not name the host: %v", said)
	}
}
