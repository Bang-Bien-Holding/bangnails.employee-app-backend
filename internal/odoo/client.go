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
// employee_id business identifier used in our own employees table — Odoo
// never sees our internal bigserial id.
type Employee struct {
	EmployeeID string
	FullName   string
	Email      string
	Username   string
	Role       string
}

// Client fetches store and employee data from Odoo. The store count is
// small enough that FetchStores returns the full list in one call — no
// pagination. FetchEmployeesByEmployeeIDs has no such guarantee, so its
// caller (employees.service.runSync) pages through its ids in fixed-size
// batches rather than sending them all in one call.
type Client interface {
	FetchStores(ctx context.Context) ([]Store, error)
	// FetchEmployeesByEmployeeIDs looks up employees by employee_id. An id
	// Odoo doesn't recognize is simply omitted from the result rather than
	// erroring — the caller distinguishes "not found" from "found" by
	// checking which requested ids came back.
	FetchEmployeesByEmployeeIDs(ctx context.Context, employeeIDs []string) ([]Employee, error)
}
