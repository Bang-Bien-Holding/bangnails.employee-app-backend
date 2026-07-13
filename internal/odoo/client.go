package odoo

//go:generate mockgen -source=client.go -destination=mocks/mock_client.go -package=mocks

import "context"

// Store is one store record as Odoo reports it.
type Store struct {
	ID   int
	Name string
	City string
}

// Client fetches store data from Odoo. The store count is small enough that
// FetchStores returns the full list in one call — no pagination.
type Client interface {
	FetchStores(ctx context.Context) ([]Store, error)
}
