//go:build windows

package tools

import (
	"os"
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW 阻止无控制台的 GUI 进程为子进程创建新的控制台黑框
const createNoWindow = 0x08000000

func configureBashCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

func resolveBashPath() (string, error) {
	return findWindowsBash(os.Getenv, exec.LookPath, isRegularFile)
}
