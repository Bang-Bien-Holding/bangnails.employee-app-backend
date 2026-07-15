package stores

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/json"
	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
)

var validate = validator.New()

type Handler struct {
	service Service
}

func NewHandler(service Service) *Handler {
	return &Handler{service: service}
}

type syncStoresResponse struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Meta    SyncSummary `json:"meta"`
}

func (h *Handler) SyncStores(w http.ResponseWriter, r *http.Request) {
	summary, err := h.service.SyncStores(r.Context())
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrSyncInProgress) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusCreated, syncStoresResponse{
		Status:  "success",
		Message: "Store synchronization and user assignments updated successfully.",
		Meta:    summary,
	})
}

func (h *Handler) GetStoreByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid store id", http.StatusBadRequest)
		return
	}

	detail, err := h.service.GetStoreByID(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrStoreNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusOK, newStoreResponse(detail))
}

func (h *Handler) ListStores(w http.ResponseWriter, r *http.Request) {
	details, err := h.service.ListStores(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, newStoreResponses(details))
}

func (h *Handler) PatchStore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid store id", http.StatusBadRequest)
		return
	}

	var params patchStoreParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	detail, err := h.service.UpdateStore(r.Context(), id, params)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrStoreNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrStoreConflict):
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusOK, newStoreResponse(detail))
}

func (h *Handler) DeleteWifiWhitelistEntries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid store id", http.StatusBadRequest)
		return
	}

	var params deleteWifiWhitelistParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	if params.isEmpty() {
		http.Error(w, "at least one of ip_addresses or mac_addresses is required", http.StatusBadRequest)
		return
	}

	results, err := h.service.DeleteWifiWhitelistEntries(r.Context(), id, params)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrStoreNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrStoreConflict):
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusOK, results)
}

// SetStoreWifiWhitelistEnabled handles PATCH
// /v1/stores/{id}/wifi-whitelist-enabled — the list screen's per-row
// Activate/Deactivate toggle (see ADR-0006).
func (h *Handler) SetStoreWifiWhitelistEnabled(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid store id", http.StatusBadRequest)
		return
	}

	var params setWifiWhitelistEnabledParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	result, err := h.service.SetStoreWifiWhitelistEnabled(r.Context(), id, params)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrStoreNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrStoreConflict):
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusOK, newStoreToggleResponse(result))
}

// BulkSetWifiWhitelistEnabled handles the collection-level PATCH /v1/stores —
// the list screen's "deactivate all" (or any explicit multi-select) action
// (see ADR-0006). Atomic and all-or-nothing: any unknown id or stale
// updated_at in the batch fails the whole request with 409 and
// {"failed_ids": [...]}, rather than DeleteWifiWhitelistEntries' best-effort,
// partial-application model.
func (h *Handler) BulkSetWifiWhitelistEnabled(w http.ResponseWriter, r *http.Request) {
	var params bulkSetWifiWhitelistEnabledParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	results, err := h.service.BulkSetWifiWhitelistEnabled(r.Context(), params)
	if err != nil {
		var conflictErr *BulkWifiWhitelistConflictError
		if errors.As(err, &conflictErr) {
			json.Write(w, http.StatusConflict, bulkWifiWhitelistConflictResponse{FailedIDs: conflictErr.FailedIDs})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, newStoreToggleResponses(results))
}
