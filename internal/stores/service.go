package stores

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// storeSyncTimeout bounds SyncStores' detached goroutine so a stalled Odoo
// or database call can't leave s.syncing stuck true indefinitely — mirrors
// employees.employeeSyncTimeout.
const storeSyncTimeout = 5 * time.Minute

type service struct {
	// repo is a plain, non-transactional Querier for reads that don't need
	// transaction scoping — GetStoreByID uses this rather than withTx.
	repo repo.Querier
	// withTx wraps fn in a transaction-scoped repo.Querier — a real
	// pool-backed implementation is installed by NewService; tests replace
	// it with a stub that calls fn against a mocked Querier directly, so
	// SyncStores' orchestration is testable without a live Postgres.
	withTx func(ctx context.Context, fn func(repo.Querier) error) error
	odoo   odoo.Client

	mu      sync.Mutex
	syncing bool
}

func NewService(pool *pgxpool.Pool, odooClient odoo.Client) Service {
	return &service{
		repo: repo.New(pool),
		withTx: func(ctx context.Context, fn func(repo.Querier) error) error {
			tx, err := pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)

			if err := fn(repo.New(tx)); err != nil {
				return err
			}
			return tx.Commit(ctx)
		},
		odoo: odooClient,
	}
}

// GetStoreByID returns one store's details together with its current wifi
// whitelist. IPAddresses/MACAddresses are always non-nil slices — a store
// with no entries yet gets an empty slice, not nil, so callers (and the
// eventual JSON response) never have to distinguish "no data" from "null".
func (s *service) GetStoreByID(ctx context.Context, id int64) (StoreDetail, error) {
	store, err := s.repo.GetStoreByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StoreDetail{}, ErrStoreNotFound
		}
		return StoreDetail{}, err
	}

	return buildStoreDetail(ctx, s.repo, store)
}

