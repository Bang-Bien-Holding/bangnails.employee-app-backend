package odoo

import "testing"

func TestFakeClient_FetchStores(t *testing.T) {
	c := NewFakeClient()

	t.Run("returns every fake store in one call", func(t *testing.T) {
		stores, err := c.FetchStores(t.Context())
		if err != nil {
			t.Fatalf("FetchStores() error = %v", err)
		}
		if len(stores) != totalFakeStores {
			t.Fatalf("len(stores) = %d, want %d", len(stores), totalFakeStores)
		}
		if stores[0].ID != 1 {
			t.Errorf("stores[0].ID = %d, want 1", stores[0].ID)
		}
		if stores[len(stores)-1].ID != totalFakeStores {
			t.Errorf("last ID = %d, want %d", stores[len(stores)-1].ID, totalFakeStores)
		}
		for _, s := range stores {
			if s.Name == "" || s.City == "" {
				t.Fatalf("store %+v has empty Name/City", s)
			}
		}
	})

	t.Run("same call is deterministic across calls", func(t *testing.T) {
		first, err := c.FetchStores(t.Context())
		if err != nil {
			t.Fatalf("FetchStores() error = %v", err)
		}
		second, err := c.FetchStores(t.Context())
		if err != nil {
			t.Fatalf("FetchStores() error = %v", err)
		}
		if len(first) != len(second) {
			t.Fatalf("len mismatch: %d vs %d", len(first), len(second))
		}
		for i := range first {
			if first[i].ID != second[i].ID || first[i].Name != second[i].Name || first[i].City != second[i].City {
				t.Fatalf("store %d differs between calls: %+v vs %+v", i, first[i], second[i])
			}
		}
	})
}

func TestFakeClient_FetchEmployeesByEmployeeIDs(t *testing.T) {
	c := NewFakeClient()

	t.Run("returns a matching employee for each known id", func(t *testing.T) {
		ids := []string{fakeEmployeeID(1), fakeEmployeeID(2)}
		employees, err := c.FetchEmployeesByEmployeeIDs(t.Context(), ids)
		if err != nil {
			t.Fatalf("FetchEmployeesByEmployeeIDs() error = %v", err)
		}
		if len(employees) != 2 {
			t.Fatalf("len(employees) = %d, want 2", len(employees))
		}
		for i, e := range employees {
			if e.EmployeeID != ids[i] {
				t.Errorf("employees[%d].EmployeeID = %q, want %q", i, e.EmployeeID, ids[i])
			}
			if e.FullName == "" || e.Email == "" || e.Username == "" || e.Role == "" {
				t.Errorf("employee %+v has an empty field", e)
			}
		}
	})

	t.Run("omits ids Odoo doesn't recognize", func(t *testing.T) {
		ids := []string{fakeEmployeeID(1), "unknown-id"}
		employees, err := c.FetchEmployeesByEmployeeIDs(t.Context(), ids)
		if err != nil {
			t.Fatalf("FetchEmployeesByEmployeeIDs() error = %v", err)
		}
		if len(employees) != 1 {
			t.Fatalf("len(employees) = %d, want 1", len(employees))
		}
		if employees[0].EmployeeID != fakeEmployeeID(1) {
			t.Errorf("employees[0].EmployeeID = %q, want %q", employees[0].EmployeeID, fakeEmployeeID(1))
		}
	})

	t.Run("empty input returns an empty result", func(t *testing.T) {
		employees, err := c.FetchEmployeesByEmployeeIDs(t.Context(), nil)
		if err != nil {
			t.Fatalf("FetchEmployeesByEmployeeIDs() error = %v", err)
		}
		if len(employees) != 0 {
			t.Fatalf("len(employees) = %d, want 0", len(employees))
		}
	})
}
