package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const brevoAPIURL = "https://api.brevo.com/v3/smtp/email"

// BrevoClient sends email through the Brevo transactional email HTTP API. It is
// the production transport; dev/staging use MailpitClient instead.
type BrevoClient struct {
	apiToken  string
	fromEmail string
	fromName  string
	endpoint  string
	client    *http.Client
}

func NewBrevo(apiToken, fromEmail, fromName string) *BrevoClient {
	return &BrevoClient{
		apiToken:  apiToken,
		fromEmail: fromEmail,
		fromName:  fromName,
		endpoint:  brevoAPIURL,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

type brevoAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type brevoRequest struct {
	Sender      brevoAddress   `json:"sender"`
	To          []brevoAddress `json:"to"`
	Subject     string         `json:"subject"`
	TextContent string         `json:"textContent"`
	HTMLContent string         `json:"htmlContent"`
}

func (c *BrevoClient) Send(ctx context.Context, to, templateFile string, data any) error {
	msg, err := renderTemplate(templateFile, data)
	if err != nil {
		return err
	}

	body, err := json.Marshal(brevoRequest{
		Sender:      brevoAddress{Email: c.fromEmail, Name: c.fromName},
		To:          []brevoAddress{{Email: to}},
		Subject:     msg.subject,
		TextContent: msg.plainBody,
		HTMLContent: msg.htmlBody,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", c.apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("brevo: unexpected status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}
