//go:build e2e || dbe2e

// Shared low-level HTTP primitives for every e2e suite in this package,
// regardless of which build tag gates it: cmd/e2e_test.go (build tag "e2e",
// needs a live Odoo instance — see its own doc comment) and
// cmd/login_e2e_test.go (build tag "dbe2e", needs only Postgres). Both hit
// the real router via httptest.Server and want the same unauthenticated
// request/response plumbing — the Bearer-token variant only the login suite
// needs lives in cmd/login_e2e_client_test.go instead.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

type apiResponse struct {
	status int
	raw    []byte
}

func (r apiResponse) decode(t *testing.T, v any) {
	t.Helper()
	if len(r.raw) == 0 {
		return
	}
	if err := json.Unmarshal(r.raw, v); err != nil {
		t.Fatalf("decode response body %q: %v", r.raw, err)
	}
}

func e2eRequest(t *testing.T, client *http.Client, method, url string, body any) apiResponse {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, url, reqBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	return apiResponse{status: resp.StatusCode, raw: raw}
}

func e2eUnique(t *testing.T, label string) string {
	t.Helper()
	return fmt.Sprintf("e2e-%s-%d", label, time.Now().UnixNano())
}
