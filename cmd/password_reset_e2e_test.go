//go:build dbe2e

// End-to-end verification of the self-service password reset request flow
// (issue #38, part of #36): POST /v1/password-reset-requests, and what it
// hands off to the same completion path cmd/login_e2e_test.go's
// TestActivationThenLoginE2E already covers (POST /v1/activate). Every
// assertion goes through this application's own HTTP API, the same
// "no mocks in this seam" approach as the rest of this package's dbe2e
// suites — the anti-enumeration property (issue #36's core requirement,
// every branch responding with the same generic 200) and the real
// invalidate-prior-token/session-cleanup DB effects are exactly the kind of
// thing only a real database and a real HTTP round trip can validate.
//
// This file adds no new client/fixture types of its own beyond
// loginE2EClient.RequestPasswordReset/RawActivate and
// loginE2EFixtures.UnusedPasswordResetTokenCount (both alongside Login's and
// Activation's own seams, in cmd/login_e2e_client_test.go and
// cmd/login_e2e_fixtures_test.go) — this feature reuses their Employee/Store
// fixtures and Activate/Login client methods directly. It does add one
// thing only this suite needs: reading the raw token back out of Mailpit
// (see passwordResetTokenFromMailpit below), since — unlike
// fixtures.ActivationToken, which seeds a token directly and bypasses the
// mailer — going through the real endpoint means the raw token exists
// nowhere but the email Mailpit received.
//
// Run it locally against the docker-compose Postgres + Mailpit with
// migrations applied:
//
//	go test -tags dbe2e -run TestPasswordResetE2E ./cmd/... -v
package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"
)

func TestPasswordResetE2E(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	t.Run("active employee: request, activate, login with new password", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})

		resp := client.RequestPasswordReset(t, employee.Email)
		if resp.status != http.StatusOK {
			t.Fatalf("POST /password-reset-requests: expected 200, got %d: %s", resp.status, resp.raw)
		}

		if n := fixtures.UnusedPasswordResetTokenCount(t, employee.ID); n != 1 {
			t.Fatalf("expected exactly 1 unused password reset token after the request, got %d", n)
		}

		token := passwordResetTokenFromMailpit(t, employee.Email)
		const newPassword = "brand-new-reset-password-1"
		client.MustActivate(t, token, newPassword)

		if n := fixtures.UnusedPasswordResetTokenCount(t, employee.ID); n != 0 {
			t.Errorf("expected 0 unused password reset tokens after redemption, got %d", n)
		}

		storeID := fixtures.Store(t, storeSeed{IPs: []string{loginE2ELoopbackIP}})
		fixtures.Link(t, employee.ID, storeID)

		lr := client.MustLogin(t, employee.Username, newPassword, loginE2EFarLat, loginE2EFarLong, "")
		if lr.StoreID == nil || *lr.StoreID != storeID {
			t.Errorf("expected login with the reset password to match store_id=%d, got %v", storeID, lr.StoreID)
		}
	})

	t.Run("pending-activation employee: request resends the activation email, same generic response", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: false})

		resp := client.RequestPasswordReset(t, employee.Email)
		if resp.status != http.StatusOK {
			t.Fatalf("POST /password-reset-requests: expected 200, got %d: %s", resp.status, resp.raw)
		}

		// Same branch as sendActivationEmail (internal/employees/service.go):
		// a never-activated Employee still gets a fresh password_reset_tokens
		// row, redeemable the same way as a real reset token — the DB-state
		// assertion is identical, only the mailer template differs.
		if n := fixtures.UnusedPasswordResetTokenCount(t, employee.ID); n != 1 {
			t.Fatalf("expected exactly 1 unused password reset token after the request, got %d", n)
		}
	})

	t.Run("inactive employee: no token issued, still 200 generic", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true, Inactive: true})

		resp := client.RequestPasswordReset(t, employee.Email)
		if resp.status != http.StatusOK {
			t.Fatalf("POST /password-reset-requests: expected 200, got %d: %s", resp.status, resp.raw)
		}
		if n := fixtures.UnusedPasswordResetTokenCount(t, employee.ID); n != 0 {
			t.Errorf("expected 0 password reset tokens for an inactive employee, got %d", n)
		}
	})

	t.Run("unknown email: still 200 generic, anti-enumeration", func(t *testing.T) {
		resp := client.RequestPasswordReset(t, e2eUnique(t, "nobody")+"@example.com")
		if resp.status != http.StatusOK {
			t.Fatalf("POST /password-reset-requests: expected 200, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("malformed email is rejected before any lookup", func(t *testing.T) {
		resp := client.RequestPasswordReset(t, "not-an-email")
		if resp.status != http.StatusBadRequest {
			t.Errorf("expected 400, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("repeated requests invalidate the prior unused token, leaving exactly one live", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})

		var firstToken string
		for i := 0; i < 3; i++ {
			resp := client.RequestPasswordReset(t, employee.Email)
			if resp.status != http.StatusOK {
				t.Fatalf("POST /password-reset-requests (call %d): expected 200, got %d: %s", i+1, resp.status, resp.raw)
			}
			if i == 0 {
				firstToken = passwordResetTokenFromMailpit(t, employee.Email)
			}
		}

		if n := fixtures.UnusedPasswordResetTokenCount(t, employee.ID); n != 1 {
			t.Errorf("expected exactly 1 unused password reset token to survive 3 repeated requests, got %d", n)
		}

		stale := client.RawActivate(t, firstToken, "stale-token-password-1", "stale-token-password-1")
		if stale.status != http.StatusBadRequest {
			t.Errorf("expected 400 activating with the invalidated first token, got %d: %s", stale.status, stale.raw)
		}
	})

	t.Run("activate with mismatched confirmPassword is rejected", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})

		resp := client.RequestPasswordReset(t, employee.Email)
		if resp.status != http.StatusOK {
			t.Fatalf("POST /password-reset-requests: expected 200, got %d: %s", resp.status, resp.raw)
		}
		token := passwordResetTokenFromMailpit(t, employee.Email)

		mismatch := client.RawActivate(t, token, "brand-new-password-1", "does-not-match")
		if mismatch.status != http.StatusBadRequest {
			t.Errorf("expected 400 on confirmPassword mismatch, got %d: %s", mismatch.status, mismatch.raw)
		}
		if n := fixtures.UnusedPasswordResetTokenCount(t, employee.ID); n != 1 {
			t.Errorf("expected the token to remain unused after a rejected mismatch, got %d unused", n)
		}
	})

	t.Run("completing a reset clears an existing session and lockout", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})
		storeID := fixtures.Store(t, storeSeed{IPs: []string{loginE2ELoopbackIP}})
		fixtures.Link(t, employee.ID, storeID)

		lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		// Simulates a lockout that accrued from before the reset, the same
		// time-travel-free way TestLoginE2E_Lockout seeds it — this test's
		// point is that CompleteActivation clears stale lockout state, not
		// that failed logins accumulate it (already covered elsewhere).
		fixtures.SetLockedUntil(t, employee.ID, time.Now().Add(time.Hour))

		resp := client.RequestPasswordReset(t, employee.Email)
		if resp.status != http.StatusOK {
			t.Fatalf("POST /password-reset-requests: expected 200, got %d: %s", resp.status, resp.raw)
		}
		token := passwordResetTokenFromMailpit(t, employee.Email)
		client.MustActivate(t, token, "post-reset-password-1")

		if fixtures.SessionExists(t, lr.Token) {
			t.Error("expected the employee's prior session to be deleted after completing the reset")
		}

		lock := fixtures.EmployeeLockState(t, employee.ID)
		if lock.LockedUntil.Valid {
			t.Errorf("expected locked_until to be cleared after completing the reset, got %v", lock.LockedUntil)
		}
	})
}

