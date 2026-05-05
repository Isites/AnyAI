//go:build !windows

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunBashProcessTimeoutKillsBackgroundProcessGroup(t *testing.T) {
	workDir := t.TempDir()
	startedAt := time.Now()

	result, err := RunBashProcess(context.Background(), workDir, nil, BashProcessInput{
		Command: "sleep 30 & bg=$!; echo $bg > bg.pid; sleep 30",
		Timeout: 1,
	})
	require.NoError(t, err)
	assert.Less(t, time.Since(startedAt), 6*time.Second)
	assert.Equal(t, "command timed out", result.Error)

	assertProcessExited(t, filepath.Join(workDir, "bg.pid"))
}

func TestRunBashProcessWaitDelayReapsBackgroundProcessGroup(t *testing.T) {
	workDir := t.TempDir()
	startedAt := time.Now()

	result, err := RunBashProcess(context.Background(), workDir, nil, BashProcessInput{
		Command: "sleep 30 & bg=$!; echo $bg > bg.pid; echo started",
		Timeout: 10,
	})
	require.NoError(t, err)
	assert.Less(t, time.Since(startedAt), 6*time.Second)
	assert.Contains(t, result.Output, "started")
	assert.Equal(t, "command exited but background subprocesses kept stdio open", result.Error)

	assertProcessExited(t, filepath.Join(workDir, "bg.pid"))
}

func assertProcessExited(t *testing.T, pidFile string) {
	t.Helper()

	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		err := syscall.Kill(pid, 0)
		return errors.Is(err, syscall.ESRCH)
	}, 2*time.Second, 50*time.Millisecond)
}
