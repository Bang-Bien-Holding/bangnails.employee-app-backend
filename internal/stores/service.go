package stores

import (
	"context"
	"errors"
	"strconv"
	"sync"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

	ips, err := s.repo.ListStoreWifiIPsByStoreID(ctx, id)
	if err != nil {
		return StoreDetail{}, err
	}
	macs, err := s.repo.ListStoreWifiMacsByStoreID(ctx, id)
	if err != nil {
		return StoreDetail{}, err
	}

	ipAddresses := make([]string, len(ips))
	for i, ip := range ips {
		ipAddresses[i] = ip.String()
	}
	macAddresses := make([]string, len(macs))
	for i, mac := range macs {
		macAddresses[i] = mac.String()
	}

	return StoreDetail{
		Store:        store,
		IPAddresses:  ipAddresses,
		MACAddresses: macAddresses,
	}, nil
}

// SyncStores runs the store-sync workflow: fetch every store from Odoo in a
// single call (the store count is small enough that pagination isn't
// needed), then in one transaction bulk-upsert them and soft-delete any
// local store Odoo no longer reports. Only one call runs at a time; a
// concurrent call is rejected with ErrSyncInProgress rather than queued or
// run in parallel. This endpoint only reconciles the store table — it does
// not touch employees.store_id.
func (s *service) SyncStores(ctx context.Context) (SyncSummary, error) {
	if !s.tryLock() {
		return SyncSummary{}, ErrSyncInProgress
	}
	defer s.unlock()

	odooStores, err := s.odoo.FetchStores(ctx)
	if err != nil {
		return SyncSummary{}, err
	}

	odooStoreIDs := make([]string, len(odooStores))
	storeNames := make([]string, len(odooStores))
	cities := make([]string, len(odooStores))
	for i, st := range odooStores {
		odooStoreIDs[i] = strconv.Itoa(st.ID)
		storeNames[i] = st.Name
		cities[i] = st.City
	}

	var summary SyncSummary
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
			summary.TotalStoresProcessed++
			if row.Inserted {
				summary.InsertedStores++
			} else {
				summary.UpdatedStores++
			}
		}

		staleStoreIDs, err := q.FindStoresNotInOdoo(ctx, odooStoreIDs)
		if err != nil {
			return err
		}
		if len(staleStoreIDs) == 0 {
			return nil
		}

		deleted, err := q.SoftDeleteStores(ctx, staleStoreIDs)
		if err != nil {
			return err
		}
		summary.DeletedStores = int(deleted)
		return nil
	})
	if err != nil {
		return SyncSummary{}, err
	}

	return summary, nil
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
