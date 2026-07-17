package odoo

import (
	"context"
	"fmt"
)

// totalFakeStores is how many records FakeClient pretends Odoo holds.
const totalFakeStores = 12

var fakeCityNames = []string{
	"Hanoi", "Ho Chi Minh City", "Da Nang", "Hai Phong", "Can Tho",
	"Nha Trang", "Hue", "Vung Tau", "Bien Hoa", "Quy Nhon",
}

// totalFakeEmployees is how many odoo_employee_ids FakeClient recognizes —
// any other id passed to FetchEmployeesByOdooEmployeeIDs is treated as
// unknown to Odoo and simply omitted from the result, so callers can
// exercise the "not found" path deterministically.
const totalFakeEmployees = 10

// fakeOdooEmployeeIDBase offsets fakeOdooEmployeeID's output so fake Odoo
// employee ids are visibly distinct from this system's own internal
// bigserial employee ids in test output/logs.
const fakeOdooEmployeeIDBase = 2000

// fakeOdooEmployeeID returns the deterministic odoo_employee_id FakeClient
// recognizes for the nth fake employee (1-indexed).
func fakeOdooEmployeeID(n int) int64 {
	return int64(fakeOdooEmployeeIDBase + n)
}

// FakeClient is a deterministic, in-memory stand-in for a real Odoo
// connection (Phase 2 of the store-sync spec: no live Odoo integration
// exists yet). It always reports the same totalFakeStores records, so
// tests and the sync flow behave identically across runs.
type FakeClient struct{}

func NewFakeClient() *FakeClient {
	return &FakeClient{}
}

func (c *FakeClient) FetchStores(ctx context.Context) ([]Store, error) {
	stores := make([]Store, 0, totalFakeStores)
	for i := range totalFakeStores {
		id := i + 1
		stores = append(stores, Store{
			ID:   id,
			Name: fmt.Sprintf("Store #%d", id),
			City: fakeCityNames[id%len(fakeCityNames)],
		})
	}
	return stores, nil
}

// FetchEmployeesByOdooEmployeeIDs recognizes only
// fakeOdooEmployeeID(1)..fakeOdooEmployeeID(totalFakeEmployees); any other
// requested id is omitted from the result, simulating an id Odoo doesn't
// know about.
func (c *FakeClient) FetchEmployeesByOdooEmployeeIDs(ctx context.Context, odooEmployeeIDs []int64) ([]Employee, error) {
	known := make(map[int64]int, totalFakeEmployees)
	for n := 1; n <= totalFakeEmployees; n++ {
		known[fakeOdooEmployeeID(n)] = n
	}

	employees := make([]Employee, 0, len(odooEmployeeIDs))
	for _, id := range odooEmployeeIDs {
		n, ok := known[id]
		if !ok {
			continue
		}
		employees = append(employees, Employee{
			OdooEmployeeID: id,
			FullName:       fmt.Sprintf("Fake Employee #%d", n),
			Email:          fmt.Sprintf("fake-employee-%d@example.com", n),
			Username:       fmt.Sprintf("fakeemployee%d", n),
		})
	}
	return employees, nil
}
