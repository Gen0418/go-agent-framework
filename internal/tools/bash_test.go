package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	output, err := execBash(t.Context(), dir, "cat input.txt")
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
			call := apis.ToolCall{ID: "a", Function: apis.ToolCallFunction{Name: tc.tool, Args: tc.args}}
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
		ID:       "a",
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

func TestExecBashTimeout(t *testing.T) {
	_, err := execBashWithTimeout(t.Context(), t.TempDir(), "sleep 5", 80*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时命令返回错误 = %v，预期包装 context.DeadlineExceeded", err)
	}
}

func TestFindWindowsBash(t *testing.T) {
	makeFileSet := func(paths ...string) func(string) bool {
		set := make(map[string]bool, len(paths))
		for _, p := range paths {
			set[strings.ToLower(p)] = true
		}
		return func(p string) bool {
			return set[strings.ToLower(p)]
		}
	}
	makeEnv := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	makeLook := func(m map[string]string) func(string) (string, error) {
		return func(name string) (string, error) {
			if p, ok := m[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		}
	}

	t.Run("环境变量显式路径优先", func(t *testing.T) {
		want := `D:\Custom\Git\bin\bash.exe`
		got, err := findWindowsBash(
			makeEnv(map[string]string{bashEnvPath: want}),
			makeLook(nil),
			makeFileSet(want),
		)
		if err != nil || got != want {
			t.Fatalf("got = (%q, %v)，预期 %q", got, err, want)
		}
	})

	t.Run("环境变量路径不存在直接报错", func(t *testing.T) {
		_, err := findWindowsBash(
			makeEnv(map[string]string{bashEnvPath: `D:\Missing\bash.exe`}),
			makeLook(nil),
			makeFileSet(),
		)
		if err == nil || !strings.Contains(err.Error(), bashEnvPath) {
			t.Fatalf("预期环境变量路径错误，实际 = %v", err)
		}
	})

	t.Run("通过 git 位置推算同目录 Git Bash 并优先 bin 路径", func(t *testing.T) {
		want := `D:\Tools\Git\bin\bash.exe`
		got, err := findWindowsBash(
			makeEnv(nil),
			makeLook(map[string]string{
				"git":  `D:\Tools\Git\cmd\git.exe`,
				"bash": `C:\Windows\System32\bash.exe`,
			}),
			makeFileSet(want, `D:\Tools\Git\usr\bin\bash.exe`, `C:\Windows\System32\bash.exe`),
		)
		if err != nil || got != want {
			t.Fatalf("got = (%q, %v)，预期 %q", got, err, want)
		}
	})

	t.Run("git 不在 PATH 时回退 ProgramFiles 并跳过 System32 WSL bash", func(t *testing.T) {
		want := `C:\Program Files\Git\bin\bash.exe`
		got, err := findWindowsBash(
			makeEnv(map[string]string{"ProgramFiles": `C:\Program Files`}),
			makeLook(map[string]string{"bash": `C:\WINDOWS\System32\bash.exe`}),
			makeFileSet(want, `C:\WINDOWS\System32\bash.exe`),
		)
		if err != nil || got != want {
			t.Fatalf("got = (%q, %v)，预期 %q", got, err, want)
		}
	})

	t.Run("仅存在 System32 WSL bash 时拒绝命中并返回清晰错误", func(t *testing.T) {
		_, err := findWindowsBash(
			makeEnv(nil),
			makeLook(map[string]string{
				"bash.exe": `C:\Windows\System32\bash.exe`,
				"bash":     `C:\Windows\System32\bash.exe`,
			}),
			makeFileSet(`C:\Windows\System32\bash.exe`),
		)
		if err == nil || !strings.Contains(err.Error(), "Git Bash") {
			t.Fatalf("仅存在 System32 bash 时应报错提示安装 Git Bash，实际 = %v", err)
		}
	})

	t.Run("PATH 中非 System32 的独立 bash 可作为兜底", func(t *testing.T) {
		want := `C:\msys64\usr\bin\bash.exe`
		got, err := findWindowsBash(
			makeEnv(nil),
			makeLook(map[string]string{"bash.exe": want}),
			makeFileSet(want),
		)
		if err != nil || got != want {
			t.Fatalf("got = (%q, %v)，预期 %q", got, err, want)
		}
	})
}
