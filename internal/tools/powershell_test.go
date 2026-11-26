package tools

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"go-agent-framework/internal/apis"
)

func TestEncodePowerShellCommand(t *testing.T) {
	cmd := "Get-ChildItem \"工作 目录\"\nWrite-Output '你好'"
	encoded := encodePowerShellCommand(cmd)

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("解码 Base64 失败: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE 字节长度应为偶数，实际 %d", len(raw))
	}

	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	decoded := string(utf16.Decode(units))

	if !strings.HasPrefix(decoded, powershellUTF8Pfx) {
		t.Fatalf("解码脚本缺少 UTF-8 输出前缀: %q", decoded)
	}
	if !strings.HasSuffix(decoded, cmd) {
		t.Fatalf("解码脚本未完整保留原命令: %q", decoded)
	}
}

func TestFindWindowsPowerShell(t *testing.T) {
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
		want := `D:\PowerShell\pwsh.exe`
		got, err := findWindowsPowerShell(
			makeEnv(map[string]string{powershellEnvPath: want}),
			makeLook(nil),
			makeFileSet(want),
		)
		if err != nil || got != want {
			t.Fatalf("got = (%q, %v)，预期 %q", got, err, want)
		}
	})

	t.Run("环境变量路径不存在直接报错", func(t *testing.T) {
		_, err := findWindowsPowerShell(
			makeEnv(map[string]string{powershellEnvPath: `D:\Missing\pwsh.exe`}),
			makeLook(nil),
			makeFileSet(),
		)
		if err == nil || !strings.Contains(err.Error(), powershellEnvPath) {
			t.Fatalf("预期环境变量路径错误，实际 = %v", err)
		}
	})

	t.Run("同时存在时优先 pwsh.exe 而非 Windows PowerShell 5.1", func(t *testing.T) {
		want := `C:\Program Files\PowerShell\7\pwsh.exe`
		got, err := findWindowsPowerShell(
			makeEnv(nil),
			makeLook(map[string]string{
				"pwsh.exe":       want,
				"powershell.exe": `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
			}),
			makeFileSet(want, `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`),
		)
		if err != nil || got != want {
			t.Fatalf("got = (%q, %v)，预期 %q", got, err, want)
		}
	})

	t.Run("无 pwsh 时回退 PATH 中的 powershell.exe", func(t *testing.T) {
		want := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
		got, err := findWindowsPowerShell(
			makeEnv(nil),
			makeLook(map[string]string{"powershell.exe": want}),
			makeFileSet(want),
		)
		if err != nil || got != want {
			t.Fatalf("got = (%q, %v)，预期 %q", got, err, want)
		}
	})

	t.Run("PATH 缺失时回退 SystemRoot 默认路径", func(t *testing.T) {
		want := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
		got, err := findWindowsPowerShell(
			makeEnv(map[string]string{"SystemRoot": `C:\Windows`}),
			makeLook(nil),
			makeFileSet(want),
		)
		if err != nil || got != want {
			t.Fatalf("got = (%q, %v)，预期 %q", got, err, want)
		}
	})

	t.Run("均不存在时返回清晰错误", func(t *testing.T) {
		_, err := findWindowsPowerShell(makeEnv(nil), makeLook(nil), makeFileSet())
		if err == nil || !strings.Contains(err.Error(), "PowerShell") {
			t.Fatalf("预期 PowerShell 缺失错误，实际 = %v", err)
		}
	})
}

func TestPowerShellDisabledOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("仅验证非 Windows 平台禁用 powershell")
	}

	reg := DefaultRegistry()
	for _, def := range reg.Definitions() {
		if def.Function.Name == "powershell" {
			t.Fatalf("非 Windows 平台不应注册 powershell 工具: %+v", reg.Definitions())
		}
	}

	call := apis.ToolCall{
		ID:       "ps-1",
		Type:     "function",
		Function: apis.ToolCallFunction{Name: "powershell", Args: `{"command":"Get-Date"}`},
	}
	got, err := reg.Run(t.Context(), t.TempDir(), call, func(context.Context, string, string) (bool, error) {
		t.Fatal("非 Windows 禁用 powershell 时不应触发确认")
		return false, nil
	})
	if err != nil || got != "尚未实现" {
		t.Fatalf("非 Windows 调用 powershell 结果 = (%q, %v)，预期 (\"尚未实现\", nil)", got, err)
	}
}
