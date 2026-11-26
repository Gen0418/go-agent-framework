package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"
	"go-agent-framework/internal/tools"
)

type AgentLoop struct {
	SessionId int64 // 绑定会话Id
	cfg       config.Config
	client    *apis.Client
	tools     *tools.Registry
	msgs      []apis.Message // 本会话的对话历史
	db        *sql.DB
	opts      apis.StreamOptions
}

type InputFunc func(context.Context) (string, error)
type OutputFunc func(context.Context, string, OutputRole) error
type ConfirmFunc func(context.Context, string, string) (bool, error)

type OutputRole int

const (
	IsContent OutputRole = iota
	IsReasoningContent
	IsToolContent
)

func NewAgentLoop(cfg config.Config, db *sql.DB, opts apis.StreamOptions) *AgentLoop {
	l := &AgentLoop{
		db:    db,
		tools: tools.DefaultRegistry(),
	}
	l.SetConfig(cfg)
	l.SetOpts(opts)
	return l
}

// 更新会话级 opts
func (l *AgentLoop) SetOpts(opts apis.StreamOptions) {
	opts.Tools = slices.Clone(opts.Tools)
	l.opts = opts
}

// 更新应用级 cfg
func (l *AgentLoop) SetConfig(cfg config.Config) {
	l.cfg = cfg
	l.client = &apis.Client{
		Endpoint: cfg.Endpoint,
		APIKey:   cfg.APIKey,
		Model:    cfg.DefaultModel,
	}
}

func (l *AgentLoop) NewSession(ctx context.Context, title string) error {
	sessionId, err := storage.CreateSession(ctx, l.db, title)
	if err != nil {
		return err
	}

	l.SessionId = sessionId
	l.msgs = make([]apis.Message, 0)
	return nil
}

func (l *AgentLoop) History() []apis.Message {
	return l.msgs
}

// 将 Loop 绑定到会话
func (l *AgentLoop) BindSession(ctx context.Context, sessionId int64) error {
	msgs, err := LoadHistory(ctx, l.db, sessionId)
	if err != nil {
		return err
	}

	l.SessionId = sessionId
	l.msgs = msgs

	return nil
}

func LoadHistory(ctx context.Context, db *sql.DB, sessionId int64) ([]apis.Message, error) {
	// 查询sessionId
	_, err := storage.QuerySession(ctx, db, sessionId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sessionId %d 不存在: %w", sessionId, err)
	}
	if err != nil {
		return nil, err
	}

	// 载入msgs
	msgStorages, err := storage.QueryMessages(ctx, db, sessionId)
	if err != nil {
		return nil, err
	}

	var msgs []apis.Message
	for _, msgStorage := range msgStorages {
		var calls []apis.ToolCall
		if msgStorage.ToolCalls != "" {
			if err := json.Unmarshal([]byte(msgStorage.ToolCalls), &calls); err != nil {
				return nil, err
			}
		}
		msgs = append(msgs, apis.Message{
			Role:             msgStorage.Role,
			Content:          msgStorage.Content,
			ReasoningContent: msgStorage.ReasoningContent,
			ToolCalls:        calls,
			ToolCallId:       msgStorage.ToolCallId,
		})
	}

	return msgs, nil
}

// 推进对话轮次
func (l *AgentLoop) RunTurn(ctx context.Context, inputFunc InputFunc, outputFunc OutputFunc, confirmFunc ConfirmFunc) error {
	input, err := l.readInput(ctx, inputFunc) // 读取输入
	if err != nil {
		return err
	}
	if err := l.appendMessage(ctx, input); err != nil {
		return err
	}

	needReply := true
	for needReply { // 读取输出循环
		output, finishReason, err := l.readReply(ctx, outputFunc)
		if err != nil {
			if errors.Is(err, context.Canceled) && output.Role != "" {
				saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				defer saveCancel()
				if saveErr := l.appendMessage(saveCtx, output); saveErr != nil {
					return errors.Join(err, saveErr)
				}
			}
			return err
		}

		if finishReason != "tool_calls" {
			needReply = false // 非工具调用，进入下一轮

			if output.Role == "" {
				continue
			}
		}

		if err := l.appendMessage(ctx, output); err != nil { // 对话历史追加并储存
			return err
		}

		for i, call := range output.ToolCalls { // 遍历工具调用结果，追加并储存
			dir, err := os.Getwd()
			if err != nil {
				if saveErr := l.cancelRemainingToolCalls(ctx, output.ToolCalls[i:], outputFunc); saveErr != nil {
					return errors.Join(err, saveErr)
				}
				return err
			}
			toolOutput, err := l.runToolCall(ctx, call, dir, confirmFunc)
			if err != nil {
				if saveErr := l.cancelRemainingToolCalls(ctx, output.ToolCalls[i:], outputFunc); saveErr != nil {
					return errors.Join(err, saveErr)
				}
				return err
			}

			msg := apis.Message{
				Role:       "tool",
				Content:    toolOutput,
				ToolCallId: call.Id,
			}

			if err := l.appendMessage(ctx, msg); err != nil {
				return err
			}

			if err := outputFunc(ctx, toolOutput, IsToolContent); err != nil {
				return err
			}
		}
	}

	return nil
}

