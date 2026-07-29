package manage

import (
	"github.com/zoefix/openfrp/internal/storage"
	"github.com/zoefix/openfrp/internal/storage/repo"
)

type Service struct {
	db       *storage.DB
	accounts *repo.Accounts
	orders   *repo.Orders

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
	}, nil
}

func (s *Service) Close() error { return s.db.Close() }
