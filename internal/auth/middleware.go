package auth

import (
	"errors"
	"log/slog"
	"net/http"
)

// AdminOnly is the admin-gating middleware issue #25 calls for: it wraps
// the existing employees/stores/positions routes at the routing layer
// (cmd/api.go), without touching those packages' own Service/Handler
// internals. It rejects any request without a valid Admin Session — a
// missing/malformed Authorization header or a token ValidateSession can't
// resolve to an open Session both get 401; a valid Session belonging to a
// non-Admin Employee gets 403.
func AdminOnly(service Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				http.Error(w, "missing or malformed Authorization header", http.StatusUnauthorized)
				return
			}

			session, err := service.ValidateSession(r.Context(), token)
			if err != nil {
				if errors.Is(err, ErrSessionNotFound) {
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				slog.Error("auth: validate session", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if !session.IsAdmin {
				http.Error(w, "admin session required", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
