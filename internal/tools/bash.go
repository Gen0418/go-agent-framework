package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"go-agent-framework/internal/apis"
)

const (
	bashEnvPath        = "SAISON_BASH_PATH"
	defaultBashTimeout = 60 * time.Second
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
		if toolOutput == "" {
			return fmt.Sprintf("bash 执行失败: %v", err), nil
		}
		return fmt.Sprintf("bash 执行失败: %v\n%s", err, toolOutput), nil
	}

	return toolOutput, nil
}

func execBash(ctx context.Context, dir, command string) (string, error) {
	return execBashWithTimeout(ctx, dir, command, defaultBashTimeout)
}

func execBashWithTimeout(ctx context.Context, dir, command string, timeout time.Duration) (string, error) {
	bashPath, err := resolveBashPath()
	if err != nil {
		return "", err
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, bashPath, "-c", command)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.WaitDelay = 3 * time.Second
	configureBashCmd(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() == nil && errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		return string(output), fmt.Errorf("命令执行超时（超过 %s）: %w", timeout, execCtx.Err())
	}
	return string(output), err
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// 解析 Windows 下的 Git Bash 路径
// 按环境变量 -> git 同级目录 -> 常见安装目录 -> 非 System32 的 PATH 顺序查找，避免命中 WSL 占位 bash.exe
func findWindowsBash(getenv func(string) string, lookPath func(string) (string, error), isFile func(string) bool) (string, error) {
	if custom := strings.TrimSpace(getenv(bashEnvPath)); custom != "" {
		if isFile(custom) {
			return custom, nil
		}
		return "", fmt.Errorf("环境变量 %s 指定的路径不存在: %s", bashEnvPath, custom)
	}

	candidates := make([]string, 0, 16)
	addGitRoot := func(root string) {
		if root == "" {
			return
		}
		// 优先 Git\bin\bash.exe（其启动器会自动补齐 usr\bin 到 PATH，保证 ls/cat 等命令可用）
		candidates = append(candidates,
			winJoin(root, "bin", "bash.exe"),
			winJoin(root, "usr", "bin", "bash.exe"),
		)
	}

	if gitPath, err := lookPath("git"); err == nil && gitPath != "" {
		dir := winDir(gitPath)
		for range 3 {
			if dir == "" {
				break
			}
			addGitRoot(dir)
			parent := winDir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	for _, envRoot := range []struct {
		env string
		sub []string
	}{
		{"ProgramFiles", []string{"Git"}},
		{"ProgramFiles(x86)", []string{"Git"}},
		{"LocalAppData", []string{"Programs", "Git"}},
		{"USERPROFILE", []string{"scoop", "apps", "git", "current"}},
	} {
		if base := strings.TrimSpace(getenv(envRoot.env)); base != "" {
			addGitRoot(winJoin(append([]string{base}, envRoot.sub...)...))
		}
	}

	for _, candidate := range candidates {
		if isFile(candidate) {
			return candidate, nil
		}
	}

	for _, name := range []string{"bash.exe", "bash"} {
		if found, err := lookPath(name); err == nil && found != "" && !isWindowsSystemBash(found) && isFile(found) {
			return found, nil
		}
	}

	return "", fmt.Errorf("未找到 Git Bash（已跳过系统自带的 WSL bash.exe），请安装 Git for Windows 或设置环境变量 %s", bashEnvPath)
}

func isWindowsSystemBash(p string) bool {
	norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"))
	return strings.HasSuffix(norm, "/system32/bash.exe") ||
		strings.HasSuffix(norm, "/sysnative/bash.exe") ||
		strings.HasSuffix(norm, "/syswow64/bash.exe")
}

func winJoin(parts ...string) string {
	var cleaned []string
	for i, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		p = strings.ReplaceAll(p, "/", "\\")
		if i > 0 {
			p = strings.TrimLeft(p, "\\")
		}
		p = strings.TrimRight(p, "\\")
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	return strings.Join(cleaned, "\\")
}

func winDir(p string) string {
	norm := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(p), "/", "\\"), "\\")
	idx := strings.LastIndex(norm, "\\")
	if idx <= 0 {
		return ""
	}
	return norm[:idx]
}
