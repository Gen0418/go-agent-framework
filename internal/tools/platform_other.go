//go:build !windows

package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func defaultTools() []Tool {
	return []Tool{Bash{}}
}

func configureHiddenCmd(*exec.Cmd) {}

func resolveBashPath() (string, error) {
	if custom := strings.TrimSpace(os.Getenv(bashEnvPath)); custom != "" {
		if isRegularFile(custom) {
			return custom, nil
		}
		return "", fmt.Errorf("环境变量 %s 指定的路径不存在: %s", bashEnvPath, custom)
	}
	return "bash", nil
}
