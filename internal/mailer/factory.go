package mailer

import "fmt"

const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config holds every value the factory might need; only the fields relevant
// to the selected Env are actually read.
type Config struct {
	Env string // EnvDevelopment (default), EnvStaging, or EnvProduction

	FromEmail string
	FromName  string

	MailpitAddr string // used for development/staging, e.g. "localhost:1025"

	BrevoAPIKey string // required for production
}

// New selects the transport for cfg.Env: Mailpit for development/staging,
// Brevo for production. Fails fast rather than silently falling back to
// Mailpit if production is missing its API key, or if Env is unrecognized.
func New(cfg Config) (Client, error) {
	switch cfg.Env {
	case EnvProduction:
		if cfg.BrevoAPIKey == "" {
			return nil, fmt.Errorf("mailer: BREVO_API_KEY is required when APP_ENV=%s", EnvProduction)
		}
		if cfg.FromEmail == "" {
			return nil, fmt.Errorf("mailer: FromEmail is required when APP_ENV=%s", EnvProduction)
		}
		return NewBrevo(cfg.BrevoAPIKey, cfg.FromEmail, cfg.FromName), nil
	case EnvDevelopment, EnvStaging, "":
		return NewMailpit(cfg.MailpitAddr, cfg.FromEmail, cfg.FromName), nil
	default:
		return nil, fmt.Errorf("mailer: unknown APP_ENV %q", cfg.Env)
	}
}
