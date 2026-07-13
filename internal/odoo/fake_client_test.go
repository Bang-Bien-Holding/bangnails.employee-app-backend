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
