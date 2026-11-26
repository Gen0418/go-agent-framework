package tools

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

const (
	powershellEnvPath = "SAISON_POWERSHELL_PATH"
	powershellUTF8Pfx = "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $OutputEncoding = [System.Text.Encoding]::UTF8\n"
)

// 将 PowerShell 命令编码为 -EncodedCommand 所需的 UTF-16LE Base64
// 自动注入 UTF-8 输出设置，避免 Windows PowerShell 5.1 中文输出乱码与命令行引号转义问题
func encodePowerShellCommand(command string) string {
	script := powershellUTF8Pfx + command
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], u)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// 解析 Windows 下的 PowerShell 可执行文件路径
// 按环境变量 -> pwsh(PowerShell 7+) -> powershell(Windows PowerShell 5.1) -> 系统默认目录查找
func findWindowsPowerShell(getenv func(string) string, lookPath func(string) (string, error), isFile func(string) bool) (string, error) {
	if custom := strings.TrimSpace(getenv(powershellEnvPath)); custom != "" {
		if isFile(custom) {
			return custom, nil
		}
		return "", fmt.Errorf("环境变量 %s 指定的路径不存在: %s", powershellEnvPath, custom)
	}

	for _, name := range []string{"pwsh.exe", "pwsh", "powershell.exe", "powershell"} {
		if found, err := lookPath(name); err == nil && found != "" && isFile(found) {
			return found, nil
		}
	}

	if sysRoot := strings.TrimSpace(getenv("SystemRoot")); sysRoot != "" {
		fallback := winJoin(sysRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if isFile(fallback) {
			return fallback, nil
		}
	}

	return "", fmt.Errorf("未找到 PowerShell 可执行文件，请检查系统 PATH 或设置环境变量 %s", powershellEnvPath)
}
