//go:build dbe2e

// storesSearchE2EFixtures is the Postgres-facing seam for
// cmd/stores_search_e2e_test.go (issues #32/#33/#34): creating Store rows
// directly via plain SQL, bypassing HTTP entirely — same reasoning as
// cmd/employees_search_e2e_fixtures_test.go's Employee/Store. This suite
// needs its own fixtures type rather than reusing loginE2EFixtures.Store
// because it needs direct control over StoreName/City/WifiWhitelistEnabled/
// OdooStoreID per Store — exactly the fields GET /v1/stores' filters key
// off — which loginE2EFixtures.Store doesn't expose (it always derives
// StoreName from a generated name and has no City/OdooStoreID at all).
package main

import (
	"context"
	"testing"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgxpool"
)

type storesSearchE2EFixtures struct {
	pool *pgxpool.Pool
	repo repo.Querier
}

func newStoresSearchE2EFixtures(pool *pgxpool.Pool, q repo.Querier) *storesSearchE2EFixtures {
	return &storesSearchE2EFixtures{pool: pool, repo: q}
}

// storeSearchSeed configures Store's fixture. StoreName defaults to a fresh
// e2eUnique value when left blank. WifiWhitelistEnabled defaults to true
// (the store table's own DB default) when left nil. City and OdooStoreID
// default to NULL (unset) when left nil — set them explicitly to exercise
// city's substring matching (#32) and odoo_store_ids' exact-list matching
// (#34).
type storeSearchSeed struct {
	StoreName            string
	City                 *string
	WifiWhitelistEnabled *bool
	OdooStoreID          *string
}

// storesSearchE2EStore is Store's result: every field a test's filter query
// might need to reference.
type storesSearchE2EStore struct {
	ID          int64
	StoreName   string
	OdooStoreID string
}

// Store inserts a bare Store row directly — no sqlc query creates a plain
// local Store outside of Odoo sync (ADR-0009), same as
// cmd/login_e2e_fixtures_test.go's Store.
func (f *storesSearchE2EFixtures) Store(t *testing.T, seed storeSearchSeed) storesSearchE2EStore {
	t.Helper()
	ctx := t.Context()

	storeName := seed.StoreName
	if storeName == "" {
		storeName = "Search E2E " + e2eUnique(t, "store")
	}
	wifiWhitelistEnabled := true
	if seed.WifiWhitelistEnabled != nil {
		wifiWhitelistEnabled = *seed.WifiWhitelistEnabled
	}

	var id int64
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO store (store_name, city, wifi_whitelist_enabled, odoo_store_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, storeName, seed.City, wifiWhitelistEnabled, seed.OdooStoreID).Scan(&id); err != nil {
		t.Fatalf("seed store: insert: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM store WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup: delete store %d: %v", id, err)
		}
	})

	var odooStoreID string
	if seed.OdooStoreID != nil {
		odooStoreID = *seed.OdooStoreID
	}
	return storesSearchE2EStore{ID: id, StoreName: storeName, OdooStoreID: odooStoreID}
}
