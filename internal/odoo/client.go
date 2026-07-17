package odoo

//go:generate mockgen -source=client.go -destination=mocks/mock_client.go -package=mocks

import "context"

// Store is one store record as Odoo reports it.
type Store struct {
	ID   int
	Name string
	City string
}

// Employee is one employee record as Odoo reports it, keyed by the same
// odoo_employee_id business identifier used in our own employees table —
// Odoo never sees our internal bigserial id. Username has no Odoo
// equivalent — it's local-only (ADR-0008) — so it isn't part of this type.
// StoreIDs is Odoo's own store ids (x_pos_shop_ids, ADR-0009), resolved to
// this system's internal store.id by the caller (employees.service.runSync)
// via store.odoo_store_id, the same join key store sync already uses.
type Employee struct {
	OdooEmployeeID int64
	FullName       string
	Email          string
	StoreIDs       []int
}

// Client fetches store and employee data from Odoo. The store count is
// small enough that FetchStores returns the full list in one call — no
// pagination. FetchEmployeesByOdooEmployeeIDs has no such guarantee, so its
// caller (employees.service.runSync) pages through its ids in fixed-size
// batches rather than sending them all in one call.
type Client interface {
	FetchStores(ctx context.Context) ([]Store, error)
	// FetchEmployeesByOdooEmployeeIDs looks up employees by
	// odoo_employee_id. An id Odoo doesn't recognize is simply omitted from
	// the result rather than erroring — the caller distinguishes "not
	// found" from "found" by checking which requested ids came back.
	FetchEmployeesByOdooEmployeeIDs(ctx context.Context, odooEmployeeIDs []int64) ([]Employee, error)
}
