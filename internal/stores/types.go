package stores

//go:generate mockgen -source=types.go -destination=service_mock_test.go -package=stores

import (
	"context"
	"errors"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrSyncInProgress is returned when SyncStores is called while a previous
// call is still running — Phase 1's concurrency guard.
var ErrSyncInProgress = errors.New("store sync already in progress")

// ErrStoreNotFound is returned by GetStoreByID for an unknown id, or one
// whose is_active is false — the store-sync feature's soft-delete flag
// doubles as the not-found condition here rather than introducing a second
// deletion concept for stores.
var ErrStoreNotFound = errors.New("store not found")

// SyncSummary reports the outcome of one SyncStores run.
type SyncSummary struct {
	TotalStoresProcessed int `json:"total_stores_processed"`
	InsertedStores       int `json:"inserted_stores"`
	UpdatedStores        int `json:"updated_stores"`
	DeletedStores        int `json:"deleted_stores"`
	Failed               int `json:"failed"`
}

// StoreDetail is the full picture of one store: the store row plus its
// current wifi whitelist. IPAddresses/MACAddresses are always non-nil
// (empty, not null, when a store has no entries yet) and already formatted
// as strings (dotted-decimal IPv4, colon-separated MAC) — GetStoreByID's
// caller doesn't need to know about netip.Addr/net.HardwareAddr.
type StoreDetail struct {
	Store        repo.Store
	IPAddresses  []string
	MACAddresses []string
}

type Service interface {
	SyncStores(ctx context.Context) (SyncSummary, error)
	GetStoreByID(ctx context.Context, id int64) (StoreDetail, error)
}

// storeResponse is the JSON shape returned by GetStoreByID (and, later,
// UpdateStore) — nullable store fields become pointers (nil rather than a
// zero value) so a store with no geofence set yet renders as
// null/null/null instead of the misleading 0/0/0.
type storeResponse struct {
	ID           int64              `json:"id"`
	StoreName    string             `json:"store_name"`
	OdooStoreID  *string            `json:"odoo_store_id"`
	City         *string            `json:"city"`
	Latitude     *float64           `json:"latitude"`
	Longitude    *float64           `json:"longitude"`
	RadiusMeters *int32             `json:"radius_meters"`
	IPAddresses  []string           `json:"ip_addresses"`
	MACAddresses []string           `json:"mac_addresses"`
	IsActive     bool               `json:"is_active"`
	CreatedAt    pgtype.Timestamptz `json:"created_at"`
	UpdatedAt    pgtype.Timestamptz `json:"updated_at"`
}

func newStoreResponse(detail StoreDetail) storeResponse {
	return storeResponse{
		ID:           detail.Store.ID,
		StoreName:    detail.Store.StoreName,
		OdooStoreID:  pgTextPtr(detail.Store.OdooStoreID),
		City:         pgTextPtr(detail.Store.City),
		Latitude:     pgNumericPtr(detail.Store.Latitude),
		Longitude:    pgNumericPtr(detail.Store.Longitude),
		RadiusMeters: pgInt4Ptr(detail.Store.RadiusMeters),
		IPAddresses:  detail.IPAddresses,
		MACAddresses: detail.MACAddresses,
		IsActive:     detail.Store.IsActive,
		CreatedAt:    detail.Store.CreatedAt,
		UpdatedAt:    detail.Store.UpdatedAt,
	}
}

func pgTextPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func pgInt4Ptr(i pgtype.Int4) *int32 {
	if !i.Valid {
		return nil
	}
	return &i.Int32
}

func pgNumericPtr(n pgtype.Numeric) *float64 {
	if !n.Valid {
		return nil
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return nil
	}
	return &f.Float64
}
