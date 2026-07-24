//go:build dbe2e

// GET /v1/stores' query-parameter search/filter support (issues #32/#33/#34)
// needs a GET with query parameters that loginE2EClient doesn't already
// provide. Rather than introduce a parallel client type duplicating
// loginE2EClient's Bearer-auth request plumbing, this file adds that one
// method directly onto loginE2EClient — legal since it's defined in the
// same package (cmd/login_e2e_client_test.go) — and reuses its existing
// authRequest/MustLogin. Mirrors cmd/employees_search_e2e_client_test.go's
// List.
package main

import (
	"net/http"
	"net/url"
	"testing"
)

// ListStores calls GET /stores (mounted under /v1 by loginE2EClient's
// baseURL, see loginE2ESetup) with query as an authenticated Admin — every
// test in cmd/stores_search_e2e_test.go is really asserting on this one
// route's filtering/sorting behavior (issues #32/#33/#34).
func (c *loginE2EClient) ListStores(t *testing.T, token string, query url.Values) apiResponse {
	t.Helper()

	path := "/stores"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return c.authRequest(t, http.MethodGet, path, token, nil)
}
