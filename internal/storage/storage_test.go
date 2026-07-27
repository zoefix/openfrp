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

// TestDatabaseIsNotReadableByOthers checks the file mode.
//
// The credentials column holds cloud provider access keys and the orders table
// holds certificate private keys. The file mode is the only thing protecting
// either, so it is worth asserting rather than assuming.
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

// TestOpenRepairsPermissions covers a database left too permissive by an older
// version or a restore. Opening it should correct the mode, not accept it.
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

// TestMigrationsAreIdempotent opens the same database repeatedly. Startup runs
// migrations every time, so a second open must be a no-op rather than an error
// or a duplicated table.
func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openfrp.db")

	for i := range 3 {
		db, err := storage.Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}

		var version int
		if err := db.QueryRow(
			`SELECT max(version) FROM schema_version`).Scan(&version); err != nil {
			t.Fatalf("read version: %v", err)
		}
		if version != 1 {
			t.Errorf("schema version is %d, want 1", version)
		}
		db.Close()
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

// TestUpdateMissingAccountIsNotFound guards the case where a row vanished
// between load and save. Without the rows-affected check this reports success.
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

// TestDeletingAccountKeepsCertificates is the important one.
//
// A certificate that is serving traffic must not disappear because someone
// removed the DNS account it was issued through. The order survives with no
// account and fails its next renewal loudly instead.
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

// TestDueForRenewal checks the scheduler's only query, including that an order
// which never issued is left alone rather than retried on the renewal timer.
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

	// Never issued, and failed. It has no expiry and must not be picked up.
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

	// The cascade only fires if foreign_keys is actually ON, which is off by
	// default in SQLite — so this doubles as a check that the DSN took effect.
	var remaining int
	if err := db.QueryRow(
		`SELECT count(*) FROM cert_event WHERE order_id = ?`, order.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("%d events outlived their order; foreign_keys is probably off", remaining)
	}
}