// 取消时补写工具调用结果
func (l *AgentLoop) cancelRemainingToolCalls(ctx context.Context, calls []apis.ToolCall, outputFunc OutputFunc) error {
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer saveCancel()

	for _, call := range calls {
		msg := apis.Message{
			Role:       "tool",
			Content:    "已取消",
			ToolCallId: call.Id,
		}
		if err := l.appendMessage(saveCtx, msg); err != nil {
			return err
		}
		_ = outputFunc(saveCtx, msg.Content, IsToolContent)
	}
	return nil
}

// 执行工具调用
// 返回字符串给模型处理，返回 error 表示执行异常
func (l *AgentLoop) runToolCall(ctx context.Context, call apis.ToolCall, dir string, confirm ConfirmFunc) (string, error) {
	reg := l.tools
	if reg == nil {
		reg = tools.DefaultRegistry()
	}
	return reg.Run(ctx, dir, call, tools.ConfirmFunc(confirm))
}

// 读取输入
func (l *AgentLoop) readInput(ctx context.Context, inputFunc InputFunc) (apis.Message, error) {
	input, err := inputFunc(ctx)
	if err != nil {
		return apis.Message{}, err
	}

	msg := apis.Message{
		Role:    "user",
		Content: input,
	}
	return msg, nil
}

// 读取模型回复
func (l *AgentLoop) readReply(ctx context.Context, outputFunc OutputFunc) (apis.Message, string, error) {
	var output, reasoningOutput strings.Builder // 输出内容缓冲区
	var calls []apis.ToolCall                   // 记录工具调用
	var finishReason string
	var reasoning bool

	remainContent := func() apis.Message {
		if output.Len() == 0 && reasoningOutput.Len() == 0 {
			return apis.Message{}
		}
		return apis.Message{
			Role:             "assistant",
			Content:          output.String(),
			ReasoningContent: reasoningOutput.String(),
		}
	}

	// 流式解析SSE
	for event, err := range l.client.StreamChat(ctx, l.msgs, l.opts) { // 解析sse流
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return remainContent(), "", ctx.Err()
			}
			return apis.Message{}, "", fmt.Errorf("流式对话失败: %w", err)
		}
		switch event.Kind {
		case apis.EventReasoning: // 思考内容
			if err := outputFunc(ctx, event.Text, IsReasoningContent); err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return remainContent(), "", ctx.Err()
				}
				return apis.Message{}, "", err
			}
			reasoning = true
			reasoningOutput.WriteString(event.Text)
		case apis.EventContent: // 输出内容
			splitMark := ""
			if reasoning {
				splitMark = "\n"
			}
			if err := outputFunc(ctx, splitMark+event.Text, IsContent); err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return remainContent(), "", ctx.Err()
				}
				return apis.Message{}, "", err
			}
			reasoning = false
			output.WriteString(event.Text)
		case apis.EventFinish: // 结束原因
			finishReason = event.FinishReason
			if event.FinishReason == "tool_calls" {
				calls = event.ToolCalls
			}
		case apis.EventDone: // 轮次结束标识
			msg := apis.Message{}
			if output.Len() > 0 || reasoningOutput.Len() > 0 || len(calls) > 0 {
				msg = apis.Message{
					Role:             "assistant",
					Content:          output.String(),
					ReasoningContent: reasoningOutput.String(),
					ToolCalls:        calls,
				}
			}
			return msg, finishReason, nil
		}
	}

	return apis.Message{}, "", io.ErrUnexpectedEOF
}

// 将 msg 追加至对话历史，并持久化储存
func (l *AgentLoop) appendMessage(ctx context.Context, msg apis.Message) error {
	callsString := "[]"
	if len(msg.ToolCalls) > 0 {
		data, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return fmt.Errorf("追加对话历史，工具调用转换失败: %w", err)
		}
		callsString = string(data)
	}

	msgStorage := storage.Message{
		SessionId:        l.SessionId,
		Role:             msg.Role,
		Content:          msg.Content,
		ReasoningContent: msg.ReasoningContent,
		ToolCalls:        callsString,
		ToolCallId:       msg.ToolCallId,
	}
	if err := storage.CreateMessage(ctx, l.db, msgStorage); err != nil { // 储存模型输出
		return fmt.Errorf("追加对话历史，储存失败: %w", err)
	}

	l.msgs = append(l.msgs, msg)

	return nil
}
