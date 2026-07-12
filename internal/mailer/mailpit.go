package mailer

import (
	"context"
	"fmt"
	"mime"
	"net/smtp"
	"strings"
)

// MailpitClient delivers email over plain SMTP. It targets a Mailpit server
// (https://mailpit.axllent.org), which captures mail for local inspection and
// accepts unauthenticated, non-TLS SMTP. It is the dev/staging transport.
type MailpitClient struct {
	addr      string // SMTP host:port, e.g. "localhost:1025"
	fromEmail string
	fromName  string
}

func NewMailpit(addr, fromEmail, fromName string) *MailpitClient {
	return &MailpitClient{addr: addr, fromEmail: fromEmail, fromName: fromName}
}

func (c *MailpitClient) Send(_ context.Context, to, templateFile string, data any) error {
	msg, err := renderTemplate(templateFile, data)
	if err != nil {
		return err
	}

	raw := buildMIME(c.fromName, c.fromEmail, to, msg)
	return smtp.SendMail(c.addr, nil, c.fromEmail, []string{to}, raw)
}

// buildMIME assembles a multipart/alternative message carrying both the plain
// and HTML bodies. The subject is RFC 2047 encoded so non-ASCII (the templates
// are Vietnamese) survives transport.
func buildMIME(fromName, fromEmail, to string, msg *message) []byte {
	const boundary = "bangnails-boundary-9f2a1c"

	var from string
	if fromName != "" {
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", fromName), fromEmail)
	} else {
		from = fromEmail
	}

	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", msg.subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n\r\n")
	b.WriteString(msg.plainBody + "\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n\r\n")
	b.WriteString(msg.htmlBody + "\r\n")

	b.WriteString("--" + boundary + "--\r\n")

	return []byte(b.String())
}
