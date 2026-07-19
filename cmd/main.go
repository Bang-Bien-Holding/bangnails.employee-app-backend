package main

import (
	"context"
	"log/slog"
	"os"
)

func main() {
	ctx := context.Background()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

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
