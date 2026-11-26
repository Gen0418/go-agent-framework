package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/interaction/cli"
	"go-agent-framework/internal/storage"
	"go-agent-framework/internal/tools"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	reg := tools.DefaultRegistry()
	opts := apis.StreamOptions{
		Thinking:        true,
		ReasoningEffort: "low",
		Tools:           reg.Definitions(),
	}
	client := &apis.Client{
		Endpoint: cfg.Endpoint,
		APIKey:   cfg.APIKey,
		Model:    cfg.DefaultModel,
	}
	store, err := storage.Open(ctx, "agent.db") // 初始化数据库连接
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	terminal := cli.New(os.Stdin, os.Stdout)
	mainLoop := agent.NewLoop(store, reg)
	selectDone := false // 选择或新建会话
	for !selectDone {
		sessionID, err := terminal.SelectSession(ctx)
		if err != nil || sessionID == -1 {
			if errors.Is(err, io.EOF) {
				fmt.Println("程序终止")
				return
			}
			continue
		}
		if sessionID == 0 {
			if err := mainLoop.NewSession(ctx, "test"); err != nil {
				log.Fatal(err)
			}
		} else {
			if err := mainLoop.BindSession(ctx, sessionID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					fmt.Printf("指定 sessionID %d 不存在\n", sessionID)
					continue
				}
				log.Fatal(err)
			}
			terminal.PrintHistory(ctx, mainLoop.History()) // 打印加载历史上下文
		}
		selectDone = true
	}

	for {
		input, err := terminal.ReadInput(ctx)
		if errors.Is(err, io.EOF) {
			fmt.Println("会话终止")
			break
		}
		if err != nil {
			log.Fatal(err)
		}

		if err := mainLoop.RunTurn(ctx, agent.TurnRequest{
			Input:   input,
			Client:  client,
			Opts:    opts,
			Output:  terminal.WriteOutput,
			Confirm: terminal.Confirm,
		}); err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Println("\n工具确认已中断，会话终止")
				break
			}
			log.Fatal(err)
		}
		fmt.Println()
	}

}
