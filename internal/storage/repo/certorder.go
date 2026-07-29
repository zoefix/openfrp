package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	StatePending = "pending"
	StateIssuing = "issuing"
	StateIssued  = "issued"
	StateFailed  = "failed"
)

type Order struct {
	ID      int64
	Domains []string
	KeyType string
	CA      string

	AccountID int64

	Email string

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

type Orders struct {
	db *sql.DB
}

func NewOrders(db *sql.DB) *Orders { return &Orders{db: db} }

const orderColumns = `id, domains, key_type, ca, account_id, email, auto_renew,
	state, last_error, certificate, private_key, issued_at, expires_at,
	created_at, updated_at`

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

func (r *Orders) Get(ctx context.Context, id int64) (Order, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM cert_order WHERE id = ?`, id)

	order, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, fmt.Errorf("repo: order %d: %w", id, ErrNotFound)
	}
	return order, err
}

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

func (r *Orders) SetState(ctx context.Context, id int64, state, lastError string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE cert_order SET state = ?, last_error = ?, updated_at = unixepoch()
		WHERE id = ?`, state, lastError, id)
	if err != nil {
		return fmt.Errorf("repo: set order %d state: %w", id, err)
	}
	return affectedOne(result, "order", id)
}

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

func (r *Orders) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM cert_order WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("repo: delete order %d: %w", id, err)
	}
	return affectedOne(result, "order", id)
}

func (r *Orders) AppendEvent(ctx context.Context, orderID int64, kind, detail string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO cert_event (order_id, kind, detail, created_at)
		VALUES (?, ?, ?, unixepoch())`, orderID, kind, detail)
	if err != nil {
		return fmt.Errorf("repo: append event to order %d: %w", orderID, err)
	}
	return nil
}

type Event struct {
	ID        int64
	Kind      string
	Detail    string
	CreatedAt int64
}

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

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