// mailpitAPIBase is Mailpit's HTTP API — docker-compose's "mailpit" service
// exposes it on 127.0.0.1:8025, the same loopback-only local/CI environment
// loginE2ENonLocalDatabaseReason already assumes for Postgres.
const mailpitAPIBase = "http://localhost:8025"

// passwordResetTokenPattern extracts the raw token from either
// PasswordResetTemplate's or AccountActivationTemplate's link
// (".../activate?token=..." or ".../reset-password?token=...", both built by
// issuePasswordResetToken).
var passwordResetTokenPattern = regexp.MustCompile(`[?&]token=([0-9a-f]+)`)

// passwordResetTokenFromMailpit polls Mailpit's HTTP API for the most recent
// email sent to `to` and extracts its raw token — the escape hatch this
// suite needs to redeem a token that came from the real
// POST /password-reset-requests endpoint, as opposed to
// fixtures.ActivationToken (which seeds one directly and never sends mail).
// issuePasswordResetToken never persists the raw token anywhere — the emailed
// link is the only place it exists — so reading it back means going through
// the real mailer transport, the same as a real employee clicking the link.
// SMTP delivery to Mailpit (mailer.MailpitClient.Send) is synchronous and
// already complete by the time RequestPasswordReset's HTTP response returns,
// but Mailpit's own indexing can lag by a few milliseconds, so this retries
// briefly rather than reading once.
func passwordResetTokenFromMailpit(t *testing.T, to string) string {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if body, ok := latestMailpitMessageBody(t, to); ok {
			m := passwordResetTokenPattern.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("mailpit message to %s has no ?token= link in its body: %s", to, body)
			}
			return m[1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no mailpit message arrived for %s within 5s", to)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type mailpitSearchResponse struct {
	Messages []struct {
		ID string `json:"ID"`
	} `json:"messages"`
}

type mailpitMessageResponse struct {
	Text string `json:"Text"`
}

// latestMailpitMessageBody returns the plain-text body of the newest message
// Mailpit has for `to` (its search API sorts newest first), or ok=false if
// none has arrived yet.
func latestMailpitMessageBody(t *testing.T, to string) (body string, ok bool) {
	t.Helper()

	var search mailpitSearchResponse
	mailpitGET(t, "/api/v1/search?query="+url.QueryEscape("to:"+to), &search)
	if len(search.Messages) == 0 {
		return "", false
	}

	var msg mailpitMessageResponse
	mailpitGET(t, "/api/v1/message/"+search.Messages[0].ID, &msg)
	return msg.Text, true
}

func mailpitGET(t *testing.T, path string, v any) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, mailpitAPIBase+path, nil)
	if err != nil {
		t.Fatalf("build mailpit request %s: %v", path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET mailpit %s: %v (is `docker compose up -d mailpit` running?)", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode mailpit response %s: %v", path, err)
	}
}
