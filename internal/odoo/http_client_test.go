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

	client, err := NewHTTPClient(Config{
		BaseURL:      server.URL,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Username:     "service-account",
		Password:     "service-password",
		Database:     "test-db",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	return client, &tokenRequests
}

// TestNewHTTPClient_ValidatesConfig verifies startup fails fast on a
// malformed/missing Odoo config, rather than constructing an unusable
// client whose first real call fails with a confusing error.
func TestNewHTTPClient_ValidatesConfig(t *testing.T) {
	validConfig := Config{
		BaseURL:      "https://erp.bangnails.fr",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Username:     "service-account",
		Password:     "service-password",
		Database:     "db",
	}

	tests := []struct {
		name    string
		mutate  func(cfg Config) Config
		wantErr bool
	}{
		{"valid config", func(cfg Config) Config { return cfg }, false},
		{"empty BaseURL", func(cfg Config) Config { cfg.BaseURL = ""; return cfg }, true},
		{"malformed BaseURL", func(cfg Config) Config { cfg.BaseURL = "not-a-url"; return cfg }, true},
		{"empty ClientID", func(cfg Config) Config { cfg.ClientID = ""; return cfg }, true},
		{"empty ClientSecret", func(cfg Config) Config { cfg.ClientSecret = ""; return cfg }, true},
		{"empty Username", func(cfg Config) Config { cfg.Username = ""; return cfg }, true},
		{"empty Password", func(cfg Config) Config { cfg.Password = ""; return cfg }, true},
		{"empty Database", func(cfg Config) Config { cfg.Database = ""; return cfg }, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewHTTPClient(tc.mutate(validConfig))
			if tc.wantErr && err == nil {
				t.Error("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
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
				t.Errorf("failed to parse token request form: %v", err)
				return
			}
			if r.PostForm.Get("grant_type") != "password" {
				t.Errorf("expected grant_type=password, got %q", r.PostForm.Get("grant_type"))
			}
			if r.PostForm.Get("username") != "service-account" || r.PostForm.Get("password") != "service-password" {
				t.Errorf("expected service account credentials in the body, got username=%q password=%q", r.PostForm.Get("username"), r.PostForm.Get("password"))
			}
			writeJSON(w, tokenResponse{AccessToken: "tok1", ExpiresIn: 3600})

		case searchReadEndpoint:
			if got := r.Header.Get("Authorization"); got != "Bearer tok1" {
				t.Errorf("expected Authorization: Bearer tok1, got %q", got)
			}
			if got := r.Header.Get("DATABASE"); got != "test-db" {
				t.Errorf("expected DATABASE: test-db, got %q", got)
			}
			if got := r.URL.Query().Get("model"); got != storeModel {
				t.Errorf("expected model %q, got %q", storeModel, got)
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
		case searchReadEndpoint:
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
		case searchReadEndpoint:
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

// TestHTTPClient_FetchEmployeesByOdooEmployeeIDs_ParsesID verifies the
// domain filter sent for the lookup (hr.employee's own plain-integer "id",
// not the user_id many2one to res.users), and that the many2many
// x_pos_shop_ids field (a flat list of ids, per ADR-0009) is requested and
// parsed into StoreIDs.
func TestHTTPClient_FetchEmployeesByOdooEmployeeIDs_ParsesID(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenEndpoint:
			writeJSON(w, tokenResponse{AccessToken: "tok1", ExpiresIn: 3600})
		case searchReadEndpoint:
			if got := r.URL.Query().Get("model"); got != employeeModel {
				t.Errorf("expected model %q, got %q", employeeModel, got)
			}
			domain := r.URL.Query().Get("domain")
			want := `[["id","in",[101,102]]]`
			if domain != want {
				t.Errorf("expected domain %q, got %q", want, domain)
			}
			fields := r.URL.Query().Get("fields")
			if !strings.Contains(fields, `"x_pos_shop_ids"`) {
				t.Errorf("expected fields to include x_pos_shop_ids, got %q", fields)
			}
			writeJSON(w, []map[string]any{
				{"id": float64(101), "name": "Nguyen Van A", "email": "van-a@example.com", "x_pos_shop_ids": []any{10, 20}},
				{"id": float64(102), "name": "Tran Thi B", "email": "tran-b@example.com", "x_pos_shop_ids": []any{}},
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

// TestHTTPClient_FetchEmployeesByOdooEmployeeIDs_LargeID verifies an id
// beyond float64's 2^53 exact-integer range (9007199254740993, i.e.
// 2^53+1) round-trips exactly rather than being silently rounded by a
// float64 decode.
func TestHTTPClient_FetchEmployeesByOdooEmployeeIDs_LargeID(t *testing.T) {
	const largeID = int64(9007199254740993)

	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenEndpoint:
			writeJSON(w, tokenResponse{AccessToken: "tok1", ExpiresIn: 3600})
		case searchReadEndpoint:
			w.Header().Set("Content-Type", "application/json")
			// Written as raw JSON text (not a Go float64 literal, which
			// would already have rounded by the time it got here) so the
			// client is the only thing that ever parses this number.
			fmt.Fprintf(w, `[{"id":%d,"name":"Nguyen Van A","email":"van-a@example.com","x_pos_shop_ids":[]}]`, largeID)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	employees, err := client.FetchEmployeesByOdooEmployeeIDs(context.Background(), []int64{largeID})
	if err != nil {
		t.Fatalf("FetchEmployeesByOdooEmployeeIDs() error = %v", err)
	}
	if len(employees) != 1 {
		t.Fatalf("expected 1 employee, got %d", len(employees))
	}
	if employees[0].OdooEmployeeID != largeID {
		t.Errorf("expected OdooEmployeeID %d, got %d", largeID, employees[0].OdooEmployeeID)
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
		case searchReadEndpoint:
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
