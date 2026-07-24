package stores

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/httpx"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/json"
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
	Status  string `json:"status"`
	Message string `json:"message"`
}

// SyncStores kicks off a background pull of every store from Odoo and
// returns immediately — it does not wait for the sync to finish. The
// frontend polls SyncStatus to know when it's done.
func (h *Handler) SyncStores(w http.ResponseWriter, r *http.Request) {
	if err := h.service.SyncStores(r.Context()); err != nil {
		if errors.Is(err, ErrSyncInProgress) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		slog.Error("stores: sync stores", "error", err)
		http.Error(w, "failed to start store sync", http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusAccepted, syncStoresResponse{
		Status:  "accepted",
		Message: "Store sync started.",
	})
}

// SyncStatus reports whether a SyncStores job is still running, for the
// frontend to poll while its trigger button is disabled.
func (h *Handler) SyncStatus(w http.ResponseWriter, r *http.Request) {
	json.Write(w, http.StatusOK, h.service.SyncStatus(r.Context()))
}

func (h *Handler) GetStoreByID(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParsePathID(w, r, "id", "store")
	if !ok {
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

// ListStores handles GET /v1/stores' optional search/filter query
// parameters (issues #32/#33/#34): store_name, city, wifi_whitelist_enabled,
// and odoo_store_ids — see parseListStoresFilter for how each is read.
func (h *Handler) ListStores(w http.ResponseWriter, r *http.Request) {
	filter, err := parseListStoresFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	details, err := h.service.ListStores(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, newStoreResponses(details))
}

// parseListStoresFilter reads ListStores' query parameters into a
// ListStoresFilter. Every field defaults to "don't filter on this facet"
// when its query parameter is absent or empty — the only failure mode is
// wifi_whitelist_enabled holding something ParseBool doesn't recognize
// (issue #33). odoo_store_ids (issue #34) has no such failure mode: it's a
// plain comma-separated string list, every element valid by construction,
// unlike employees' numeric id-list params.
func parseListStoresFilter(r *http.Request) (ListStoresFilter, error) {
	var filter ListStoresFilter

	if storeName := r.URL.Query().Get("store_name"); storeName != "" {
		filter.StoreName = &storeName
	}
	if city := r.URL.Query().Get("city"); city != "" {
		filter.City = &city
	}
	if raw := r.URL.Query().Get("wifi_whitelist_enabled"); raw != "" {
		wifiWhitelistEnabled, err := strconv.ParseBool(raw)
		if err != nil {
			return ListStoresFilter{}, fmt.Errorf("invalid wifi_whitelist_enabled: %q is not a valid boolean", raw)
		}
		filter.WifiWhitelistEnabled = &wifiWhitelistEnabled
	}
	filter.OdooStoreIDs = parseStringListQueryParam(r, "odoo_store_ids")

	return filter, nil
}

// parseStringListQueryParam parses name's comma-separated query parameter
// (e.g. "odoo_store_ids=A1,A2,A3") into a []string. Returns nil when the
// parameter is absent or empty, so the caller can tell "not filtering on
// this facet" apart from "filter to nothing" further down — mirrors
// employees.parseInt64ListQueryParam, minus the per-element numeric parse
// (and its failure mode) since every string is already valid. Empty
// elements (a double comma or a trailing comma) are dropped rather than
// kept as "" — they can never match a real odoo_store_id, so keeping them
// would just be dead weight in the filter list.
func parseStringListQueryParam(r *http.Request, name string) []string {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func (h *Handler) PatchStore(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParsePathID(w, r, "id", "store")
	if !ok {
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
	id, ok := httpx.ParsePathID(w, r, "id", "store")
	if !ok {
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
	id, ok := httpx.ParsePathID(w, r, "id", "store")
	if !ok {
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
