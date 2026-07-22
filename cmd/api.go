package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/auth"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/employees"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/env"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/positions"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/stores"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
)

type application struct {
	config config
	db     *pgxpool.Pool
	mailer mailer.Client
	odoo   odoo.Client
	logger *slog.Logger
}

// buildApplication wires up the real application exactly as main() runs
// it in production: config from env, a live Postgres pool, the real mailer,
// and the real Odoo HTTP client. Factored out of main() so the e2e test
// suite (cmd/e2e_test.go) exercises the identical wiring rather than a
// hand-rolled approximation of it. The caller owns closing the returned
// application's db pool.
func buildApplication(ctx context.Context, logger *slog.Logger) (*application, error) {
	cfg := config{
		addr:        env.GetString("ADDR", ":8080"),
		frontendURL: env.GetString("APP_URL", "http://localhost:8081"),
		db: dbConfig{
			dsn: env.GetString("DATABASE_DSN", "host=localhost user=postgres password=postgres dbname=employees sslmode=disable"),
		},
	}

	pool, err := pgxpool.New(ctx, cfg.db.dsn)
	if err != nil {
		return nil, fmt.Errorf("build application: connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("build application: ping database: %w", err)
	}

	connConfig := pool.Config().ConnConfig
	logger.Info("connected to database", "host", connConfig.Host, "port", connConfig.Port, "database", connConfig.Database)

	mailClient, err := mailer.New(mailer.Config{
		Env:         env.GetString("APP_ENV", mailer.EnvDevelopment),
		FromEmail:   env.GetString("MAIL_FROM_EMAIL", "no-reply@bangnails.local"),
		FromName:    env.GetString("MAIL_FROM_NAME", "Bangnails"),
		MailpitAddr: env.GetString("MAILPIT_ADDR", "localhost:1025"),
		BrevoAPIKey: env.GetString("BREVO_API_KEY", ""),
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("build application: build mailer: %w", err)
	}

	odooClient, err := odoo.NewHTTPClient(odoo.Config{
		BaseURL:      env.GetString("ODOO_BASE_URL", ""),
		ClientID:     env.GetString("ODOO_CLIENT_ID", ""),
		ClientSecret: env.GetString("ODOO_CLIENT_SECRET", ""),
		Username:     env.GetString("ODOO_USERNAME", ""),
		Password:     env.GetString("ODOO_PASSWORD", ""),
		Database:     env.GetString("ODOO_DATABASE", ""),
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("build application: build odoo client: %w", err)
	}

	return &application{
		config: cfg,
		db:     pool,
		mailer: mailClient,
		odoo:   odooClient,
		logger: logger,
	}, nil
}

func (app *application) mount() http.Handler {
	r := chi.NewRouter()

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{app.config.frontendURL},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// A good base middleware stack
	r.Use(middleware.RequestID)
	r.Use(middleware.ClientIPFromRemoteAddr) // pick one ClientIPFrom* based on your infra, see below
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Set a timeout value on the request context (ctx), that will signal
	// through ctx.Done() that the request has timed out and further
	// processing should be stopped.
	r.Use(middleware.Timeout(60 * time.Second))

	r.Route("/v1", func(r chi.Router) {
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			if _, err := w.Write([]byte("all good")); err != nil {
				app.logger.Error("health handler: write response", "error", err)
			}
		})

		authService := auth.NewService(app.db)
		authHandler := auth.NewHandler(authService)
		r.Post("/auth/login", authHandler.Login)
		r.Post("/auth/logout", authHandler.Logout)
		r.Post("/auth/heartbeat", authHandler.Heartbeat)

		employeeHandler := employees.NewHandler(employees.NewService(app.db, app.mailer, app.odoo))
		positionsHandler := positions.NewHandler(positions.NewService(app.db))
		storesHandler := stores.NewHandler(stores.NewService(app.db, app.odoo))

		// Public, unauthenticated — not nested under /employees since it's
		// not a CRUD action on a specific employee resource; the token in
		// the body identifies the employee. Deliberately outside the
		// AdminOnly group below (ADR-0015): activation proves email
		// ownership, not admin identity.
		r.Post("/activate", employeeHandler.CompleteActivation)

		// Every existing admin endpoint (Employees, Stores, Positions) now
		// requires a valid Admin Session (ADR-0015, issue #25) — the gate
		// lives entirely at this routing layer, via auth.AdminOnly,
		// without touching these packages' own Service/Handler internals.
		r.Group(func(r chi.Router) {
			r.Use(auth.AdminOnly(authService))

			r.Post("/employees", employeeHandler.CreateEmployee)
			r.Get("/employees", employeeHandler.ListEmployees)
			r.Get("/employees/{id}", employeeHandler.GetEmployeeByID)
			r.Put("/employees/{id}", employeeHandler.UpdateEmployee)
			r.Patch("/employees/{id}/status", employeeHandler.SetEmployeeActive)
			r.Patch("/employees/{id}/password", employeeHandler.SetEmployeePassword)
			r.Delete("/employees/{id}", employeeHandler.DeleteEmployee)
			r.Delete("/employees", employeeHandler.BulkDeleteEmployees)
			r.Post("/employees/password-reset-links", employeeHandler.BulkSendPasswordResetLinks)
			r.Post("/employees/syncs", employeeHandler.SyncEmployees)
			r.Get("/employees/syncs", employeeHandler.SyncStatus)

			r.Post("/positions", positionsHandler.CreatePosition)
			r.Get("/positions", positionsHandler.ListPositions)
			r.Put("/positions/{id}", positionsHandler.UpdatePosition)
			r.Delete("/positions/{id}", positionsHandler.DeletePosition)
			r.Delete("/positions", positionsHandler.BulkDeletePositions)
			r.Get("/positions/{id}/employees", positionsHandler.GetPositionEmployees)
			r.Put("/positions/{id}/employees", positionsHandler.SetPositionEmployees)

			r.Post("/stores/syncs", storesHandler.SyncStores)
			r.Get("/stores/syncs", storesHandler.SyncStatus)
			r.Get("/stores", storesHandler.ListStores)
			r.Patch("/stores", storesHandler.BulkSetWifiWhitelistEnabled)
			r.Get("/stores/{id}", storesHandler.GetStoreByID)
			r.Patch("/stores/{id}", storesHandler.PatchStore)
			r.Patch("/stores/{id}/wifi-whitelist-enabled", storesHandler.SetStoreWifiWhitelistEnabled)
			r.Delete("/stores/{id}/wifi-whitelist", storesHandler.DeleteWifiWhitelistEntries)
		})
	})

	return r
}

func (app *application) run(h http.Handler) error {
	srv := &http.Server{
		Addr:         app.config.addr,
		Handler:      h,
		WriteTimeout: time.Second * 30,
		ReadTimeout:  time.Second * 10,
		IdleTimeout:  time.Minute,
	}

	shutdown := make(chan error)

	go func() {
		quit := make(chan os.Signal, 1)

		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		s := <-quit

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		app.logger.Info("signal caught", "signal", s.String())

		shutdown <- srv.Shutdown(ctx)
	}()

	app.logger.Info("server has started", "addr", app.config.addr)

	err := srv.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	err = <-shutdown
	if err != nil {
		return err
	}

	app.logger.Info("server has stopped", "addr", app.config.addr)

	return nil
}

type config struct {
	addr        string
	frontendURL string
	db          dbConfig
}

type dbConfig struct {
	dsn string
}
