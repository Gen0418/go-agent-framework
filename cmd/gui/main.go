package main

import (
	"context"
	"log"

	// 	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/interaction/gui"
	"go-agent-framework/internal/storage"
	"go-agent-framework/internal/tools"
)

func main() {
	ctx := context.Background()
	const cfgPath = "config.json"
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	opts := apis.StreamOptions{
		Thinking:        true,
		ReasoningEffort: "low",
		Tools:           tools.DefaultRegistry().Definitions(),
	}
	db, err := storage.Open(ctx, "agent.db") // 初始化数据库连接
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := gui.Run(ctx, db, cfg, cfgPath, opts); err != nil {
		log.Fatal(err)
	}
}
