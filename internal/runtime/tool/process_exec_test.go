//go:build !windows

package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
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

func TestRunBashProcessWaitDelayDetachesBackgroundProcessForRun(t *testing.T) {
	workDir := t.TempDir()
	manager := NewBackgroundProcessManager()
	ctx := WithBackgroundProcessManager(context.Background(), manager)
	startedAt := time.Now()

	result, err := RunBashProcess(ctx, workDir, nil, BashProcessInput{
		Command: "sleep 30 & bg=$!; echo $bg > bg.pid; echo started",
		Timeout: 10,
	})
	require.NoError(t, err)
	assert.Less(t, time.Since(startedAt), 6*time.Second)
	assert.Contains(t, result.Output, "started")
	assert.Contains(t, result.Output, "Background subprocess detached")
	assert.Empty(t, result.Error)
	assert.Equal(t, true, result.Metadata["background_process_detached"])

	assertProcessRunning(t, filepath.Join(workDir, "bg.pid"))
	manager.Cleanup()
	assertProcessExited(t, filepath.Join(workDir, "bg.pid"))
}

func TestRunBashProcessDetachedServerRemainsReachableUntilCleanup(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workDir := t.TempDir()
	port := freeTCPPort(t)
	manager := NewBackgroundProcessManager()
	ctx := WithBackgroundProcessManager(context.Background(), manager)
	command := fmt.Sprintf(`python3 -u -m http.server %d --bind 127.0.0.1 &
bg=$!
echo $bg > server.pid
for i in $(seq 1 50); do
  python3 -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:%d', timeout=0.2).read()" >/dev/null 2>&1 && { echo ready; exit 0; }
  sleep 0.1
done
exit 1`, port, port)

	result, err := RunBashProcess(ctx, workDir, nil, BashProcessInput{Command: command, Timeout: 10})
	require.NoError(t, err)
	require.Empty(t, result.Error, result.Error)
	assert.Contains(t, result.Output, "ready")
	assert.Equal(t, true, result.Metadata["background_process_detached"])

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	manager.Cleanup()
	assertProcessExited(t, filepath.Join(workDir, "server.pid"))
}

func TestRunBashProcessExplicitBackgroundWaitForHTTP(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workDir := t.TempDir()
	port := freeTCPPort(t)
	manager := NewBackgroundProcessManager()
	ctx := WithBackgroundProcessManager(context.Background(), manager)
	command := fmt.Sprintf(`python3 -u -m http.server %d --bind 127.0.0.1 &
echo $! > server.pid`, port)

	result, err := RunBashProcess(ctx, workDir, nil, BashProcessInput{
		Command:    command,
		Timeout:    10,
		Background: true,
		WaitFor:    fmt.Sprintf("http://127.0.0.1:%d", port),
	})
	require.NoError(t, err)
	require.Empty(t, result.Error, result.Error)
	assert.Equal(t, true, result.Metadata["background_process_detached"])
	assert.Equal(t, true, result.Metadata["background_process_explicit"])
	assert.Equal(t, "run_end", result.Metadata["background_cleanup"])
	assert.Equal(t, fmt.Sprintf("http://127.0.0.1:%d", port), result.Metadata["background_wait_for"])

	manager.Cleanup()
	assertProcessExited(t, filepath.Join(workDir, "server.pid"))
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

func assertProcessRunning(t *testing.T, pidFile string) {
	t.Helper()

	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)

	err = syscall.Kill(pid, 0)
	require.NoError(t, err)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return addr.Port
}
