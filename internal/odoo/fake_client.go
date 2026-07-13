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

// totalFakeEmployees is how many employee_ids FakeClient recognizes — any
// other id passed to FetchEmployeesByEmployeeIDs is treated as unknown to
// Odoo and simply omitted from the result, so callers can exercise the
// "not found" path deterministically.
const totalFakeEmployees = 10

var fakeEmployeeRoles = []string{"technician", "manager", "cashier", "admin"}

// fakeEmployeeID returns the deterministic employee_id FakeClient recognizes
// for the nth fake employee (1-indexed), e.g. "ODOO-EMP-001".
func fakeEmployeeID(n int) string {
	return fmt.Sprintf("ODOO-EMP-%03d", n)
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

// FetchEmployeesByEmployeeIDs recognizes only fakeEmployeeID(1)..fakeEmployeeID(totalFakeEmployees);
// any other requested id is omitted from the result, simulating an id Odoo
// doesn't know about.
func (c *FakeClient) FetchEmployeesByEmployeeIDs(ctx context.Context, employeeIDs []string) ([]Employee, error) {
	known := make(map[string]int, totalFakeEmployees)
	for n := 1; n <= totalFakeEmployees; n++ {
		known[fakeEmployeeID(n)] = n
	}

	employees := make([]Employee, 0, len(employeeIDs))
	for _, id := range employeeIDs {
		n, ok := known[id]
		if !ok {
			continue
		}
		employees = append(employees, Employee{
			EmployeeID: id,
			FullName:   fmt.Sprintf("Fake Employee #%d", n),
			Email:      fmt.Sprintf("fake-employee-%d@example.com", n),
			Username:   fmt.Sprintf("fakeemployee%d", n),
			Role:       fakeEmployeeRoles[n%len(fakeEmployeeRoles)],
		})
	}
	return employees, nil
}
