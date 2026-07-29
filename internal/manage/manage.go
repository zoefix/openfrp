package manage

import (
	"github.com/zoefix/openfrp/internal/storage"
	"github.com/zoefix/openfrp/internal/storage/repo"
)

type Service struct {
	db       *storage.DB
	accounts *repo.Accounts
	orders   *repo.Orders
	traffic  *repo.Traffic

	httpSolver *httpSolver
}

func New(path string) (*Service, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}

	return &Service{
		db:       db,
		accounts: repo.NewAccounts(db.DB),
		orders:   repo.NewOrders(db.DB),
		traffic:  repo.NewTraffic(db.DB),
	}, nil
}

func (s *Service) Close() error { return s.db.Close() }

// Traffic exposes the daily history, so the daemon can record into the same
// database the certificates already live in rather than opening a second one.
func (s *Service) Traffic() *repo.Traffic { return s.traffic }
