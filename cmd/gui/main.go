package main

import (
	"context"
	"log"

	// 	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/interaction/gui"
	"go-agent-framework/internal/storage"
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
		Tools: []apis.Tool{
			{
				Type: "function",
				Function: apis.FunctionDef{
					Name:        "bash",
					Description: "经用户确认后，在当前工作目录执行 Bash 命令并返回输出。",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"command": map[string]any{
								"type":        "string",
								"description": "要执行的完整 Bash 命令。",
							},
						},
						"required":             []string{"command"},
						"additionalProperties": false,
					},
				},
			},
		},
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
