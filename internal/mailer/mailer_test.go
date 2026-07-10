package mailer

import (
	"strings"
	"testing"
)

func TestRenderTemplateAccountActivation(t *testing.T) {
	msg, err := renderTemplate(AccountActivationTemplate, AccountActivationData{
		FullName:   "Nguyen Van A",
		Link:       "http://localhost:3000/activate?token=abc123",
		TTLMinutes: 30,
	})
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}

	if !strings.Contains(msg.subject, "Kích hoạt") {
		t.Errorf("expected subject to mention activation, got %q", msg.subject)
	}
	for _, want := range []string{"Nguyen Van A", "http://localhost:3000/activate?token=abc123", "30"} {
		if !strings.Contains(msg.plainBody, want) {
			t.Errorf("expected plainBody to contain %q, got:\n%s", want, msg.plainBody)
		}
		if !strings.Contains(msg.htmlBody, want) {
			t.Errorf("expected htmlBody to contain %q, got:\n%s", want, msg.htmlBody)
		}
	}
}
