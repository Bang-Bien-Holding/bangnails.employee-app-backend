package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/joho/godotenv"
)

func main() {
	ctx := context.Background()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Loads .env into the process environment for local development; a
	// missing file (the case in every real deployment, which sets env vars
	// directly) is not an error.
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("failed to load .env file", "error", err)
	}

	api, err := buildApplication(ctx, logger)
	if err != nil {
		panic(err)
	}
	defer api.db.Close()

	if err := api.run(api.mount()); err != nil {
		logger.Error("server has failed to start", "error", err)
		os.Exit(1)
	}
}
