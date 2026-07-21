package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func withURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestParsePathID(t *testing.T) {
	tests := []struct {
		name       string
		idParam    string
		wantID     int64
		wantOK     bool
		wantStatus int
	}{
		{name: "valid positive id", idParam: "42", wantID: 42, wantOK: true},
		{name: "zero id is rejected", idParam: "0", wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "negative id is rejected", idParam: "-1", wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "non-numeric id is rejected", idParam: "not-a-number", wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "missing id is rejected", idParam: "", wantOK: false, wantStatus: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := withURLParam(httptest.NewRequest(http.MethodGet, "/things/"+tc.idParam, nil), "id", tc.idParam)
			rec := httptest.NewRecorder()

			id, ok := ParsePathID(rec, req, "id", "thing")

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				if rec.Code != tc.wantStatus {
					t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
				}
				if rec.Body.String() != "invalid thing id\n" {
					t.Errorf("body = %q, want %q", rec.Body.String(), "invalid thing id\n")
				}
				return
			}
			if id != tc.wantID {
				t.Errorf("id = %d, want %d", id, tc.wantID)
			}
		})
	}
}
