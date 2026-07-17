package positions

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

func (h *Handler) CreatePosition(w http.ResponseWriter, r *http.Request) {
	var params createPositionParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	position, err := h.service.CreatePosition(r.Context(), params)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrPositionNameAlreadyExists) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusCreated, newPositionResponse(position))
}

func (h *Handler) ListPositions(w http.ResponseWriter, r *http.Request) {
	positions, err := h.service.ListPositions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, newPositionResponses(positions))
}

func (h *Handler) UpdatePosition(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid position id", http.StatusBadRequest)
		return
	}

	var params updatePositionParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	position, err := h.service.UpdatePosition(r.Context(), id, params)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrPositionNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrPositionNameAlreadyExists):
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusOK, newPositionResponse(position))
}

func (h *Handler) DeletePosition(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid position id", http.StatusBadRequest)
		return
	}

	if err := h.service.DeletePosition(r.Context(), id); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrPositionNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
