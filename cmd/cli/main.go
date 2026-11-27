package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/interaction/cli"
	"go-agent-framework/internal/storage"
	"go-agent-framework/internal/tools"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()
	client := &apis.Client{
		Endpoint: cfg.Endpoint,
		APIKey:   cfg.APIKey,
		Model:    cfg.DefaultModel,
	}
	allTools := append(tools.LocalTools(),
		tools.WebSearch{Client: func() *apis.Client { return client }},
		tools.WebFetch{Client: func() *apis.Client { return client }},
	)
	reg := tools.NewRegistry(allTools...)
	opts := apis.StreamOptions{
		Thinking:        true,
		ReasoningEffort: "low",
		Tools:           reg.Definitions(),
	}
	store, err := storage.Open(ctx, "agent.db")
	if err != nil {
		return err
	}
	defer store.Close()

	terminal := cli.New(os.Stdin, os.Stdout)
	mainLoop := agent.NewLoop(store, reg)
	if err := chooseSession(ctx, terminal, mainLoop); err != nil {
		if errors.Is(err, io.EOF) {
			fmt.Println("程序终止")
			return nil
		}
		return err
	}

	for {
		input, err := terminal.ReadInput(ctx)
		if errors.Is(err, io.EOF) {
			fmt.Println("会话终止")
			return nil
		}
		if err != nil {
			return err
		}

		// 仅在单轮生成期间捕获 Ctrl-C，取消本轮并恢复默认信号处理，避免空等输入时阻塞退出
		turnCtx, stopTurn := signal.NotifyContext(ctx, os.Interrupt)
		stopAfter := context.AfterFunc(turnCtx, stopTurn)
		err = mainLoop.RunTurn(turnCtx, agent.TurnRequest{
			Input:   input,
			Client:  client,
			Opts:    opts,
			Output:  terminal.WriteOutput,
			Confirm: terminal.Confirm,
		})
		stopAfter()
		stopTurn()

		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Println("\n已取消当前回复")
				continue
			}
			if errors.Is(err, io.EOF) {
				fmt.Println("\n工具确认已中断，会话终止")
				return nil
			}
			return err
		}
		fmt.Println()
	}
}

func chooseSession(ctx context.Context, terminal *cli.CLI, loop *agent.Loop) error {
	for {
		sessionID, isNew, err := terminal.SelectSession(ctx)
		if err != nil {
			if errors.Is(err, cli.ErrInvalidSession) {
				fmt.Println("请输入 new 或有效的正整数 sessionID")
				continue
			}
			return err
		}
		if isNew {
			return loop.NewSession(ctx, "test")
		}
		if err := loop.BindSession(ctx, sessionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fmt.Printf("指定 sessionID %d 不存在\n", sessionID)
				continue
			}
			return err
		}
		return terminal.PrintHistory(ctx, loop.History())
	}
}
