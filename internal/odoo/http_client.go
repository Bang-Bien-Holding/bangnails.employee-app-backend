package odoo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Odoo model and field names this client queries, per the field mapping
// this integration's ADRs already record: employee_id/user_id, full_name/
// name, email/email (ADR-0008), store_id/x_pos_shop_ids (ADR-0009).
// storeModel is this integration's documented assumption for the co-model
// behind hr.employee's x_pos_shop_ids many2many field — not itself
// confirmed against the live erp.bangnails.fr instance, which is what the
// E2E verification ticket is for.
const (
	employeeModel      = "hr.employee"
	employeeIDField    = "user_id"
	employeeNameField  = "name"
	employeeEmailField = "email"
	storeModel         = "x_pos_shop"
	storeIDField       = "id"
	storeNameField     = "name"
	storeCityField     = "city"
)

// tokenEndpoint and tokenExpiryLeeway are the MuK REST OAuth2 password-grant
// token endpoint and how far ahead of its stated expiry HTTPClient
// refreshes it, so a request doesn't race a token that's about to expire
// mid-flight.
const (
	tokenEndpoint     = "/api/auth/token"
	tokenExpiryLeeway = 30 * time.Second
)

// Config holds the connection details for a real Odoo instance — all
// sourced from environment variables at startup, following this repo's
// existing secrets convention (see internal/mailer.Config).
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	Username     string
	Password     string
}

// HTTPClient is the real Odoo integration, replacing FakeClient entirely.
// It authenticates via OAuth2 password grant against Odoo's MuK REST API
// token endpoint (client id/secret via the Authorization header, service
// account username/password in the request body — see
// .scratch/employee-odoo-integration/spec.md), caches the resulting access
// token in memory, and transparently re-authenticates on expiry or a 401
// response. There is no refresh-token flow: re-authentication always
// repeats the password grant.
type HTTPClient struct {
	baseURL      string
	clientID     string
	clientSecret string
	username     string
	password     string
	httpClient   *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

func NewHTTPClient(cfg Config) *HTTPClient {
	return &HTTPClient{
		baseURL:      strings.TrimSuffix(cfg.BaseURL, "/"),
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		username:     cfg.Username,
		password:     cfg.Password,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// token returns a cached access token, re-authenticating if none is cached
// or the cached one is within tokenExpiryLeeway of its stated expiry.
func (c *HTTPClient) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	token := c.accessToken
	valid := token != "" && time.Now().Before(c.expiresAt.Add(-tokenExpiryLeeway))
	c.mu.Unlock()

	if valid {
		return token, nil
	}
	return c.authenticate(ctx)
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// authenticate performs the OAuth2 password grant and caches the resulting
// token, overwriting whatever was cached before (including a token another
// concurrent caller may be mid-way through using — the old one simply stops
// working, and its caller's next request re-authenticates in turn).
func (c *HTTPClient) authenticate(ctx context.Context) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", c.username)
	form.Set("password", c.password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.clientID, c.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("odoo: authenticate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("odoo: authenticate: unexpected status %d: %s", resp.StatusCode, body)
	}

	var tok tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("odoo: authenticate: decode response: %w", err)
	}

	c.mu.Lock()
	c.accessToken = tok.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	c.mu.Unlock()

	return tok.AccessToken, nil
}

// searchRead performs an Odoo search_read-style query against model via
// MuK REST's GET /api/<model> endpoint: domain (a JSON-encoded array of
// [field, operator, value] triples), fields (a JSON array of field names),
// and limit/offset for pagination (0 for either means "let Odoo use its
// default"). A 401 response is treated as an expired/rejected cached token:
// authenticate is retried exactly once before failing the call.
func (c *HTTPClient) searchRead(ctx context.Context, model string, domain []any, fields []string, limit, offset int) ([]map[string]any, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("odoo: search_read %s: %w", model, err)
	}

	resp, err := c.doSearchRead(ctx, model, domain, fields, limit, offset, token)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		token, err = c.authenticate(ctx)
		if err != nil {
			return nil, fmt.Errorf("odoo: search_read %s: re-authenticate: %w", model, err)
		}
		resp, err = c.doSearchRead(ctx, model, domain, fields, limit, offset, token)
		if err != nil {
			return nil, err
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("odoo: search_read %s: unexpected status %d: %s", model, resp.StatusCode, respBody)
	}

	var records []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, fmt.Errorf("odoo: search_read %s: decode response: %w", model, err)
	}
	return records, nil
}

