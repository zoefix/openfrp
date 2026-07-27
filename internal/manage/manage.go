// Package manage is the management service behind the LuCI pages.
//
// It sits between the storage repositories and whatever is driving them, and
// owns the rules that must hold no matter who is calling: credentials are
// redacted on the way out, secrets left blank on an edit keep their stored
// value, and a provider is constructed from stored credentials in exactly one
// place.
//
// The plan called for this to be reached over a local HTTP API. It is reached
// through openfrpc subcommands instead, because management has to work when
// the tunnel daemon is stopped — which is precisely when someone is setting
// the thing up — and because a listening socket with cloud credentials behind
// it is a surface this does not need. The rpcd backend and job worker that
// drive it already exist and are tested.
package manage

import (
	"github.com/zoefix/openfrp/internal/storage"
	"github.com/zoefix/openfrp/internal/storage/repo"
)

// Service exposes management operations over the database.
type Service struct {
	db       *storage.DB
	accounts *repo.Accounts
	orders   *repo.Orders

	// httpSolver answers ACME validations through the tunnel servers, for
	// certificates issued without DNS credentials. Nil when no server is
	// configured, which leaves DNS-01 as the only option.
	httpSolver *httpSolver
}

// New opens the database at path and returns a service over it.
//
// On an architecture with no SQLite driver this returns storage.ErrUnsupported,
// which callers should surface as "unavailable here" rather than as a crash.
func New(path string) (*Service, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}

	return &Service{
		db:       db,
		accounts: repo.NewAccounts(db.DB),
		orders:   repo.NewOrders(db.DB),
	}, nil
}

// Close releases the database.
func (s *Service) Close() error { return s.db.Close() }
