// Package httpx holds small HTTP-handler-layer helpers shared across
// resource packages.
package httpx

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// ParsePathID parses the named chi URL param as a positive int64 — the only
// shape a primary key in this schema can have. On failure it writes a 400
// with "invalid <resource> id" and returns false; the caller should return
// immediately without doing anything else.
func ParsePathID(w http.ResponseWriter, r *http.Request, param, resource string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, param), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid "+resource+" id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
