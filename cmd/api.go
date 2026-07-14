package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/employees"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
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
	logger *slog.Logger
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

		// odoo.NewFakeClient stands in for a real Odoo connection — no live
		// integration exists yet (see internal/odoo).
		odooClient := odoo.NewFakeClient()

		employeeService := employees.NewService(repo.New(app.db), app.mailer, odooClient)
		employeeHandler := employees.NewHandler(employeeService)
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

		// Public, unauthenticated — not nested under /employees since
		// it's not a CRUD action on a specific employee resource; the
		// token in the body identifies the employee.
		r.Post("/activate", employeeHandler.CompleteActivation)

		storesService := stores.NewService(app.db, odooClient)
		storesHandler := stores.NewHandler(storesService)
		r.Post("/stores/syncs", storesHandler.SyncStores)
		r.Get("/stores", storesHandler.ListStores)
		r.Get("/stores/{id}", storesHandler.GetStoreByID)
		r.Patch("/stores/{id}", storesHandler.PatchStore)
		r.Delete("/stores/{id}/wifi-whitelist", storesHandler.DeleteWifiWhitelistEntries)
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
