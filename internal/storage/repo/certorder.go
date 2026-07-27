package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Order states.
const (
	StatePending = "pending"
	StateIssuing = "issuing"
	StateIssued  = "issued"
	StateFailed  = "failed"
)

// Order is one certificate and everything needed to renew it.
type Order struct {
	ID      int64
	Domains []string
	KeyType string
	CA      string

	// AccountID is the DNS account used for DNS-01. Zero means the account was
	// deleted, which is recoverable by pointing the order at another one.
	AccountID int64

	// Email identifies the ACME account that issued this, so a renewal reuses
	// it rather than registering a new one against the CA's rate limit.
	Email string

	// AutoRenew is false for a certificate the operator renews by hand.
	//
	// A pointer so that "not specified" is distinguishable from "explicitly
	// off", and unspecified means on. With a plain bool, Go's zero value
	// silently overrode the column default and no certificate was ever due for
	// renewal — a failure that shows up as an expired certificate months later
	// and nowhere before that.
	AutoRenew *bool

	State     string
	LastError string

	Certificate []byte
	PrivateKey  []byte

	IssuedAt  int64
	ExpiresAt int64

	CreatedAt int64
	UpdatedAt int64
}

// Orders stores certificate orders.
type Orders struct {
	db *sql.DB
}

// NewOrders returns a repository over db.
func NewOrders(db *sql.DB) *Orders { return &Orders{db: db} }

const orderColumns = `id, domains, key_type, ca, account_id, email, auto_renew,
	state, last_error, certificate, private_key, issued_at, expires_at,
	created_at, updated_at`

// List returns every order, newest first.
func (r *Orders) List(ctx context.Context) ([]Order, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+orderColumns+` FROM cert_order ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("repo: list orders: %w", err)
	}
	defer rows.Close()

	var out []Order
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, rows.Err()
}

// Get returns one order.
func (r *Orders) Get(ctx context.Context, id int64) (Order, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM cert_order WHERE id = ?`, id)

	order, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, fmt.Errorf("repo: order %d: %w", id, ErrNotFound)
	}
	return order, err
}

// DueForRenewal returns issued orders expiring at or before cutoff, soonest
// first. This is the scheduler's whole query.
//
// Orders that have never been issued are excluded: they have no expiry to
// compare, and retrying a failed first issuance on the renewal timer would
// hammer the CA's rate limit. Orders with auto-renew off are excluded because
// the operator said so.
func (r *Orders) DueForRenewal(ctx context.Context, cutoff int64) ([]Order, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+orderColumns+` FROM cert_order
		WHERE state = ? AND auto_renew = 1
		  AND expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY expires_at`, StateIssued, cutoff)
	if err != nil {
		return nil, fmt.Errorf("repo: list renewals: %w", err)
	}
	defer rows.Close()

	var out []Order
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, rows.Err()
}

// Create stores a new order.
func (r *Orders) Create(ctx context.Context, order Order) (Order, error) {
	domains, err := json.Marshal(order.Domains)
	if err != nil {
		return Order{}, fmt.Errorf("repo: encode domains: %w", err)
	}
	if order.State == "" {
		order.State = StatePending
	}
	if order.AutoRenew == nil {
		order.AutoRenew = new(bool)
		*order.AutoRenew = true
	}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO cert_order
			(domains, key_type, ca, account_id, email, auto_renew, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, unixepoch(), unixepoch())
		RETURNING id, created_at, updated_at`,
		string(domains), order.KeyType, order.CA,
		nullableID(order.AccountID), order.Email, *order.AutoRenew, order.State)

	if err := row.Scan(&order.ID, &order.CreatedAt, &order.UpdatedAt); err != nil {
		return Order{}, fmt.Errorf("repo: create order: %w", err)
	}
	return order, nil
}

// SetState records a state transition and its explanation.
func (r *Orders) SetState(ctx context.Context, id int64, state, lastError string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE cert_order SET state = ?, last_error = ?, updated_at = unixepoch()
		WHERE id = ?`, state, lastError, id)
	if err != nil {
		return fmt.Errorf("repo: set order %d state: %w", id, err)
	}
	return affectedOne(result, "order", id)
}

// StoreCertificate saves issued material and marks the order issued.
//
// The write is one statement so an order can never be observed as issued while
// still holding the previous certificate, which would have the renewal
// scheduler skip it and the server serve an expired chain.
func (r *Orders) StoreCertificate(ctx context.Context, id int64,
	certificate, privateKey []byte, issuedAt, expiresAt int64) error {

	result, err := r.db.ExecContext(ctx, `
		UPDATE cert_order
		SET certificate = ?, private_key = ?, issued_at = ?, expires_at = ?,
		    state = ?, last_error = '', updated_at = unixepoch()
		WHERE id = ?`,
		certificate, privateKey, issuedAt, expiresAt, StateIssued, id)
	if err != nil {
		return fmt.Errorf("repo: store certificate for order %d: %w", id, err)
	}
	return affectedOne(result, "order", id)
}

// Delete removes an order and, by cascade, its event history.
func (r *Orders) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM cert_order WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("repo: delete order %d: %w", id, err)
	}
	return affectedOne(result, "order", id)
}

// AppendEvent records something that happened to an order.
func (r *Orders) AppendEvent(ctx context.Context, orderID int64, kind, detail string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO cert_event (order_id, kind, detail, created_at)
		VALUES (?, ?, ?, unixepoch())`, orderID, kind, detail)
	if err != nil {
		return fmt.Errorf("repo: append event to order %d: %w", orderID, err)
	}
	return nil
}

// Event is one entry in an order's history.
type Event struct {
	ID        int64
	Kind      string
	Detail    string
	CreatedAt int64
}

// Events returns an order's history, newest first.
func (r *Orders) Events(ctx context.Context, orderID int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, kind, detail, created_at FROM cert_event
		WHERE order_id = ? ORDER BY id DESC LIMIT ?`, orderID, limit)
	if err != nil {
		return nil, fmt.Errorf("repo: list events for order %d: %w", orderID, err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Kind, &event.Detail, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func scanOrder(src scanner) (Order, error) {
	var (
		order     Order
		domains   string
		accountID sql.NullInt64
		autoRenew bool
		issuedAt  sql.NullInt64
		expiresAt sql.NullInt64
	)

	if err := src.Scan(&order.ID, &domains, &order.KeyType, &order.CA,
		&accountID, &order.Email, &autoRenew,
		&order.State, &order.LastError,
		&order.Certificate, &order.PrivateKey,
		&issuedAt, &expiresAt, &order.CreatedAt, &order.UpdatedAt); err != nil {
		return Order{}, err
	}

	if err := json.Unmarshal([]byte(domains), &order.Domains); err != nil {
		return Order{}, fmt.Errorf("repo: order %d has unreadable domains: %w", order.ID, err)
	}

	order.AutoRenew = &autoRenew
	order.AccountID = accountID.Int64
	order.IssuedAt = issuedAt.Int64
	order.ExpiresAt = expiresAt.Int64

	return order, nil
}

// nullableID maps a zero id to NULL, so the foreign key is either valid or
// absent rather than pointing at an account 0 that cannot exist.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
