package auth

import (
	"errors"
	"net/http"
	"strings"

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

// Login handles POST /v1/auth/login. The request's presence-check IP comes
// from middleware.GetClientIPAddr, not the body (ADR-0013: "the app sends
// no IP field for this check") — cmd/api.go's ClientIPFrom* middleware
// derives it from the connection itself before this handler runs.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var params loginParams
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

	result, err := h.service.Login(r.Context(), params, clientIP)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			status = http.StatusUnauthorized
		case errors.Is(err, ErrNoStoreMatch):
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}

	json.Write(w, http.StatusOK, newLoginResponse(result))
}

// Logout handles POST /v1/auth/logout. The caller's session token is read
// from the Authorization header ("Bearer <token>") — there is no session
// middleware yet to have already extracted it (that lands with the
// admin-gating work, see issue #25); Logout is itself the one place in this
// ticket that needs to parse it directly.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "missing or malformed Authorization header", http.StatusUnauthorized)
		return
	}

	if err := h.service.Logout(r.Context(), token); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Heartbeat handles POST /v1/auth/heartbeat. Like Login, the presence
// check's IP comes from middleware.GetClientIPAddr, not the body; the
// session token comes from the Authorization header, same as Logout.
func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "missing or malformed Authorization header", http.StatusUnauthorized)
		return
	}

	var params heartbeatParams
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

	result, err := h.service.Heartbeat(r.Context(), token, params, clientIP)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, newHeartbeatResponse(result))
}

// bearerToken extracts the token from a request's "Authorization: Bearer
// <token>" header.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(header, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}
