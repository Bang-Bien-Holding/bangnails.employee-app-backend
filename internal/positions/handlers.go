package positions

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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

func (h *Handler) CreatePosition(w http.ResponseWriter, r *http.Request) {
	var params createPositionParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	params.Name = strings.TrimSpace(params.Name)

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	position, err := h.service.CreatePosition(r.Context(), params)
	if err != nil {
		if errors.Is(err, ErrPositionNameAlreadyExists) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		slog.Error("positions: create position", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := json.Write(w, http.StatusCreated, newPositionResponse(position)); err != nil {
		slog.Error("positions: write create position response", "error", err)
	}
}

func (h *Handler) ListPositions(w http.ResponseWriter, r *http.Request) {
	positions, err := h.service.ListPositions(r.Context())
	if err != nil {
		slog.Error("positions: list positions", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := json.Write(w, http.StatusOK, newPositionResponses(positions)); err != nil {
		slog.Error("positions: write list positions response", "error", err)
	}
}

func (h *Handler) UpdatePosition(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid position id", http.StatusBadRequest)
		return
	}

	var params updatePositionParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	params.Name = strings.TrimSpace(params.Name)

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	position, err := h.service.UpdatePosition(r.Context(), id, params)
	if err != nil {
		switch {
		case errors.Is(err, ErrPositionNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, ErrPositionNameAlreadyExists):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			slog.Error("positions: update position", "id", id, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}

	if err := json.Write(w, http.StatusOK, newPositionResponse(position)); err != nil {
		slog.Error("positions: write update position response", "error", err)
	}
}

func (h *Handler) DeletePosition(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid position id", http.StatusBadRequest)
		return
	}

	if err := h.service.DeletePosition(r.Context(), id); err != nil {
		if errors.Is(err, ErrPositionNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		slog.Error("positions: delete position", "id", id, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetPositionEmployees handles GET /v1/positions/{id}/employees (ADR-0011) —
// the read half of the position-first membership screen.
func (h *Handler) GetPositionEmployees(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid position id", http.StatusBadRequest)
		return
	}

	details, err := h.service.GetPositionEmployees(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrPositionNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		slog.Error("positions: get position employees", "id", id, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := json.Write(w, http.StatusOK, newEmployeeResponses(details)); err != nil {
		slog.Error("positions: write get position employees response", "error", err)
	}
}

// SetPositionEmployees handles PUT /v1/positions/{id}/employees (ADR-0011) —
// replaces the position's whole employee set via diff, mirroring
// PUT /v1/employees/{id}'s positionIds whole-set replace (ADR-0008) in the
// other direction.
func (h *Handler) SetPositionEmployees(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid position id", http.StatusBadRequest)
		return
	}

	var params setPositionEmployeesParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	details, err := h.service.SetPositionEmployees(r.Context(), id, params)
	if err != nil {
		switch {
		case errors.Is(err, ErrPositionNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, ErrUnknownEmployeeID):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			slog.Error("positions: set position employees", "id", id, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}

	if err := json.Write(w, http.StatusOK, newEmployeeResponses(details)); err != nil {
		slog.Error("positions: write set position employees response", "error", err)
	}
}

// BulkDeletePositions handles DELETE /v1/positions (bulk, issue #13) —
// all-or-nothing, unlike employees.Handler.BulkDeleteEmployees' best-effort
// per-id results (see Service.BulkDeletePositions).
func (h *Handler) BulkDeletePositions(w http.ResponseWriter, r *http.Request) {
	var params bulkDeletePositionsParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.service.BulkDeletePositions(r.Context(), params.IDs); err != nil {
		if errors.Is(err, ErrPositionNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		slog.Error("positions: bulk delete positions", "ids", params.IDs, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
