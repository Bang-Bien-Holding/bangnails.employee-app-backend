package stores

import (
	"errors"
	"net/http"

	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/json"
)

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
