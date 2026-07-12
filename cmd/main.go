package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/env"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()

	cfg := config{
		addr:        env.GetString("ADDR", ":8080"),
		frontendURL: env.GetString("APP_URL", "http://localhost:8081"),
		db: dbConfig{
			dsn: env.GetString("DATABASE_DSN", "host=localhost user=postgres password=postgres dbname=employees sslmode=disable"),
		},
	}

	// Logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Database
	pool, err := pgxpool.New(ctx, cfg.db.dsn)
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		panic(err)
	}

	connConfig := pool.Config().ConnConfig
	logger.Info("connected to database", "host", connConfig.Host, "port", connConfig.Port, "database", connConfig.Database)

	// Mailer
	mailClient, err := mailer.New(mailer.Config{
		Env:         env.GetString("APP_ENV", mailer.EnvDevelopment),
		FromEmail:   env.GetString("MAIL_FROM_EMAIL", "no-reply@bangnails.local"),
		FromName:    env.GetString("MAIL_FROM_NAME", "Bangnails"),
		MailpitAddr: env.GetString("MAILPIT_ADDR", "localhost:1025"),
		BrevoAPIKey: env.GetString("BREVO_API_KEY", ""),
	})
	if err != nil {
		panic(err)
	}

	api := application{
		config: cfg,
		db:     pool,
		mailer: mailClient,
		logger: logger,
	}

	if err := api.run(api.mount()); err != nil {
		logger.Error("server has failed to start", "error", err)
		os.Exit(1)
	}
}
