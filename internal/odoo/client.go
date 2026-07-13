package odoo

//go:generate mockgen -source=client.go -destination=mocks/mock_client.go -package=mocks

import "context"

// Store is one store record as Odoo reports it. OdooUserIDs are the Odoo
// user IDs currently assigned to work at this store — the store-sync flow
// uses this to keep employees.store_id in sync with Odoo's assignments.
type Store struct {
	ID          int
	Name        string
	City        string
	OdooUserIDs []int
}

// Client fetches store data from Odoo, one page at a time. FetchStores
// returns an empty slice once offset has passed the last record — callers
// paginate by incrementing offset by limit until they see that empty slice.
type Client interface {
	FetchStores(ctx context.Context, limit, offset int) ([]Store, error)
}