// UpdateStore applies params' geofence group (if present — all three or
// none, enforced by patchStoreParams' validation tags), and always bumps
// store.updated_at, gated by params.UpdatedAt matching the store's current
// updated_at (see ErrStoreConflict). wifi_whitelist_enabled is not settable
// here — see ADR-0006, it moved to its own dedicated endpoints. Runs inside
// withTx even though today it only issues one write — the same transaction
// ticket 03 extends to also replace the wifi whitelist tables atomically
// alongside this update.
func (s *service) UpdateStore(ctx context.Context, id int64, params patchStoreParams) (StoreDetail, error) {
	var detail StoreDetail
	err := s.withTx(ctx, func(q repo.Querier) error {
		latitude, err := float64PtrToNumeric(params.Latitude)
		if err != nil {
			return err
		}
		longitude, err := float64PtrToNumeric(params.Longitude)
		if err != nil {
			return err
		}

		store, err := q.UpdateStoreGeofence(ctx, repo.UpdateStoreGeofenceParams{
			ID:                id,
			Latitude:          latitude,
			Longitude:         longitude,
			RadiusMeters:      int32PtrToInt4(params.RadiusMeters),
			ExpectedUpdatedAt: pgtype.Timestamptz{Time: params.UpdatedAt, Valid: true},
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// No row matched id + updated_at together — find out which of
			// those failed: if the store doesn't exist it's a 404, otherwise
			// the row exists and updated_at was stale.
			_, existsErr := q.GetStoreByID(ctx, id)
			switch {
			case errors.Is(existsErr, pgx.ErrNoRows):
				return ErrStoreNotFound
			case existsErr != nil:
				return existsErr
			default:
				return ErrStoreConflict
			}
		}

		if params.IPAddresses != nil {
			ips, err := parseAddresses(params.IPAddresses, netip.ParseAddr)
			if err != nil {
				return err
			}
			if err := q.DeleteStoreWifiIPsNotIn(ctx, repo.DeleteStoreWifiIPsNotInParams{
				StoreID: id, IpAddresses: ips,
			}); err != nil {
				return err
			}
			if err := q.InsertStoreWifiIPs(ctx, repo.InsertStoreWifiIPsParams{
				StoreID: id, IpAddresses: ips,
			}); err != nil {
				return err
			}
		}

		if params.MACAddresses != nil {
			macs, err := parseAddresses(params.MACAddresses, net.ParseMAC)
			if err != nil {
				return err
			}
			if err := q.DeleteStoreWifiMacsNotIn(ctx, repo.DeleteStoreWifiMacsNotInParams{
				StoreID: id, MacAddresses: macs,
			}); err != nil {
				return err
			}
			if err := q.InsertStoreWifiMacs(ctx, repo.InsertStoreWifiMacsParams{
				StoreID: id, MacAddresses: macs,
			}); err != nil {
				return err
			}
		}

		detail, err = buildStoreDetail(ctx, q, store)
		return err
	})
	if err != nil {
		return StoreDetail{}, err
	}
	return detail, nil
}

// buildStoreDetail fills in a StoreDetail's wifi whitelist for an
// already-fetched store row — shared by GetStoreByID (via s.repo) and
// UpdateStore (via the transaction-scoped q) so both read the whitelist the
// same way.
func buildStoreDetail(ctx context.Context, q repo.Querier, store repo.Store) (StoreDetail, error) {
	ips, err := q.ListStoreWifiIPsByStoreID(ctx, store.ID)
	if err != nil {
		return StoreDetail{}, err
	}
	macs, err := q.ListStoreWifiMacsByStoreID(ctx, store.ID)
	if err != nil {
		return StoreDetail{}, err
	}

	return StoreDetail{
		Store:        store,
		IPAddresses:  stringifyAddresses(ips),
		MACAddresses: stringifyAddresses(macs),
	}, nil
}

// ListStores returns every store — wifi-enabled and wifi-disabled, since the list
// screen's Activate toggle needs to see and re-enable a deactivated one —
// together with its current wifi whitelist. See the ListStores query in
// queries.sql for how ordering and whitelist aggregation are done in one
// round trip, unlike GetStoreByID/buildStoreDetail's per-store follow-up
// queries.
func (s *service) ListStores(ctx context.Context) ([]StoreDetail, error) {
	rows, err := s.repo.ListStores(ctx)
	if err != nil {
		return nil, err
	}

	details := make([]StoreDetail, len(rows))
	for i, row := range rows {
		details[i] = StoreDetail{
			Store:        row.Store,
			IPAddresses:  stringifyAddresses(row.IpAddresses),
			MACAddresses: stringifyAddresses(row.MacAddresses),
		}
	}
	return details, nil
}

// DeleteWifiWhitelistEntries removes specific IP and/or MAC values from one
// store's whitelist, by value rather than internal id (see ADR-0003),
// best-effort per entry: a submitted value not currently in the whitelist is
// reported as a failed result rather than blocking or rolling back the rest
// of the batch. store.updated_at only bumps when at least one entry was
// actually deleted, so — unlike UpdateStore's single CAS UPDATE — the
// updated_at match can't be checked and applied in the same statement: it's
// verified up front against the already-fetched store row, then, only if a
// delete actually happened, re-applied atomically via UpdateStoreGeofence
// (its geofence args left invalid so only updated_at changes) to
// close the race window between the initial check and here.
func (s *service) DeleteWifiWhitelistEntries(ctx context.Context, id int64, params deleteWifiWhitelistParams) ([]WifiWhitelistDeleteResult, error) {
	var results []WifiWhitelistDeleteResult
	err := s.withTx(ctx, func(q repo.Querier) error {
		store, err := q.GetStoreByID(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrStoreNotFound
			}
			return err
		}
		if !store.UpdatedAt.Time.Equal(params.UpdatedAt) {
			return ErrStoreConflict
		}

		results = make([]WifiWhitelistDeleteResult, 0, len(params.IPAddresses)+len(params.MACAddresses))
		deletedAny := false

		if len(params.IPAddresses) > 0 {
			ips, err := parseAddresses(params.IPAddresses, netip.ParseAddr)
			if err != nil {
				return err
			}
			deleted, err := q.DeleteStoreWifiIPsByValue(ctx, repo.DeleteStoreWifiIPsByValueParams{
				StoreID: id, IpAddresses: ips,
			})
			if err != nil {
				return err
			}
			found := make(map[netip.Addr]bool, len(deleted))
			for _, d := range deleted {
				found[d] = true
			}
			for i, raw := range params.IPAddresses {
				ok := found[ips[i]]
				deletedAny = deletedAny || ok
				results = append(results, newWifiWhitelistDeleteResult(raw, "ip", ok))
			}
		}

		if len(params.MACAddresses) > 0 {
			macs, err := parseAddresses(params.MACAddresses, net.ParseMAC)
			if err != nil {
				return err
			}
			deleted, err := q.DeleteStoreWifiMacsByValue(ctx, repo.DeleteStoreWifiMacsByValueParams{
				StoreID: id, MacAddresses: macs,
			})
			if err != nil {
				return err
			}
			found := make(map[string]bool, len(deleted))
			for _, d := range deleted {
				found[d.String()] = true
			}
			for i, raw := range params.MACAddresses {
				ok := found[macs[i].String()]
				deletedAny = deletedAny || ok
				results = append(results, newWifiWhitelistDeleteResult(raw, "mac", ok))
			}
		}

		if !deletedAny {
			return nil
		}

		if _, err := q.UpdateStoreGeofence(ctx, repo.UpdateStoreGeofenceParams{
			ID:                id,
			ExpectedUpdatedAt: store.UpdatedAt,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrStoreConflict
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// newWifiWhitelistDeleteResult builds one DeleteWifiWhitelistEntries result
// entry, echoing back the value exactly as submitted (not re-formatted)
// alongside the fixed "not found in whitelist" error a best-effort miss
// reports.
func newWifiWhitelistDeleteResult(value, addrType string, success bool) WifiWhitelistDeleteResult {
	result := WifiWhitelistDeleteResult{Value: value, Type: addrType, Success: success}
	if !success {
		result.Error = "not found in whitelist"
	}
	return result
}

// SetStoreWifiWhitelistEnabled sets one store's wifi_whitelist_enabled flag —
// the list screen's per-row Activate/Deactivate toggle (see ADR-0006),
// separate from UpdateStore so the caller can flip one boolean without the
// weight of the full-store PATCH. Optimistic-locked the same way as
// UpdateStore: a mismatched params.UpdatedAt updates nothing and surfaces as
// ErrStoreConflict, disambiguated from ErrStoreNotFound via a follow-up
// GetStoreByID.
func (s *service) SetStoreWifiWhitelistEnabled(ctx context.Context, id int64, params setWifiWhitelistEnabledParams) (StoreWifiToggleResult, error) {
	var result StoreWifiToggleResult
	err := s.withTx(ctx, func(q repo.Querier) error {
		row, err := q.SetStoreWifiWhitelistEnabled(ctx, repo.SetStoreWifiWhitelistEnabledParams{
			ID:                   id,
			WifiWhitelistEnabled: *params.WifiWhitelistEnabled,
			ExpectedUpdatedAt:    pgtype.Timestamptz{Time: params.UpdatedAt, Valid: true},
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			_, existsErr := q.GetStoreByID(ctx, id)
			switch {
			case errors.Is(existsErr, pgx.ErrNoRows):
				return ErrStoreNotFound
			case existsErr != nil:
				return existsErr
			default:
				return ErrStoreConflict
			}
		}
		result = StoreWifiToggleResult{
			ID:                   row.ID,
			WifiWhitelistEnabled: *params.WifiWhitelistEnabled,
			UpdatedAt:            row.UpdatedAt,
		}
		return nil
	})
	if err != nil {
		return StoreWifiToggleResult{}, err
	}
	return result, nil
}

// BulkSetWifiWhitelistEnabled atomically sets wifi_whitelist_enabled on every
// store in params.Stores — the list screen's "deactivate all" (or any
// explicit multi-select) action (see ADR-0006). All-or-nothing: inside one
// transaction, GetStoresByIDsForUpdate fetches every submitted id's current
// (id, updated_at) and locks those rows (FOR UPDATE), which this function
// compares against the caller's submitted pairs — any missing id or any
// updated_at mismatch aborts before the UPDATE runs, with every
// missing/mismatched id collected into BulkWifiWhitelistConflictError.
// Only if every id matched does BulkSetStoreWifiWhitelistEnabled apply the
// write and return fresh state for every store.
func (s *service) BulkSetWifiWhitelistEnabled(ctx context.Context, params bulkSetWifiWhitelistEnabledParams) ([]StoreWifiToggleResult, error) {
	submitted := make(map[int64]time.Time, len(params.Stores))
	ids := make([]int64, len(params.Stores))
	for i, store := range params.Stores {
		submitted[store.ID] = store.UpdatedAt
		ids[i] = store.ID
	}

	var results []StoreWifiToggleResult
	err := s.withTx(ctx, func(q repo.Querier) error {
		current, err := q.GetStoresByIDsForUpdate(ctx, ids)
		if err != nil {
			return err
		}

		found := make(map[int64]bool, len(current))
		var failedIDs []int64
		for _, row := range current {
			found[row.ID] = true
			if !row.UpdatedAt.Time.Equal(submitted[row.ID]) {
				failedIDs = append(failedIDs, row.ID)
			}
		}
		for id := range submitted {
			if !found[id] {
				failedIDs = append(failedIDs, id)
			}
		}
		if len(failedIDs) > 0 {
			return &BulkWifiWhitelistConflictError{FailedIDs: failedIDs}
		}

		rows, err := q.BulkSetStoreWifiWhitelistEnabled(ctx, repo.BulkSetStoreWifiWhitelistEnabledParams{
			WifiWhitelistEnabled: *params.WifiWhitelistEnabled,
			StoreIds:             ids,
		})
		if err != nil {
			return err
		}
		results = make([]StoreWifiToggleResult, len(rows))
		for i, row := range rows {
			results[i] = StoreWifiToggleResult{
				ID:                   row.ID,
				WifiWhitelistEnabled: row.WifiWhitelistEnabled,
				UpdatedAt:            row.UpdatedAt,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// stringifyAddresses formats a list of typed addresses (netip.Addr or
// net.HardwareAddr) as strings, always returning a non-nil slice — a store
// with no entries gets [], not nil, so StoreDetail's caller (and the
// eventual JSON response) never has to distinguish "no data" from "null".
func stringifyAddresses[T fmt.Stringer](addrs []T) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

// float64PtrToNumeric converts an optional request field to the nullable
// pgtype.Numeric UpdateStoreGeofence expects: nil means "leave the column
// unchanged" (its SQL COALESCEs over a NULL/invalid arg), not "clear it".
func float64PtrToNumeric(f *float64) (pgtype.Numeric, error) {
	if f == nil {
		return pgtype.Numeric{}, nil
	}
	var n pgtype.Numeric
	// Numeric.Scan only accepts a string or nil — 'f' formatting avoids
	// scientific notation, which ScanScientific/Scan can't parse back.
	if err := n.Scan(strconv.FormatFloat(*f, 'f', -1, 64)); err != nil {
		return pgtype.Numeric{}, err
	}
	return n, nil
}

// int32PtrToInt4 converts an optional request field to the nullable
// pgtype.Int4 UpdateStoreGeofence expects — see float64PtrToNumeric.
func int32PtrToInt4(i *int32) pgtype.Int4 {
	if i == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *i, Valid: true}
}

// parseAddresses converts patchStoreParams.IPAddresses/MACAddresses'
// already-validated (validate:"dive,ipv4|mac") strings to the typed values
// the replace queries expect, via netip.ParseAddr or net.ParseMAC.
func parseAddresses[T any](values []string, parse func(string) (T, error)) ([]T, error) {
	parsed := make([]T, len(values))
	for i, v := range values {
		p, err := parse(v)
		if err != nil {
			return nil, err
		}
		parsed[i] = p
	}
	return parsed, nil
}

// SyncStores kicks off the store-sync workflow in a detached goroutine and
// returns immediately (Step 3 of employees.SyncEmployees' pattern, applied
// here) — the caller doesn't wait on Odoo/DB latency. Unlike SyncEmployees,
// there's no per-request lookup to do first (Store Sync always covers every
// store, never a caller-chosen subset), so there's no failure path between
// acquiring the lock and starting the goroutine. Only one sync runs at a
// time; a concurrent call is rejected with ErrSyncInProgress rather than
// queued or run in parallel.
func (s *service) SyncStores(ctx context.Context) error {
	if !s.tryLock() {
		return ErrSyncInProgress
	}

	// Detached from ctx: the HTTP handler's request context is canceled the
	// moment it returns, which would race with (and likely abort) this
	// goroutine if it inherited that cancellation. Still bounded by
	// storeSyncTimeout so a stalled Odoo/database call can't hold s.syncing
	// true forever; runSync owns cancel and releases it when it returns.
	syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeSyncTimeout)
	go s.runSync(syncCtx, cancel)

	return nil
}

// SyncStatus reports whether a background sync started by SyncStores is
// still running, so the frontend can poll it to keep its trigger button
// disabled for the duration.
func (s *service) SyncStatus(ctx context.Context) SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SyncStatus{Syncing: s.syncing}
}

// runSync does the actual work: fetch every store from Odoo in a single call
// (the store count is small enough that pagination isn't needed), then in
// one transaction bulk-upsert them and hard-delete any local store Odoo no
// longer reports (see ADR-0005) — store_wifi_ip/store_wifi_mac cascade and
// employees.store_id is nulled automatically via DB constraints, no
// application-level query needed for either. It logs the outcome since
// nothing else observes this goroutine once SyncStores has already returned
// to its caller.
func (s *service) runSync(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	defer s.unlock()

	odooStores, err := s.odoo.FetchStores(ctx)
	if err != nil {
		slog.Error("stores: sync fetch from odoo", "error", err)
		return
	}

	odooStoreIDs := make([]string, len(odooStores))
	storeNames := make([]string, len(odooStores))
	cities := make([]string, len(odooStores))
	for i, st := range odooStores {
		odooStoreIDs[i] = strconv.Itoa(st.ID)
		storeNames[i] = st.Name
		cities[i] = st.City
	}

	var inserted, updated, deleted int
	err = s.withTx(ctx, func(q repo.Querier) error {
		rows, err := q.UpsertStores(ctx, repo.UpsertStoresParams{
			OdooStoreIds: odooStoreIDs,
			StoreNames:   storeNames,
			Cities:       cities,
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Inserted {
				inserted++
			} else {
				updated++
			}
		}

		staleStoreIDs, err := q.FindStoresNotInOdoo(ctx, odooStoreIDs)
		if err != nil {
			return err
		}
		if len(staleStoreIDs) == 0 {
			return nil
		}

		deletedCount, err := q.DeleteStores(ctx, staleStoreIDs)
		if err != nil {
			return err
		}
		deleted = int(deletedCount)
		return nil
	})
	if err != nil {
		slog.Error("stores: sync upsert/delete", "error", err)
		return
	}

	slog.Info("stores: sync completed", "inserted", inserted, "updated", updated, "deleted", deleted)
}

func (s *service) tryLock() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.syncing {
		return false
	}
	s.syncing = true
	return true
}

func (s *service) unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncing = false
}
