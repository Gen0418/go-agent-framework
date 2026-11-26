package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-agent-framework/internal/apis"
)

func TestExecBashWorkingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "工作 目录")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("目标文件"), 0600); err != nil {
		t.Fatal(err)
	}

	output, err := ExecBash(t.Context(), dir, "cat input.txt")
	if err != nil || output != "目标文件" {
		t.Fatalf("输出 = %q，错误 = %v", output, err)
	}
}

func TestRegistryDefaultDefinitions(t *testing.T) {
	reg := DefaultRegistry()
	defs := reg.Definitions()
	if len(defs) != 1 || defs[0].Function.Name != "bash" {
		t.Fatalf("默认工具定义 = %+v，预期包含 bash", defs)
	}

	// 修改返回的切片不应污染注册表
	defs[0].Function.Name = "mutated"
	if reg.Definitions()[0].Function.Name != "bash" {
		t.Fatal("修改 Definitions 返回切片污染了注册表")
	}
}

func TestRegistryRunInvalidArguments(t *testing.T) {
	reg := DefaultRegistry()
	unexpectedConfirm := func(context.Context, string, string) (bool, error) {
		t.Fatal("参数非法或未知工具时不应触发用户确认")
		return false, nil
	}

	tests := []struct {
		name string
		tool string
		args string
		want string
	}{
		{"残缺对象", "bash", `{"command":`, "必须是 JSON 对象"},
		{"数组", "bash", `[]`, "必须是 JSON 对象"},
		{"类型错误", "bash", `{"command":12}`, "参数解析错误"},
		{"未知字段", "bash", `{"command":"pwd","cwd":"/tmp"}`, `unknown field "cwd"`},
		{"缺少命令", "bash", `{}`, "命令串为空"},
		{"空白命令", "bash", `{"command":"  "}`, "命令串为空"},
		{"未知工具", "unknown_tool", `{}`, "尚未实现"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			call := apis.ToolCall{Id: "a", Function: apis.ToolCallFunction{Name: tc.tool, Args: tc.args}}
			got, err := reg.Run(t.Context(), t.TempDir(), call, unexpectedConfirm)
			if err != nil || !strings.Contains(got, tc.want) {
				t.Fatalf("结果 = %q，错误 = %v，预期包含 %q", got, err, tc.want)
			}
		})
	}
}

func TestBashRunConfirmAndExecution(t *testing.T) {
	reg := DefaultRegistry()
	call := apis.ToolCall{
		Id:       "a",
		Type:     "function",
		Function: apis.ToolCallFunction{Name: "bash", Args: `{"command":"echo diagnostic >&2; exit 7"}`},
	}

	t.Run("用户拒绝返回模型可见提示", func(t *testing.T) {
		got, err := reg.Run(t.Context(), t.TempDir(), call, func(context.Context, string, string) (bool, error) {
			return false, nil
		})
		if err != nil || got != "用户拒绝执行" {
			t.Fatalf("结果 = %q，错误 = %v", got, err)
		}
	})

	t.Run("确认中断返回程序级错误", func(t *testing.T) {
		_, err := reg.Run(t.Context(), t.TempDir(), call, func(context.Context, string, string) (bool, error) {
			return false, io.EOF
		})
		if !errors.Is(err, io.EOF) {
			t.Fatalf("错误 = %v，预期 io.EOF", err)
		}
	})

	t.Run("命令非零退出返回带输出的字符串", func(t *testing.T) {
		got, err := reg.Run(t.Context(), t.TempDir(), call, func(context.Context, string, string) (bool, error) {
			return true, nil
		})
		if err != nil || !strings.Contains(got, "exit status 7") || !strings.Contains(got, "diagnostic") {
			t.Fatalf("结果 = %q，错误 = %v", got, err)
		}
	})
}
