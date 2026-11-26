package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/tools"
)

// Store 抽象会话与消息持久化依赖
type Store interface {
	CreateSession(ctx context.Context, title string) (int64, error)
	AppendMessage(ctx context.Context, sessionId int64, msg apis.Message) error
	LoadHistory(ctx context.Context, sessionId int64) ([]apis.Message, error)
}

type AgentLoop struct {
	sessionId int64 // 绑定会话Id
	tools     *tools.Registry
	msgs      []apis.Message // 本会话的对话历史
	store     Store
}

type OutputFunc func(ctx context.Context, event OutputEvent) error
type ConfirmFunc func(ctx context.Context, dir, command string) (bool, error)

type OutputKind int

const (
	OutputReasoning OutputKind = iota
	OutputContent
	OutputToolCall
	OutputToolResult
)

// OutputEvent 描述单次流式或工具执行输出事件
type OutputEvent struct {
	Kind       OutputKind
	Text       string
	ToolCall   *apis.ToolCall
	ToolCallId string
}

// TurnRequest 描述推进单轮对话所需的输入、客户端、流式选项与回调
type TurnRequest struct {
	Input   string
	Client  *apis.Client
	Opts    apis.StreamOptions
	WorkDir string // 为空时默认使用当前工作目录
	Output  OutputFunc
	Confirm ConfirmFunc
}

func NewAgentLoop(store Store, reg *tools.Registry) *AgentLoop {
	if reg == nil {
		reg = tools.DefaultRegistry()
	}
	return &AgentLoop{
		store: store,
		tools: reg,
	}
}

func (l *AgentLoop) SessionId() int64 {
	return l.sessionId
}

func (l *AgentLoop) NewSession(ctx context.Context, title string) error {
	sessionId, err := l.store.CreateSession(ctx, title)
	if err != nil {
		return err
	}

	l.sessionId = sessionId
	l.msgs = make([]apis.Message, 0)
	return nil
}

func (l *AgentLoop) History() []apis.Message {
	return l.msgs
}

// 将 Loop 绑定到会话
func (l *AgentLoop) BindSession(ctx context.Context, sessionId int64) error {
	msgs, err := l.store.LoadHistory(ctx, sessionId)
	if err != nil {
		return err
	}

	l.sessionId = sessionId
	l.msgs = msgs

	return nil
}

// 推进对话轮次
func (l *AgentLoop) RunTurn(ctx context.Context, req TurnRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.Client == nil {
		return errors.New("缺少模型客户端")
	}

	opts := req.Opts
	opts.Tools = slices.Clone(opts.Tools)
	outputFunc := req.Output
	if outputFunc == nil {
		outputFunc = func(context.Context, OutputEvent) error { return nil }
	}

	if err := l.appendMessage(ctx, apis.Message{Role: "user", Content: req.Input}); err != nil {
		return err
	}

	for {
		output, finishReason, err := l.readReply(ctx, req.Client, opts, outputFunc)
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
			if output.Role != "" {
				if err := l.appendMessage(ctx, output); err != nil {
					return err
				}
			}
			return nil
		}

		if err := l.appendMessage(ctx, output); err != nil { // 对话历史追加并储存
			return err
		}

		for _, call := range output.ToolCalls { // 实时通知本轮请求的工具调用
			callCopy := call
			if err := outputFunc(ctx, OutputEvent{
				Kind:     OutputToolCall,
				ToolCall: &callCopy,
			}); err != nil {
				if saveErr := l.cancelRemainingToolCalls(ctx, output.ToolCalls, outputFunc); saveErr != nil {
					return errors.Join(err, saveErr)
				}
				return err
			}
		}

		dir := req.WorkDir
		if dir == "" {
			dir, err = os.Getwd()
			if err != nil {
				if saveErr := l.cancelRemainingToolCalls(ctx, output.ToolCalls, outputFunc); saveErr != nil {
					return errors.Join(err, saveErr)
				}
				return err
			}
		}

		for i, call := range output.ToolCalls { // 遍历工具调用结果，追加并储存
			toolOutput, err := l.runToolCall(ctx, call, dir, req.Confirm)
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

			if err := outputFunc(ctx, OutputEvent{
				Kind:       OutputToolResult,
				Text:       toolOutput,
				ToolCallId: call.Id,
			}); err != nil {
				if saveErr := l.cancelRemainingToolCalls(ctx, output.ToolCalls[i+1:], outputFunc); saveErr != nil {
					return errors.Join(err, saveErr)
				}
				return err
			}
		}
	}
}

// 取消时补写工具调用结果
func (l *AgentLoop) cancelRemainingToolCalls(ctx context.Context, calls []apis.ToolCall, outputFunc OutputFunc) error {
	if len(calls) == 0 {
		return nil
	}
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
		_ = outputFunc(saveCtx, OutputEvent{
			Kind:       OutputToolResult,
			Text:       msg.Content,
			ToolCallId: call.Id,
		})
	}
	return nil
}

// 执行工具调用：返回字符串交由模型处理，返回 error 表示程序级或取消中断错误
func (l *AgentLoop) runToolCall(ctx context.Context, call apis.ToolCall, dir string, confirm ConfirmFunc) (string, error) {
	reg := l.tools
	if reg == nil {
		reg = tools.DefaultRegistry()
	}
	return reg.Run(ctx, dir, call, tools.ConfirmFunc(confirm))
}

// 读取模型回复
func (l *AgentLoop) readReply(ctx context.Context, client *apis.Client, opts apis.StreamOptions, outputFunc OutputFunc) (apis.Message, string, error) {
	var output, reasoningOutput strings.Builder // 输出内容缓冲区
	var calls []apis.ToolCall                   // 记录工具调用
	var finishReason string

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
	for event, err := range client.StreamChat(ctx, l.msgs, opts) { // 解析sse流
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return remainContent(), "", ctx.Err()
			}
			return apis.Message{}, "", fmt.Errorf("流式对话失败: %w", err)
		}
		switch event.Kind {
		case apis.EventReasoning: // 思考内容
			if err := outputFunc(ctx, OutputEvent{Kind: OutputReasoning, Text: event.Text}); err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return remainContent(), "", ctx.Err()
				}
				return apis.Message{}, "", err
			}
			reasoningOutput.WriteString(event.Text)
		case apis.EventContent: // 输出内容
			if err := outputFunc(ctx, OutputEvent{Kind: OutputContent, Text: event.Text}); err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return remainContent(), "", ctx.Err()
				}
				return apis.Message{}, "", err
			}
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
	if err := l.store.AppendMessage(ctx, l.sessionId, msg); err != nil {
		return fmt.Errorf("追加对话历史，储存失败: %w", err)
	}

	l.msgs = append(l.msgs, msg)
	return nil
}
