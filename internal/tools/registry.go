package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"go-agent-framework/internal/apis"
)

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

// DefaultRegistry 返回包含内置默认工具的注册表
func DefaultRegistry() *Registry {
	return NewRegistry(Bash{})
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
