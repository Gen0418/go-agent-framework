package main

import (
	"context"
	"log"

	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/interaction/gui"
	"go-agent-framework/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()
	const cfgPath = "config.json"
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	opts := apis.StreamOptions{
		Thinking:        true,
		ReasoningEffort: "low",
	}
	store, err := storage.Open(ctx, "agent.db")
	if err != nil {
		return err
	}
	defer store.Close()

	return gui.Run(ctx, store, cfg, cfgPath, opts)
}
