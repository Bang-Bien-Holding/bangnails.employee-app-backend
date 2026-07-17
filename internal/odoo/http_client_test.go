package odoo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*HTTPClient, *int32) {
	t.Helper()
	var tokenRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenEndpoint {
			atomic.AddInt32(&tokenRequests, 1)
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(Config{
		BaseURL:      server.URL,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Username:     "service-account",
		Password:     "service-password",
	})
	return client, &tokenRequests
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// TestHTTPClient_FetchStores_AuthenticatesAndQueries verifies the client
// authenticates via OAuth2 password grant (client credentials via
// Authorization header, username/password in the body), then queries the
// store model via search_read and parses the response.
func TestHTTPClient_FetchStores_AuthenticatesAndQueries(t *testing.T) {
	client, tokenRequests := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenEndpoint:
			user, pass, ok := r.BasicAuth()
			if !ok || user != "test-client-id" || pass != "test-client-secret" {
				t.Errorf("expected Basic auth test-client-id:test-client-secret, got %q/%q (ok=%v)", user, pass, ok)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("failed to parse token request form: %v", err)
			}
			if r.PostForm.Get("grant_type") != "password" {
				t.Errorf("expected grant_type=password, got %q", r.PostForm.Get("grant_type"))
			}
			if r.PostForm.Get("username") != "service-account" || r.PostForm.Get("password") != "service-password" {
				t.Errorf("expected service account credentials in the body, got username=%q password=%q", r.PostForm.Get("username"), r.PostForm.Get("password"))
			}
			writeJSON(w, tokenResponse{AccessToken: "tok1", ExpiresIn: 3600})

		case "/api/" + storeModel:
			if got := r.Header.Get("Authorization"); got != "Bearer tok1" {
				t.Errorf("expected Authorization: Bearer tok1, got %q", got)
			}
			domain := r.URL.Query().Get("domain")
			if domain != "[]" {
				t.Errorf("expected an empty domain filter, got %q", domain)
			}
			writeJSON(w, []map[string]any{
				{"id": float64(1), "name": "Store #1", "city": "Hanoi"},
				{"id": float64(2), "name": "Store #2", "city": "Da Nang"},
			})

		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	stores, err := client.FetchStores(context.Background())
	if err != nil {
		t.Fatalf("FetchStores() error = %v", err)
	}
	if len(stores) != 2 {
		t.Fatalf("expected 2 stores, got %d", len(stores))
	}
	if stores[0] != (Store{ID: 1, Name: "Store #1", City: "Hanoi"}) {
		t.Errorf("unexpected first store: %+v", stores[0])
	}
	if *tokenRequests != 1 {
		t.Errorf("expected exactly 1 token request, got %d", *tokenRequests)
	}
}

// TestHTTPClient_CachesToken verifies a second call within the token's
// lifetime reuses the cached access token instead of re-authenticating.
func TestHTTPClient_CachesToken(t *testing.T) {
	var calls int32
	client, tokenRequests := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenEndpoint:
			writeJSON(w, tokenResponse{AccessToken: "tok1", ExpiresIn: 3600})
		case "/api/" + storeModel:
			atomic.AddInt32(&calls, 1)
			writeJSON(w, []map[string]any{})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	ctx := context.Background()
	if _, err := client.FetchStores(ctx); err != nil {
		t.Fatalf("first FetchStores() error = %v", err)
	}
	if _, err := client.FetchStores(ctx); err != nil {
		t.Fatalf("second FetchStores() error = %v", err)
	}

	if *tokenRequests != 1 {
		t.Errorf("expected exactly 1 token request across 2 data calls, got %d", *tokenRequests)
	}
	if calls != 2 {
		t.Errorf("expected 2 data calls, got %d", calls)
	}
}

// TestHTTPClient_ReAuthenticatesOn401 verifies that a 401 on the data
// endpoint (a token Odoo has since rejected, e.g. revoked or expired
// early) triggers exactly one re-authentication and the request is retried
// with the new token, rather than failing the caller.
func TestHTTPClient_ReAuthenticatesOn401(t *testing.T) {
	var issuedTokens int32
	client, tokenRequests := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenEndpoint:
			n := atomic.AddInt32(&issuedTokens, 1)
			writeJSON(w, tokenResponse{AccessToken: fmt.Sprintf("tok%d", n), ExpiresIn: 3600})
		case "/api/" + storeModel:
			auth := r.Header.Get("Authorization")
			if auth == "Bearer tok1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if auth != "Bearer tok2" {
				t.Errorf("expected retry with Bearer tok2, got %q", auth)
			}
			writeJSON(w, []map[string]any{{"id": float64(1), "name": "Store #1", "city": "Hanoi"}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	stores, err := client.FetchStores(context.Background())
	if err != nil {
		t.Fatalf("FetchStores() error = %v", err)
	}
	if len(stores) != 1 {
		t.Fatalf("expected 1 store after re-authenticating, got %d", len(stores))
	}
	if *tokenRequests != 2 {
		t.Errorf("expected exactly 2 token requests (initial + re-auth on 401), got %d", *tokenRequests)
	}
}

// TestHTTPClient_FetchEmployeesByOdooEmployeeIDs_ParsesMany2One verifies
// the domain filter sent for the lookup, that a many2one field (Odoo's
// [id, display_name] tuple shape) is correctly unpacked to just the id, and
// that the many2many x_pos_shop_ids field (a flat list of ids, per
// ADR-0009) is requested and parsed into StoreIDs.
func TestHTTPClient_FetchEmployeesByOdooEmployeeIDs_ParsesMany2One(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenEndpoint:
			writeJSON(w, tokenResponse{AccessToken: "tok1", ExpiresIn: 3600})
		case "/api/" + employeeModel:
			domain := r.URL.Query().Get("domain")
			want := `[["user_id","in",[101,102]]]`
			if domain != want {
				t.Errorf("expected domain %q, got %q", want, domain)
			}
			fields := r.URL.Query().Get("fields")
			if !strings.Contains(fields, `"x_pos_shop_ids"`) {
				t.Errorf("expected fields to include x_pos_shop_ids, got %q", fields)
			}
			writeJSON(w, []map[string]any{
				{"user_id": []any{101, "Nguyen Van A"}, "name": "Nguyen Van A", "email": "van-a@example.com", "x_pos_shop_ids": []any{10, 20}},
				{"user_id": []any{102, "Tran Thi B"}, "name": "Tran Thi B", "email": "tran-b@example.com", "x_pos_shop_ids": []any{}},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	employees, err := client.FetchEmployeesByOdooEmployeeIDs(context.Background(), []int64{101, 102})
	if err != nil {
		t.Fatalf("FetchEmployeesByOdooEmployeeIDs() error = %v", err)
	}
	if len(employees) != 2 {
		t.Fatalf("expected 2 employees, got %d", len(employees))
	}
	if employees[0].OdooEmployeeID != 101 || employees[0].FullName != "Nguyen Van A" || employees[0].Email != "van-a@example.com" {
		t.Errorf("unexpected first employee: %+v", employees[0])
	}
	if fmt.Sprint(employees[0].StoreIDs) != fmt.Sprint([]int{10, 20}) {
		t.Errorf("expected first employee StoreIDs [10 20], got %v", employees[0].StoreIDs)
	}
	if employees[1].OdooEmployeeID != 102 {
		t.Errorf("expected second employee OdooEmployeeID 102, got %d", employees[1].OdooEmployeeID)
	}
	if len(employees[1].StoreIDs) != 0 {
		t.Errorf("expected second employee to have no store ids, got %v", employees[1].StoreIDs)
	}
}

// TestHTTPClient_FetchEmployeesByOdooEmployeeIDs_EmptyInput verifies an
// empty id list returns immediately without any network call — matching
// FakeClient's existing determinism for this edge case.
func TestHTTPClient_FetchEmployeesByOdooEmployeeIDs_EmptyInput(t *testing.T) {
	client, tokenRequests := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request for an empty id list: %s %s", r.Method, r.URL.Path)
	})

	employees, err := client.FetchEmployeesByOdooEmployeeIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("FetchEmployeesByOdooEmployeeIDs() error = %v", err)
	}
	if len(employees) != 0 {
		t.Errorf("expected no employees, got %v", employees)
	}
	if *tokenRequests != 0 {
		t.Errorf("expected no token requests, got %d", *tokenRequests)
	}
}

// TestHTTPClient_AuthenticateFailure_ReturnsError verifies a rejected
// password grant (bad credentials) surfaces as an error rather than
// panicking or silently returning no data.
func TestHTTPClient_AuthenticateFailure_ReturnsError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != tokenEndpoint {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})

	_, err := client.FetchStores(context.Background())
	if err == nil {
		t.Fatal("expected an error when authentication fails, got nil")
	}
	if !strings.Contains(err.Error(), "authenticate") {
		t.Errorf("expected error to mention authentication, got: %v", err)
	}
}

// TestHTTPClient_SearchReadFailure_ReturnsError verifies a non-2xx,
// non-401 response from the data endpoint surfaces as an error including
// the status code.
func TestHTTPClient_SearchReadFailure_ReturnsError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenEndpoint:
			writeJSON(w, tokenResponse{AccessToken: "tok1", ExpiresIn: 3600})
		case "/api/" + storeModel:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal error"))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	_, err := client.FetchStores(context.Background())
	if err == nil {
		t.Fatal("expected an error on a 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}
