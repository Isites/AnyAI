package builtin_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/Isites/anyai/internal/runtime/task"
	runtimetaskbuiltin "github.com/Isites/anyai/internal/runtime/task/builtin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessExecutorExecutesBashTask(t *testing.T) {
	executor := runtimetaskbuiltin.NewProcessExecutor()
	workDir := t.TempDir()

	result, err := executor.Execute(context.Background(), task.Record{
		ID:          "task_bash",
		ProcessName: "bash",
		Input:       `{"command":"printf hello","timeout":1}`,
		Contract: task.Contract{
			Workspace: workDir,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, task.StatusCompleted, result.Status)
	assert.Equal(t, "hello", result.Summary)
	assert.Equal(t, "bash", result.Metadata["process_name"])
	assert.Equal(t, workDir, result.Metadata["workdir"])
}

func TestProcessExecutorExecutesPythonTask(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, fallbackErr := exec.LookPath("python"); fallbackErr != nil {
			t.Skip("python interpreter not available")
		}
	}

	executor := runtimetaskbuiltin.NewProcessExecutor()
	result, err := executor.Execute(context.Background(), task.Record{
		ID:          "task_python",
		ProcessName: "python",
		Input:       `{"script":"print('hello from python')","timeout":1}`,
	})
	require.NoError(t, err)
	assert.Equal(t, task.StatusCompleted, result.Status)
	assert.Equal(t, "hello from python", strings.TrimSpace(result.Summary))
	assert.Equal(t, "python", result.Metadata["process_name"])
}