func (c *HTTPClient) doSearchRead(ctx context.Context, model string, domain []any, fields []string, limit, offset int, token string) (*http.Response, error) {
	domainJSON, err := json.Marshal(domain)
	if err != nil {
		return nil, err
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("domain", string(domainJSON))
	q.Set("fields", string(fieldsJSON))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/"+model+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	return c.httpClient.Do(req)
}

// FetchStores fetches every store record from Odoo in one call — the store
// count is small enough that pagination isn't needed.
func (c *HTTPClient) FetchStores(ctx context.Context) ([]Store, error) {
	records, err := c.searchRead(ctx, storeModel, []any{}, []string{storeIDField, storeNameField, storeCityField}, 0, 0)
	if err != nil {
		return nil, err
	}

	stores := make([]Store, 0, len(records))
	for _, r := range records {
		id, err := recordInt(r, storeIDField)
		if err != nil {
			return nil, fmt.Errorf("odoo: store record: %w", err)
		}
		stores = append(stores, Store{
			ID:   id,
			Name: recordString(r, storeNameField),
			City: recordString(r, storeCityField),
		})
	}
	return stores, nil
}

// FetchEmployeesByOdooEmployeeIDs looks up hr.employee records whose
// user_id (this integration's odoo_employee_id join key — see ADR-0008) is
// in odooEmployeeIDs. An empty input returns immediately without a network
// call — there is nothing to look up. An id Odoo doesn't recognize is
// simply absent from the result, matching the Client interface's contract.
func (c *HTTPClient) FetchEmployeesByOdooEmployeeIDs(ctx context.Context, odooEmployeeIDs []int64) ([]Employee, error) {
	if len(odooEmployeeIDs) == 0 {
		return nil, nil
	}

	ids := make([]any, len(odooEmployeeIDs))
	for i, id := range odooEmployeeIDs {
		ids[i] = id
	}
	domain := []any{[]any{employeeIDField, "in", ids}}

	records, err := c.searchRead(ctx, employeeModel, domain, []string{employeeIDField, employeeNameField, employeeEmailField}, 0, 0)
	if err != nil {
		return nil, err
	}

	employees := make([]Employee, 0, len(records))
	for _, r := range records {
		id, err := recordMany2OneID(r, employeeIDField)
		if err != nil {
			return nil, fmt.Errorf("odoo: employee record: %w", err)
		}
		employees = append(employees, Employee{
			OdooEmployeeID: id,
			FullName:       recordString(r, employeeNameField),
			Email:          recordString(r, employeeEmailField),
		})
	}
	return employees, nil
}

// recordString reads a plain string field from a decoded Odoo record,
// tolerating Odoo's convention of representing an unset field as the JSON
// literal false rather than "" or null.
func recordString(record map[string]any, field string) string {
	v, _ := record[field].(string)
	return v
}

// recordInt reads a plain numeric field (e.g. a store's own "id").
func recordInt(record map[string]any, field string) (int, error) {
	v, ok := record[field].(float64)
	if !ok {
		return 0, fmt.Errorf("field %q: expected a number, got %T", field, record[field])
	}
	return int(v), nil
}

// recordMany2OneID reads a many2one field, which Odoo represents as a
// [id, display_name] tuple.
func recordMany2OneID(record map[string]any, field string) (int64, error) {
	tuple, ok := record[field].([]any)
	if !ok || len(tuple) == 0 {
		return 0, fmt.Errorf("field %q: expected a [id, name] tuple, got %T", field, record[field])
	}
	id, ok := tuple[0].(float64)
	if !ok {
		return 0, fmt.Errorf("field %q: expected a numeric id, got %T", field, tuple[0])
	}
	return int64(id), nil
}
