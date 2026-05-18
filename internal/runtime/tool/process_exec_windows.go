//go:build windows

package tools

import (
	"os/exec"
	"time"
)

const managedCommandWaitDelay = 2 * time.Second

func configureManagedCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.WaitDelay = managedCommandWaitDelay
}

func cleanupManagedCommand(_ *exec.Cmd) {}

func managedProcessGroupAlive(_ *exec.Cmd) bool { return false }
