//go:build dbe2e

// loginE2EClient is the one seam every test in cmd/login_e2e_test.go
// crosses to talk to the running application: Login, Heartbeat, Logout,
// Activate, AdminGET. Every method returns the raw apiResponse (status +
// decodable body) rather than asserting anything itself — a test expecting
// 401 and a test expecting 200 both cross the same seam the same way. The
// Must* methods are sugar for the "arrange, this has to succeed" call sites
// (most of this suite's setup steps): they call the plain method and fail
// the test immediately on an unexpected status, so only the handful of
// tests that need a live Session ever have to spell that check out.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

type loginE2EClient struct {
	http *http.Client
	base string
}

func newLoginE2EClient(httpClient *http.Client, base string) *loginE2EClient {
	return &loginE2EClient{http: httpClient, base: base}
}

// loginE2ELoginResponse mirrors auth.loginResponse's JSON shape.
type loginE2ELoginResponse struct {
	Token     string    `json:"token"`
	StoreID   *int64    `json:"store_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// loginE2EHeartbeatResponse mirrors auth.heartbeatResponse's JSON shape.
type loginE2EHeartbeatResponse struct {
	Active bool   `json:"active"`
	Reason string `json:"reason"`
}

// RawLogin posts an arbitrary body to /auth/login — the one escape hatch
// past Login's typed signature, for TestLoginE2E_Validation's deliberately
// malformed/incomplete bodies.
func (c *loginE2EClient) RawLogin(t *testing.T, body any) apiResponse {
	t.Helper()
	return e2eRequest(t, c.http, http.MethodPost, c.base+"/auth/login", body)
}

func (c *loginE2EClient) Login(t *testing.T, username, password string, lat, long float64, mac string) apiResponse {
	t.Helper()
	return c.RawLogin(t, loginBody(username, password, lat, long, mac))
}

// MustLogin is the shared "arrange" step for every test whose actual
// subject is Logout/Heartbeat/AdminOnly, not Login itself: it fails the
// test immediately if Login didn't return 200.
func (c *loginE2EClient) MustLogin(t *testing.T, username, password string, lat, long float64, mac string) loginE2ELoginResponse {
	t.Helper()
	resp := c.Login(t, username, password, lat, long, mac)
	if resp.status != http.StatusOK {
		t.Fatalf("POST /auth/login: expected 200, got %d: %s", resp.status, resp.raw)
	}
	var lr loginE2ELoginResponse
	resp.decode(t, &lr)
	if lr.Token == "" {
		t.Fatalf("POST /auth/login: 200 response had an empty token: %s", resp.raw)
	}
	return lr
}

// RawHeartbeat posts an arbitrary body to /auth/heartbeat — the escape
// hatch past Heartbeat's typed signature, for the missing-latitude
// validation case.
func (c *loginE2EClient) RawHeartbeat(t *testing.T, token string, body any) apiResponse {
	t.Helper()
	return c.authRequest(t, http.MethodPost, "/auth/heartbeat", token, body)
}

func (c *loginE2EClient) Heartbeat(t *testing.T, token string, lat, long float64, mac string) apiResponse {
	t.Helper()
	return c.RawHeartbeat(t, token, heartbeatBody(lat, long, mac))
}

func (c *loginE2EClient) Logout(t *testing.T, token string) apiResponse {
	t.Helper()
	return c.authRequest(t, http.MethodPost, "/auth/logout", token, nil)
}

// Activate sends confirmPassword equal to password — every existing call
// site here is exercising something other than the confirmPassword check
// (reuse, expiry, staleness), so it needs the confirm to match for the
// request to even reach that code path (issue #38's eqfield validation runs
// before the service call). TestPasswordResetE2E's own file covers a
// deliberate mismatch via RawActivate.
func (c *loginE2EClient) Activate(t *testing.T, token, password string) apiResponse {
	t.Helper()
	return c.RawActivate(t, token, password, password)
}

// RawActivate posts an arbitrary confirmPassword alongside token/password —
// the escape hatch past Activate's matching-confirm convenience, for tests
// that need a deliberate mismatch.
func (c *loginE2EClient) RawActivate(t *testing.T, token, password, confirmPassword string) apiResponse {
	t.Helper()
	return e2eRequest(t, c.http, http.MethodPost, c.base+"/activate", map[string]any{
		"token":           token,
		"password":        password,
		"confirmPassword": confirmPassword,
	})
}

// MustActivate is the "arrange" step for TestActivationThenLoginE2E's happy
// path: it fails the test immediately if Activate didn't return 204.
func (c *loginE2EClient) MustActivate(t *testing.T, token, password string) {
	t.Helper()
	resp := c.Activate(t, token, password)
	if resp.status != http.StatusNoContent {
		t.Fatalf("POST /activate: expected 204, got %d: %s", resp.status, resp.raw)
	}
}

// RequestPasswordReset hits the real public, unauthenticated
// POST /password-reset-requests endpoint (issue #38) — the entry point
// TestPasswordResetE2E drives, as opposed to seeding a token directly via
// fixtures.ActivationToken.
func (c *loginE2EClient) RequestPasswordReset(t *testing.T, email string) apiResponse {
	t.Helper()
	return e2eRequest(t, c.http, http.MethodPost, c.base+"/password-reset-requests", map[string]any{
		"email": email,
	})
}

// AdminGET hits the one real admin-gated route (GET /v1/employees)
// TestAdminOnlyGatingE2E exercises the AdminOnly gate through — the gate
// itself, not what it protects, is the point, so this stays fixed rather
// than taking a path parameter for a second route that doesn't exist yet.
func (c *loginE2EClient) AdminGET(t *testing.T, token string) apiResponse {
	t.Helper()
	return c.authRequest(t, http.MethodGet, "/employees", token, nil)
}

// BulkSendPasswordResetLinks hits the real admin-gated
// POST /v1/employees/password-reset-links endpoint — the one entry point
// TestActivationThenLoginE2E's stale-token case drives to prove
// issuePasswordResetToken's invalidation (issue #37) fires through a real
// HTTP call, not just the mocked unit test.
func (c *loginE2EClient) BulkSendPasswordResetLinks(t *testing.T, token string, ids []int64) apiResponse {
	t.Helper()
	return c.authRequest(t, http.MethodPost, "/employees/password-reset-links", token, map[string]any{
		"ids": ids,
	})
}

// authRequest is the Bearer-authenticated request builder every
// token-taking method funnels through — the one place
// "Authorization: Bearer <token>" gets set. token == "" omits the header
// entirely, which is what every missing-authorization-header case in this
// suite exercises, uniformly, through the same method as a real token.
func (c *loginE2EClient) authRequest(t *testing.T, method, path, token string, body any) apiResponse {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, c.base+path, reqBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	return apiResponse{status: resp.StatusCode, raw: raw}
}

func loginBody(username, password string, lat, long float64, mac string) map[string]any {
	body := map[string]any{
		"username":  username,
		"password":  password,
		"latitude":  lat,
		"longitude": long,
	}
	if mac != "" {
		body["mac_address"] = mac
	}
	return body
}

func heartbeatBody(lat, long float64, mac string) map[string]any {
	body := map[string]any{
		"latitude":  lat,
		"longitude": long,
	}
	if mac != "" {
		body["mac_address"] = mac
	}
	return body
}
