package storage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zoefix/openfrp/internal/storage"
	"github.com/zoefix/openfrp/internal/storage/repo"
)

func open(t *testing.T) *storage.DB {
	t.Helper()

	db, err := storage.Open(filepath.Join(t.TempDir(), "openfrp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDatabaseIsNotReadableByOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openfrp.db")

	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("database mode is %#o, want 0600", mode)
	}
}

func TestOpenRepairsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openfrp.db")

	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode after reopen is %#o, want 0600", mode)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openfrp.db")

	var first, firstCount int

	for i := range 3 {
		db, err := storage.Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}

		var version, count int
		if err := db.QueryRow(
			`SELECT max(version), count(*) FROM schema_version`).Scan(&version, &count); err != nil {
			t.Fatalf("read version: %v", err)
		}
		db.Close()

		if version < 1 {
			t.Fatalf("open %d applied no migrations", i+1)
		}

		if i == 0 {
			first, firstCount = version, count
			continue
		}
		if version != first || count != firstCount {
			t.Errorf("open %d moved the schema from version %d (%d applied) to %d (%d applied)",
				i+1, first, firstCount, version, count)
		}
	}
}

func TestAccountRoundTrip(t *testing.T) {
	ctx := context.Background()
	accounts := repo.NewAccounts(open(t).DB)

	created, err := accounts.Create(ctx, repo.Account{
		Name:     "aliyun-main",
		Provider: "aliyun",
		Credentials: map[string]string{
			"access_key_id":     "LTAI5t",
			"access_key_secret": "verysecret",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("create returned no id")
	}

	got, err := accounts.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Credentials["access_key_secret"] != "verysecret" {
		t.Errorf("credentials did not survive the round trip: %v", got.Credentials)
	}

	got.Name = "aliyun-renamed"
	got.Credentials["access_key_secret"] = "rotated"
	if err := accounts.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	reread, err := accounts.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Name != "aliyun-renamed" ||
		reread.Credentials["access_key_secret"] != "rotated" {
		t.Errorf("update did not stick: %+v", reread)
	}

	if err := accounts.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := accounts.Get(ctx, created.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("get after delete = %v, want ErrNotFound", err)
	}
}

func TestUpdateMissingAccountIsNotFound(t *testing.T) {
	accounts := repo.NewAccounts(open(t).DB)

	err := accounts.Update(context.Background(), repo.Account{
		ID: 9999, Name: "ghost", Provider: "aliyun",
		Credentials: map[string]string{},
	})
	if !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("update of a missing row = %v, want ErrNotFound", err)
	}
}

func TestDuplicateAccountNameIsRejected(t *testing.T) {
	ctx := context.Background()
	accounts := repo.NewAccounts(open(t).DB)

	account := repo.Account{Name: "same", Provider: "cloudflare",
		Credentials: map[string]string{"api_token": "t"}}

	if _, err := accounts.Create(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create(ctx, account); err == nil {
		t.Error("a duplicate name was accepted; the UI relies on names being unique")
	}
}

func TestDeletingAccountKeepsCertificates(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	accounts := repo.NewAccounts(db.DB)
	orders := repo.NewOrders(db.DB)

	account, err := accounts.Create(ctx, repo.Account{
		Name: "dnspod", Provider: "dnspod",
		Credentials: map[string]string{"secret_id": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}

	order, err := orders.Create(ctx, repo.Order{
		Domains: []string{"*.aiqno.com"}, KeyType: "ec256",
		CA: "https://acme-v02.api.letsencrypt.org/directory", AccountID: account.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := orders.StoreCertificate(ctx, order.ID,
		[]byte("CERT"), []byte("KEY"), 1000, 2000); err != nil {
		t.Fatal(err)
	}

	if err := accounts.Delete(ctx, account.ID); err != nil {
		t.Fatal(err)
	}

	survivor, err := orders.Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("the order died with its account: %v", err)
	}
	if string(survivor.Certificate) != "CERT" {
		t.Error("the certificate material was lost")
	}
	if survivor.AccountID != 0 {
		t.Errorf("account id is %d, want 0 after the account was deleted", survivor.AccountID)
	}
}

func TestDueForRenewal(t *testing.T) {
	ctx := context.Background()
	orders := repo.NewOrders(open(t).DB)

	expiring, err := orders.Create(ctx, repo.Order{
		Domains: []string{"soon.example"}, KeyType: "ec256", CA: "ca"})
	if err != nil {
		t.Fatal(err)
	}
	if err := orders.StoreCertificate(ctx, expiring.ID, []byte("c"), []byte("k"), 0, 1_000); err != nil {
		t.Fatal(err)
	}

	distant, err := orders.Create(ctx, repo.Order{
		Domains: []string{"later.example"}, KeyType: "ec256", CA: "ca"})
	if err != nil {
		t.Fatal(err)
	}
	if err := orders.StoreCertificate(ctx, distant.ID, []byte("c"), []byte("k"), 0, 9_000); err != nil {
		t.Fatal(err)
	}

	failed, err := orders.Create(ctx, repo.Order{
		Domains: []string{"broken.example"}, KeyType: "ec256", CA: "ca"})
	if err != nil {
		t.Fatal(err)
	}
	if err := orders.SetState(ctx, failed.ID, repo.StateFailed, "dns account missing"); err != nil {
		t.Fatal(err)
	}

	due, err := orders.DueForRenewal(ctx, 5_000)
	if err != nil {
		t.Fatal(err)
	}

	if len(due) != 1 {
		t.Fatalf("got %d orders due, want 1: %+v", len(due), due)
	}
	if due[0].ID != expiring.ID {
		t.Errorf("due order is %d, want %d", due[0].ID, expiring.ID)
	}
}

func TestEventsAreNewestFirstAndCascade(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	orders := repo.NewOrders(db.DB)

	order, err := orders.Create(ctx, repo.Order{
		Domains: []string{"a.example"}, KeyType: "ec256", CA: "ca"})
	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"requested", "issued", "deployed"} {
		if err := orders.AppendEvent(ctx, order.ID, kind, ""); err != nil {
			t.Fatal(err)
		}
	}

	events, err := orders.Events(ctx, order.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].Kind != "deployed" {
		t.Errorf("first event is %q, want the newest (deployed)", events[0].Kind)
	}

	if err := orders.Delete(ctx, order.ID); err != nil {
		t.Fatal(err)
	}

	var remaining int
	if err := db.QueryRow(
		`SELECT count(*) FROM cert_event WHERE order_id = ?`, order.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("%d events outlived their order; foreign_keys is probably off", remaining)
	}
}
