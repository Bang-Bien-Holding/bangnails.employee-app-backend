package employees

import (
	"errors"
	"log/slog"
	"net/http"

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

func (h *Handler) CreateEmployee(w http.ResponseWriter, r *http.Request) {
	var params createEmployeeParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	employee, err := h.service.CreateEmployee(r.Context(), params)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrEmailAlreadyExists), errors.Is(err, ErrUsernameAlreadyExists), errors.Is(err, ErrOdooEmployeeIDAlreadyExists):
			status = http.StatusConflict
		case errors.Is(err, ErrUnknownPositionID), errors.Is(err, ErrOdooEmployeeIDNotFound):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusCreated, newEmployeeResponse(employee))
}

func (h *Handler) GetEmployeeByID(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParsePathID(w, r, "id", "employee")
	if !ok {
		return
	}

	employee, err := h.service.GetEmployeeByID(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrEmployeeNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusOK, newEmployeeResponse(employee))
}

func (h *Handler) UpdateEmployee(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParsePathID(w, r, "id", "employee")
	if !ok {
		return
	}

	var params updateEmployeeParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	employee, err := h.service.UpdateEmployee(r.Context(), id, params)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrEmployeeNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrEmailAlreadyExists), errors.Is(err, ErrUsernameAlreadyExists), errors.Is(err, ErrOdooEmployeeIDAlreadyExists):
			status = http.StatusConflict
		case errors.Is(err, ErrUnknownPositionID), errors.Is(err, ErrOdooEmployeeIDNotFound):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusOK, newEmployeeResponse(employee))
}

// SetEmployeePassword lets an admin directly assign an employee's new
// password, bypassing the token/email flow in CompleteActivation.
func (h *Handler) SetEmployeePassword(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParsePathID(w, r, "id", "employee")
	if !ok {
		return
	}

	var params setEmployeePasswordParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.service.SetEmployeePassword(r.Context(), id, params.Password); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrEmployeeNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) SetEmployeeActive(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParsePathID(w, r, "id", "employee")
	if !ok {
		return
	}

	var params setEmployeeActiveParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.service.SetEmployeeActive(r.Context(), id, *params.IsActive); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrEmployeeNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteEmployee(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParsePathID(w, r, "id", "employee")
	if !ok {
		return
	}

	if err := h.service.DeleteEmployee(r.Context(), id); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrEmployeeNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) BulkDeleteEmployees(w http.ResponseWriter, r *http.Request) {
	var params bulkDeleteEmployeesParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	results := h.service.BulkDeleteEmployees(r.Context(), params.IDs)

	json.Write(w, http.StatusOK, results)
}

func (h *Handler) BulkSendPasswordResetLinks(w http.ResponseWriter, r *http.Request) {
	var params bulkSendPasswordResetLinksParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	results := h.service.BulkSendPasswordResetLinks(r.Context(), params.IDs)

	json.Write(w, http.StatusOK, results)
}

// CompleteActivation is a public, unauthenticated endpoint — anyone holding
// a valid token (received via email) can call it, no admin session
// required. Serves both first-time activation and an admin-triggered
// password-reset link, since both are the same operation from here on.
func (h *Handler) CompleteActivation(w http.ResponseWriter, r *http.Request) {
	var params completeActivationParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.service.CompleteActivation(r.Context(), params); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrInvalidOrExpiredToken) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListEmployees(w http.ResponseWriter, r *http.Request) {
	employees, err := h.service.ListEmployees(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, newEmployeeResponses(employees))
}

type syncEmployeesResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// SyncEmployees kicks off a background pull from Odoo for the employees
// matching the given internal ids and returns immediately — it does not
// wait for the sync to finish. The frontend polls SyncStatus to know when
// it's done.
func (h *Handler) SyncEmployees(w http.ResponseWriter, r *http.Request) {
	var params syncEmployeesParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.service.SyncEmployees(r.Context(), params.IDs); err != nil {
		if errors.Is(err, ErrSyncInProgress) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		slog.Error("employees: sync employees", "error", err)
		http.Error(w, "failed to start employee sync", http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusAccepted, syncEmployeesResponse{
		Status:  "accepted",
		Message: "Employee sync started.",
	})
}

// SyncStatus reports whether a SyncEmployees job is still running, for the
// frontend to poll while its trigger button is disabled.
func (h *Handler) SyncStatus(w http.ResponseWriter, r *http.Request) {
	json.Write(w, http.StatusOK, h.service.SyncStatus(r.Context()))
}
