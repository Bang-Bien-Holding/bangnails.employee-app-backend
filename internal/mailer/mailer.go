package mailer

//go:generate mockgen -source=mailer.go -destination=mocks/mock_client.go -package=mocks

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	texttemplate "text/template"
)

//go:embed templates
var templateFS embed.FS

const (
	PasswordResetTemplate     = "password_reset.tmpl"
	AccountActivationTemplate = "account_activation.tmpl"
)

// AccountActivationData is the data AccountActivationTemplate expects.
type AccountActivationData struct {
	FullName   string
	Link       string
	TTLMinutes int
}

// PasswordResetData is the data PasswordResetTemplate expects.
type PasswordResetData struct {
	FullName   string
	Link       string
	TTLMinutes int
}

// Client sends a templated email. ctx bounds/cancels the underlying
// transport call (an HTTP request for BrevoClient; MailpitClient's plain
// SMTP call ignores it, since net/smtp has no context-aware API).
type Client interface {
	Send(ctx context.Context, to, templateFile string, data any) error
}

type message struct {
	subject   string
	plainBody string
	htmlBody  string
}

// renderTemplate executes the named template file from the embedded FS. Each
// template must define three blocks: "subject", "plainBody" and "htmlBody".
func renderTemplate(templateFile string, data any) (*message, error) {
	tmpl, err := template.New("email").ParseFS(templateFS, "templates/"+templateFile)
	if err != nil {
		return nil, err
	}

	plainTmpl, err := texttemplate.New("email").ParseFS(templateFS, "templates/"+templateFile)
	if err != nil {
		return nil, err
	}

	render := func(name string) (string, error) {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
			return "", err
		}
		return buf.String(), nil
	}

	renderPlain := func(name string) (string, error) {
		var buf bytes.Buffer
		if err := plainTmpl.ExecuteTemplate(&buf, name, data); err != nil {
			return "", err
		}
		return buf.String(), nil
	}

	msg := &message{}
	if msg.subject, err = render("subject"); err != nil {
		return nil, err
	}
	if msg.plainBody, err = renderPlain("plainBody"); err != nil {
		return nil, err
	}
	if msg.htmlBody, err = render("htmlBody"); err != nil {
		return nil, err
	}
	return msg, nil
}
