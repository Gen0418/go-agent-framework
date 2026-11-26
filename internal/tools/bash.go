package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"go-agent-framework/internal/apis"
)

type bashArgs struct {
	Command string `json:"command"`
}

// Bash 经用户确认后在指定目录执行 Bash 命令
type Bash struct{}

// 传给模型方的工具定义
func (Bash) Definition() apis.Tool {
	return apis.Tool{
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
	}
}

func (Bash) Run(ctx context.Context, dir, rawArgs string, confirm ConfirmFunc) (string, error) {
	var args bashArgs
	decoder := json.NewDecoder(strings.NewReader(rawArgs))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return fmt.Sprintf("bash 参数解析错误: %v", err), nil
	}

	if strings.TrimSpace(args.Command) == "" {
		return "bash 命令串为空", nil
	}

	isConfirmed, err := confirm(ctx, dir, args.Command)
	if err != nil {
		return "", err
	}
	if !isConfirmed {
		return "用户拒绝执行", nil
	}

	toolOutput, err := execBash(ctx, dir, args.Command)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return fmt.Sprintf("bash 执行失败: %v\n%s", err, toolOutput), nil
	}

	return toolOutput, nil
}

func execBash(ctx context.Context, dir, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	return string(output), err
}
