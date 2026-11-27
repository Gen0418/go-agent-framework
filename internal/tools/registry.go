package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"go-agent-framework/internal/apis"
)

const defaultCommandTimeout = 60 * time.Second

// ConfirmFunc 在执行工具前向用户确认，返回是否允许执行
type ConfirmFunc func(ctx context.Context, dir, detail string) (bool, error)

// Tool 统一工具契约
// 返回的字符串会作为工具结果交给模型处理
// 仅在程序级错误或上下文取消/中断时返回非 nil error。
type Tool interface {
	Definition() apis.Tool
	Run(ctx context.Context, dir, rawArgs string, confirm ConfirmFunc) (string, error)
}

// Registry 管理已注册的工具定义与分发执行
type Registry struct {
	defs  []apis.Tool
	tools map[string]Tool
}

func NewRegistry(list ...Tool) *Registry {
	r := &Registry{
		defs:  make([]apis.Tool, 0, len(list)),
		tools: make(map[string]Tool, len(list)),
	}
	for _, tool := range list {
		def := tool.Definition()
		r.defs = append(r.defs, def)
		r.tools[def.Function.Name] = tool
	}
	return r
}

// LocalTools 返回当前平台内置的本地命令工具列表
// 非 Windows 仅包含 bash；Windows 始终包含 powershell，检测到 Git Bash 时额外附加 bash
func LocalTools() []Tool {
	return defaultTools()
}

// DefaultRegistry 返回当前平台内置本地命令工具的注册表
func DefaultRegistry() *Registry {
	return NewRegistry(LocalTools()...)
}

func (r *Registry) Definitions() []apis.Tool {
	return slices.Clone(r.defs)
}

// Run 校验公共 JSON 对象格式并按名称分发给对应工具执行
func (r *Registry) Run(ctx context.Context, dir string, call apis.ToolCall, confirm ConfirmFunc) (string, error) {
	args := strings.TrimSpace(call.Function.Args)
	if !strings.HasPrefix(args, "{") || !json.Valid([]byte(args)) {
		return fmt.Sprintf("工具调用 %s 的参数必须是 JSON 对象", call.ID), nil
	}

	tool, ok := r.tools[call.Function.Name]
	if !ok {
		return "尚未实现", nil
	}

	return tool.Run(ctx, dir, args, confirm)
}

type commandArgs struct {
	Command string `json:"command"`
}

// 解析单命令参数、执行用户确认并调用底层命令执行器
// 统一 bash 与 powershell 的参数校验、确认拦截、取消传播与模型可见错误格式
func runConfirmedCommand(
	ctx context.Context,
	toolName, dir, rawArgs string,
	confirm ConfirmFunc,
	execFn func(ctx context.Context, dir, command string) (string, error),
) (string, error) {
	var args commandArgs
	decoder := json.NewDecoder(strings.NewReader(rawArgs))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return fmt.Sprintf("%s 参数解析错误: %v", toolName, err), nil
	}

	if strings.TrimSpace(args.Command) == "" {
		return fmt.Sprintf("%s 命令串为空", toolName), nil
	}

	isConfirmed, err := confirm(ctx, dir, args.Command)
	if err != nil {
		return "", err
	}
	if !isConfirmed {
		return "用户拒绝执行", nil
	}

	toolOutput, err := execFn(ctx, dir, args.Command)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		if toolOutput == "" {
			return fmt.Sprintf("%s 执行失败: %v", toolName, err), nil
		}
		return fmt.Sprintf("%s 执行失败: %v\n%s", toolName, err, toolOutput), nil
	}

	return toolOutput, nil
}

// 启动受超时与隐藏窗口保护的子进程并合并输出
func runSubprocess(ctx context.Context, dir string, timeout time.Duration, name string, args ...string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.WaitDelay = 3 * time.Second
	configureHiddenCmd(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() == nil && errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		return string(output), fmt.Errorf("命令执行超时（超过 %s）: %w", timeout, execCtx.Err())
	}
	return string(output), err
}
