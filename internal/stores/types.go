package stores

//go:generate mockgen -source=types.go -destination=service_mock_test.go -package=stores

import (
	"context"
	"errors"
)

// ErrSyncInProgress is returned when SyncStores is called while a previous
// call is still running — Phase 1's concurrency guard.
var ErrSyncInProgress = errors.New("store sync already in progress")

// SyncSummary reports the outcome of one SyncStores run.
type SyncSummary struct {
	TotalStoresProcessed int `json:"total_stores_processed"`
	InsertedStores       int `json:"inserted_stores"`
	UpdatedStores        int `json:"updated_stores"`
	DeletedStores        int `json:"deleted_stores"`
	Failed               int `json:"failed"`
}

type Service interface {
	SyncStores(ctx context.Context) (SyncSummary, error)
}
