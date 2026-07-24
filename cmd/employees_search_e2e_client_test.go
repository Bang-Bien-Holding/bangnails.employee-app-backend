//go:build dbe2e

// GET /employees' query-parameter search/filter support (issue #28) needs a
// GET with query parameters that loginE2EClient doesn't already provide
// (its AdminGET is a fixed, parameter-less route). Rather than introduce a
// parallel client type duplicating loginE2EClient's Bearer-auth request
// plumbing, this file adds that one method directly onto loginE2EClient —
// legal since it's defined in the same package (cmd/login_e2e_client_test.go)
// — and reuses its existing authRequest/MustLogin.
package main

import (
	"net/http"
	"net/url"
	"testing"
)

// List calls GET /employees with query as an authenticated Admin — every
// test in cmd/employees_search_e2e_test.go is really asserting on this one
// route's filtering/sorting behavior (issue #28).
func (c *loginE2EClient) List(t *testing.T, token string, query url.Values) apiResponse {
	t.Helper()

	path := "/employees"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return c.authRequest(t, http.MethodGet, path, token, nil)
}
