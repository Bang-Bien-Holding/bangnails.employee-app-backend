package odoo

import "testing"

func TestFakeClient_FetchStores(t *testing.T) {
	c := NewFakeClient()

	t.Run("first page returns a full batch", func(t *testing.T) {
		stores, err := c.FetchStores(t.Context(), 100, 0)
		if err != nil {
			t.Fatalf("FetchStores() error = %v", err)
		}
		if len(stores) != 100 {
			t.Fatalf("len(stores) = %d, want 100", len(stores))
		}
		if stores[0].ID != 1 {
			t.Errorf("stores[0].ID = %d, want 1", stores[0].ID)
		}
		if stores[0].Name == "" || stores[0].City == "" {
			t.Errorf("stores[0] has empty Name/City: %+v", stores[0])
		}
		if len(stores[0].OdooUserIDs) == 0 {
			t.Errorf("stores[0].OdooUserIDs is empty, want at least one assigned user")
		}
	})

	t.Run("later page returns the remainder, sized less than limit", func(t *testing.T) {
		stores, err := c.FetchStores(t.Context(), 100, 200)
		if err != nil {
			t.Fatalf("FetchStores() error = %v", err)
		}
		if len(stores) != 50 {
			t.Fatalf("len(stores) = %d, want 50", len(stores))
		}
		if stores[0].ID != 201 {
			t.Errorf("stores[0].ID = %d, want 201", stores[0].ID)
		}
		if stores[len(stores)-1].ID != 250 {
			t.Errorf("last ID = %d, want 250", stores[len(stores)-1].ID)
		}
	})

	t.Run("offset past the end returns empty, the pagination loop's break signal", func(t *testing.T) {
		stores, err := c.FetchStores(t.Context(), 100, 300)
		if err != nil {
			t.Fatalf("FetchStores() error = %v", err)
		}
		if len(stores) != 0 {
			t.Fatalf("len(stores) = %d, want 0", len(stores))
		}
	})

	t.Run("paginating start to finish visits exactly the advertised total once each", func(t *testing.T) {
		seen := map[int]bool{}
		for offset := 0; ; offset += 100 {
			stores, err := c.FetchStores(t.Context(), 100, offset)
			if err != nil {
				t.Fatalf("FetchStores() error = %v", err)
			}
			if len(stores) == 0 {
				break
			}
			for _, s := range stores {
				if seen[s.ID] {
					t.Fatalf("store ID %d fetched more than once", s.ID)
				}
				seen[s.ID] = true
			}
		}
		if len(seen) != 250 {
			t.Fatalf("total distinct stores seen = %d, want 250", len(seen))
		}
	})

	t.Run("same offset is deterministic across calls", func(t *testing.T) {
		first, err := c.FetchStores(t.Context(), 100, 0)
		if err != nil {
			t.Fatalf("FetchStores() error = %v", err)
		}
		second, err := c.FetchStores(t.Context(), 100, 0)
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
