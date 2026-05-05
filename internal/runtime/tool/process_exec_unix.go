//go:build !windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const managedCommandWaitDelay = 2 * time.Second

func configureManagedCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.WaitDelay = managedCommandWaitDelay
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		return killManagedProcessGroup(cmd)
	}
}

func cleanupManagedCommand(cmd *exec.Cmd) {
	_ = killManagedProcessGroup(cmd)
}

func killManagedProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
