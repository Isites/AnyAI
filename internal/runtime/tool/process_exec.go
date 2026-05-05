package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
)

type BashProcessInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"` // seconds, optional
}

type PythonProcessInput struct {
	Script  string   `json:"script,omitempty"`
	File    string   `json:"file,omitempty"`
	Args    []string `json:"args,omitempty"`
	Binary  string   `json:"binary,omitempty"`
	Timeout int      `json:"timeout,omitempty"` // seconds, optional
}

func ParseBashProcessInput(input json.RawMessage) (BashProcessInput, error) {
	var in BashProcessInput
	if err := json.Unmarshal(input, &in); err != nil {
		return BashProcessInput{}, fmt.Errorf("invalid input: %v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return BashProcessInput{}, fmt.Errorf("command is required")
	}
	return in, nil
}

func RunBashProcess(ctx context.Context, workDir string, execPolicy *ExecPolicy, in BashProcessInput) (ToolResult, error) {
	if execPolicy != nil {
		switch execPolicy.Level {
		case "deny":
			return ToolResult{Error: "bash execution is disabled by policy"}, nil
		case "allowlist":
			for _, meta := range []string{"$(", "`", "<(", ">(", "${", "\\n"} {
				if strings.Contains(in.Command, meta) {
					return ToolResult{Error: "command contains shell metacharacters not allowed in allowlist mode"}, nil
				}
			}
			cmds := extractCommands(in.Command)
			allowed := make(map[string]bool, len(execPolicy.Allowlist))
			for _, a := range execPolicy.Allowlist {
				allowed[a] = true
			}
			for _, cmd := range cmds {
				if !allowed[cmd] {
					return ToolResult{Error: fmt.Sprintf("command %q is not in the exec allowlist", cmd)}, nil
				}
			}
		}
	}

	timeout := defaultBashTimeout
	if in.Timeout > 0 {
		timeout = time.Duration(in.Timeout) * time.Second
	}

	runCtx, cancel := withOptionalTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd", "/c", in.Command)
	} else {
		cmd = exec.CommandContext(runCtx, "bash", "-c", in.Command)
	}
	configureManagedCommand(cmd)
	if strings.TrimSpace(workDir) != "" {
		cmd.Dir = workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	stopKeepAlive := startKeepAlive(runCtx, map[string]any{
		"process_name": "bash",
		"workdir":      workDir,
	})
	defer stopKeepAlive()

	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		cleanupManagedCommand(cmd)
	}
	output := stdout.String()
	errOutput := stderr.String()

	if err != nil {
		message := err.Error()
		if errors.Is(err, exec.ErrWaitDelay) {
			message = backgroundSubprocessMessage("bash")
		}
		if runCtx.Err() == context.DeadlineExceeded {
			message = "command timed out"
		}
		if errOutput != "" {
			message = errOutput
		}
		return ToolResult{
			Output: output,
			Error:  message,
			Metadata: map[string]any{
				"process_name": "bash",
				"workdir":      workDir,
			},
		}, nil
	}

	if errOutput != "" {
		output += "\nSTDERR:\n" + errOutput
	}

	return ToolResult{
		Output: output,
		Metadata: map[string]any{
			"process_name": "bash",
			"workdir":      workDir,
		},
	}, nil
}

func ParsePythonProcessInput(input json.RawMessage) (PythonProcessInput, error) {
	var in PythonProcessInput
	if err := json.Unmarshal(input, &in); err != nil {
		return PythonProcessInput{}, fmt.Errorf("invalid input: %v", err)
	}
	in.Script = strings.TrimSpace(in.Script)
	in.File = strings.TrimSpace(in.File)
	in.Binary = strings.TrimSpace(in.Binary)
	if in.Script == "" && in.File == "" {
		return PythonProcessInput{}, fmt.Errorf("script or file is required")
	}
	return in, nil
}

func RunPythonProcess(ctx context.Context, workDir string, execPolicy *ExecPolicy, in PythonProcessInput) (ToolResult, error) {
	timeout := defaultBashTimeout
	if in.Timeout > 0 {
		timeout = time.Duration(in.Timeout) * time.Second
	}

	runCtx, cancel := withOptionalTimeout(ctx, timeout)
	defer cancel()

	binary, err := resolvePythonBinary(in.Binary, execPolicy)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}

	args := append([]string(nil), in.Args...)
	if in.File != "" {
		target := in.File
		if workDir != "" && !filepath.IsAbs(target) {
			target = filepath.Join(workDir, target)
		}
		args = append([]string{target}, args...)
	} else {
		args = append([]string{"-c", in.Script}, args...)
	}

	cmd := exec.CommandContext(runCtx, binary, args...)
	configureManagedCommand(cmd)
	if strings.TrimSpace(workDir) != "" {
		cmd.Dir = workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	stopKeepAlive := startKeepAlive(runCtx, map[string]any{
		"process_name": "python",
		"python_bin":   binary,
		"workdir":      workDir,
	})
	defer stopKeepAlive()

	err = cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		cleanupManagedCommand(cmd)
	}
	output := stdout.String()
	errOutput := stderr.String()
	if err != nil {
		message := err.Error()
		if errors.Is(err, exec.ErrWaitDelay) {
			message = backgroundSubprocessMessage("python")
		}
		if runCtx.Err() == context.DeadlineExceeded {
			message = "python process timed out"
		}
		if errOutput != "" {
			message = errOutput
		}
		return ToolResult{
			Output: output,
			Error:  message,
			Metadata: map[string]any{
				"process_name": "python",
				"python_bin":   binary,
				"workdir":      workDir,
			},
		}, nil
	}

	if errOutput != "" {
		output += "\nSTDERR:\n" + errOutput
	}

	return ToolResult{
		Output: output,
		Metadata: map[string]any{
			"process_name": "python",
			"python_bin":   binary,
			"workdir":      workDir,
		},
	}, nil
}

func resolvePythonBinary(requested string, execPolicy *ExecPolicy) (string, error) {
	if execPolicy != nil && execPolicy.Level == "deny" {
		return "", fmt.Errorf("python execution is disabled by policy")
	}
	candidates := []string{}
	if requested != "" {
		candidates = append(candidates, requested)
	}
	candidates = append(candidates, "python3", "python")
	allowed := map[string]bool{}
	if execPolicy != nil && execPolicy.Level == "allowlist" {
		for _, name := range execPolicy.Allowlist {
			allowed[filepath.Base(strings.TrimSpace(name))] = true
		}
	}
	for _, candidate := range candidates {
		if len(allowed) > 0 && !allowed[filepath.Base(candidate)] {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	if len(allowed) > 0 {
		return "", fmt.Errorf("python interpreter is not in the exec allowlist")
	}
	return "", fmt.Errorf("python interpreter not available")
}

func withOptionalTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline || timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func startKeepAlive(ctx context.Context, metadata map[string]any) func() {
	if ctx == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				runtimeactivity.Emit(ctx, metadata)
			}
		}
	}()
	return func() {
		close(done)
	}
}

func backgroundSubprocessMessage(processName string) string {
	switch strings.TrimSpace(strings.ToLower(processName)) {
	case "python", "python3":
		return "python process exited but child processes kept stdio open"
	default:
		return "command exited but background subprocesses kept stdio open"
	}
}
