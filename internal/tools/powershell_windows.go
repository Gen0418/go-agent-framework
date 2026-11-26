//go:build windows

package tools

import (
	"context"

	"go-agent-framework/internal/apis"
)

// PowerShell 经用户确认后在指定目录执行 Windows PowerShell 命令
type PowerShell struct{}

// 传给模型方的工具定义
func (PowerShell) Definition() apis.Tool {
	return apis.Tool{
		Type: "function",
		Function: apis.FunctionDef{
			Name:        "powershell",
			Description: "经用户确认后，在当前工作目录执行 Windows PowerShell 命令并返回输出。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "要执行的完整 PowerShell 命令。",
					},
				},
				"required":             []string{"command"},
				"additionalProperties": false,
			},
		},
	}
}

func (PowerShell) Run(ctx context.Context, dir, rawArgs string, confirm ConfirmFunc) (string, error) {
	return runConfirmedCommand(ctx, "powershell", dir, rawArgs, confirm, execPowerShell)
}

func execPowerShell(ctx context.Context, dir, command string) (string, error) {
	psPath, err := resolvePowerShellPath()
	if err != nil {
		return "", err
	}
	encoded := encodePowerShellCommand(command)
	return runSubprocess(ctx, dir, defaultCommandTimeout, psPath, "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded)
}
