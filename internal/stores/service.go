package stores

import (
	"context"
	"strconv"
	"sync"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fetchBatchSize is how many stores SyncStores requests from Odoo per page,
// per the spec's Phase 3.
const fetchBatchSize = 100

type service struct {
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

// SyncStores runs the full store-sync workflow: fetch every store page from
// Odoo, bulk-upsert each page and sync its employee assignments, then
// soft-delete any local store Odoo no longer reports. Only one call runs at
// a time; a concurrent call is rejected with ErrSyncInProgress rather than
// queued or run in parallel.
func (s *service) SyncStores(ctx context.Context) (SyncSummary, error) {
	if !s.tryLock() {
		return SyncSummary{}, ErrSyncInProgress
	}
	defer s.unlock()

	var summary SyncSummary
	activeOdooStoreIDs := []string{}

	for offset := 0; ; offset += fetchBatchSize {
		batch, err := s.odoo.FetchStores(ctx, fetchBatchSize, offset)
		if err != nil {
			return SyncSummary{}, err
		}
		if len(batch) == 0 {
			break
		}

		odooStoreIDs := make([]string, len(batch))
		for i, st := range batch {
			odooStoreIDs[i] = strconv.Itoa(st.ID)
		}

		if err := s.syncBatch(ctx, batch, odooStoreIDs, &summary); err != nil {
			return SyncSummary{}, err
		}

		activeOdooStoreIDs = append(activeOdooStoreIDs, odooStoreIDs...)
	}

	if err := s.deleteStoresNotInOdoo(ctx, activeOdooStoreIDs, &summary); err != nil {
		return SyncSummary{}, err
	}

	return summary, nil
}

// syncBatch upserts one page of Odoo stores and, for each, reconciles which
// employees are assigned to it — Phase 4.
func (s *service) syncBatch(ctx context.Context, batch []odoo.Store, odooStoreIDs []string, summary *SyncSummary) error {
	storeNames := make([]string, len(batch))
	cities := make([]string, len(batch))
	for i, st := range batch {
		storeNames[i] = st.Name
		cities[i] = st.City
	}

	return s.withTx(ctx, func(q repo.Querier) error {
		rows, err := q.UpsertStores(ctx, repo.UpsertStoresParams{
			OdooStoreIds: odooStoreIDs,
			StoreNames:   storeNames,
			Cities:       cities,
		})
		if err != nil {
			return err
		}

		storeIDByOdooID := make(map[string]int64, len(rows))
		for _, row := range rows {
			summary.TotalStoresProcessed++
			if row.Inserted {
				summary.InsertedStores++
			} else {
				summary.UpdatedStores++
			}
			storeIDByOdooID[row.OdooStoreID.String] = row.ID
		}

		for _, st := range batch {
			storeID, ok := storeIDByOdooID[strconv.Itoa(st.ID)]
			if !ok {
				continue
			}

			odooUserIDs := make([]string, len(st.OdooUserIDs))
			for i, uid := range st.OdooUserIDs {
				odooUserIDs[i] = strconv.Itoa(uid)
			}

			if err := q.ClearStoreAssignmentsNotInOdoo(ctx, repo.ClearStoreAssignmentsNotInOdooParams{
				StoreID:         pgInt8(storeID),
				KeepEmployeeIds: odooUserIDs,
			}); err != nil {
				return err
			}

			if err := q.AssignEmployeesToStore(ctx, repo.AssignEmployeesToStoreParams{
				StoreID:           pgInt8(storeID),
				AssignEmployeeIds: odooUserIDs,
			}); err != nil {
				return err
			}
		}

		return nil
	})
}

// deleteStoresNotInOdoo runs once, after the fetch loop ends — Phase 5:
// local stores Odoo no longer reports get their employees unassigned and
// are soft-deleted (is_active = false).
func (s *service) deleteStoresNotInOdoo(ctx context.Context, activeOdooStoreIDs []string, summary *SyncSummary) error {
	return s.withTx(ctx, func(q repo.Querier) error {
		staleStoreIDs, err := q.FindStoresNotInOdoo(ctx, activeOdooStoreIDs)
		if err != nil {
			return err
		}
		if len(staleStoreIDs) == 0 {
			return nil
		}

		if err := q.ClearEmployeeAssignmentsForStores(ctx, staleStoreIDs); err != nil {
			return err
		}

		deleted, err := q.SoftDeleteStores(ctx, staleStoreIDs)
		if err != nil {
			return err
		}
		summary.DeletedStores = int(deleted)
		return nil
	})
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

func pgInt8(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}
