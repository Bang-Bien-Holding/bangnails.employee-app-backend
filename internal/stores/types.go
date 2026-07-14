package stores

//go:generate mockgen -source=types.go -destination=service_mock_test.go -package=stores

import (
	"context"
	"errors"
	"time"

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

// ErrStoreConflict is returned by UpdateStore when the caller's UpdatedAt no
// longer matches the store's current updated_at — another admin edited this
// store (wifi whitelist or geofence) since the caller last fetched it. The
// caller must re-fetch and redo its edit against the latest state.
var ErrStoreConflict = errors.New("store was modified since it was last fetched")

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

// patchStoreParams is the body for PATCH /v1/stores/{id}. UpdatedAt is
// required on every request — it's the optimistic-concurrency token the
// caller last saw from GetStoreByID, checked against the store's current
// updated_at before anything is changed (see ErrStoreConflict).
//
// Latitude/Longitude/RadiusMeters are pointers so "not sent" (nil, leave the
// geofence untouched) is distinguishable from an explicit zero value (e.g.
// latitude 0 is a real point on the equator). The three are validated as an
// all-or-nothing group via required_with: each names the other two, so
// submitting exactly one or two of them fails validation, while submitting
// all three or none of them passes.
//
// IPAddresses/MACAddresses need no pointer for the same "omitted vs.
// explicit-empty" distinction: encoding/json already leaves a plain slice
// nil when the field is absent from the body and non-nil (even if empty)
// when it's present, including as "[]" — unlike a scalar, a slice's zero
// value (nil) is never itself a value the caller could have meant, so nil
// unambiguously means "field omitted, leave this whitelist untouched",
// while any non-nil slice (including an empty one) replaces the store's
// entire whitelist for that list to match exactly what's submitted (see
// UpdateStore). "unique" rejects a value repeated within the same array
// rather than silently deduping it; "dive" then validates each element's
// address format.
type patchStoreParams struct {
	UpdatedAt    time.Time `json:"updated_at" validate:"required"`
	Latitude     *float64  `json:"latitude" validate:"required_with=Longitude RadiusMeters,omitempty,min=-90,max=90"`
	Longitude    *float64  `json:"longitude" validate:"required_with=Latitude RadiusMeters,omitempty,min=-180,max=180"`
	RadiusMeters *int32    `json:"radius_meters" validate:"required_with=Latitude Longitude,omitempty,min=1,max=1000"`
	IPAddresses  []string  `json:"ip_addresses" validate:"omitempty,unique,dive,ipv4"`
	MACAddresses []string  `json:"mac_addresses" validate:"omitempty,unique,dive,mac"`
}

type Service interface {
	SyncStores(ctx context.Context) (SyncSummary, error)
	GetStoreByID(ctx context.Context, id int64) (StoreDetail, error)
	UpdateStore(ctx context.Context, id int64, params patchStoreParams) (StoreDetail, error)
	ListStores(ctx context.Context) ([]StoreDetail, error)
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

// newStoreResponses maps ListStores' result to the JSON array GET /v1/stores
// returns, one element per store in the same shape newStoreResponse builds
// for a single store.
func newStoreResponses(details []StoreDetail) []storeResponse {
	responses := make([]storeResponse, len(details))
	for i, detail := range details {
		responses[i] = newStoreResponse(detail)
	}
	return responses
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
