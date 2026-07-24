package employees

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/httpx"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/json"
	"github.com/go-chi/chi/v5/middleware"
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

// requestPasswordResetResponse is the one and only body
// RequestPasswordReset ever returns, on every request regardless of
// outcome — see requestPasswordResetGenericMessage.
type requestPasswordResetResponse struct {
	Message string `json:"message"`
}

// requestPasswordResetGenericMessage is deliberately identical for every
// case RequestPasswordReset's service call can branch into — unknown email,
// inactive employee, pending-activation employee, or active employee — so
// none of those cases is distinguishable from the HTTP response (issue #38,
// #36's anti-enumeration requirement).
const requestPasswordResetGenericMessage = "If an account with that email exists, we've sent instructions."

// RequestPasswordReset is a public, unauthenticated endpoint — any caller
// can submit any email, no admin session required. It always responds
// 200 OK with the same generic message; the exceptions are a syntactically
// malformed email, rejected below by validate.Struct before the service is
// ever called, and an undeterminable client IP, both pure
// input/infrastructure conditions that reveal nothing about account
// existence (unlike the rate limiting from issue #39, which the service
// layer enforces with no observable difference in this response). The IP
// itself comes from middleware.GetClientIPAddr, not the body — same
// ADR-0013 pattern as auth.Handler.Login.
func (h *Handler) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var params requestPasswordResetParams
	if err := json.Read(w, r, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validate.Struct(params); err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	clientIP := middleware.GetClientIPAddr(r.Context())
	if !clientIP.IsValid() {
		http.Error(w, "could not determine client IP", http.StatusInternalServerError)
		return
	}

	h.service.RequestPasswordReset(r.Context(), params.Email, clientIP)

	json.Write(w, http.StatusOK, requestPasswordResetResponse{Message: requestPasswordResetGenericMessage})
}

// ListEmployees handles GET /employees' optional search/filter query
// parameters (issue #28): q, position_ids, store_ids, odoo_employee_ids,
// is_active — see parseListEmployeesFilter for how each is read.
func (h *Handler) ListEmployees(w http.ResponseWriter, r *http.Request) {
	filter, err := parseListEmployeesFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	employees, err := h.service.ListEmployees(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, newEmployeeResponses(employees))
}

// parseListEmployeesFilter reads ListEmployees' query parameters into a
// ListEmployeesFilter. Every field defaults to "don't filter on this facet"
// when its query parameter is absent or empty — the only failure mode is a
// list parameter (position_ids/store_ids/odoo_employee_ids) containing a
// non-numeric entry, or is_active holding something ParseBool doesn't
// recognize (issue #28 user story 11). A well-formed but nonexistent id is
// not this function's concern — that's a query-time "matches nothing" case
// (user story 12), handled by the SQL, not rejected here.
func parseListEmployeesFilter(r *http.Request) (ListEmployeesFilter, error) {
	var filter ListEmployeesFilter

	if q := r.URL.Query().Get("q"); q != "" {
		filter.Q = &q
	}

	var err error
	if filter.PositionIDs, err = parseInt64ListQueryParam(r, "position_ids"); err != nil {
		return ListEmployeesFilter{}, err
	}
	if filter.StoreIDs, err = parseInt64ListQueryParam(r, "store_ids"); err != nil {
		return ListEmployeesFilter{}, err
	}
	if filter.OdooEmployeeIDs, err = parseInt64ListQueryParam(r, "odoo_employee_ids"); err != nil {
		return ListEmployeesFilter{}, err
	}

	if raw := r.URL.Query().Get("is_active"); raw != "" {
		isActive, err := strconv.ParseBool(raw)
		if err != nil {
			return ListEmployeesFilter{}, fmt.Errorf("invalid is_active: %q is not a valid boolean", raw)
		}
		filter.IsActive = &isActive
	}

	return filter, nil
}

// parseInt64ListQueryParam parses name's comma-separated query parameter
// (e.g. "store_ids=1,2,3") into a []int64. Returns (nil, nil) when the
// parameter is absent or empty, so the caller can tell "not filtering on
// this facet" apart from "filter to nothing" further down.
func parseInt64ListQueryParam(r *http.Request, name string) ([]int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	ids := make([]int64, len(parts))
	for i, p := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %q is not a valid integer", name, p)
		}
		ids[i] = id
	}
	return ids, nil
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
