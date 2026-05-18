package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
)

type BashProcessInput struct {
	Command    string `json:"command"`
	Timeout    int    `json:"timeout"` // seconds, optional
	Background bool   `json:"background,omitempty"`
	WaitFor    string `json:"wait_for,omitempty"`
	Cleanup    string `json:"cleanup,omitempty"` // "run_end" (default) or "none"
}

type PythonProcessInput struct {
	Script     string   `json:"script,omitempty"`
	File       string   `json:"file,omitempty"`
	Args       []string `json:"args,omitempty"`
	Binary     string   `json:"binary,omitempty"`
	Timeout    int      `json:"timeout,omitempty"` // seconds, optional
	Background bool     `json:"background,omitempty"`
	WaitFor    string   `json:"wait_for,omitempty"`
	Cleanup    string   `json:"cleanup,omitempty"` // "run_end" (default) or "none"
}

// BackgroundProcessManager owns process groups detached during one agent
// run-controller invocation.
type BackgroundProcessManager struct {
	mu   sync.Mutex
	cmds []*exec.Cmd
}

func NewBackgroundProcessManager() *BackgroundProcessManager {
	return &BackgroundProcessManager{}
}

func (m *BackgroundProcessManager) register(cmd *exec.Cmd) bool {
	if m == nil || cmd == nil || cmd.Process == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cmds = append(m.cmds, cmd)
	return true
}

func (m *BackgroundProcessManager) Cleanup() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cmds := append([]*exec.Cmd(nil), m.cmds...)
	m.cmds = nil
	m.mu.Unlock()

	for _, cmd := range cmds {
		cleanupManagedCommand(cmd)
	}
}

func ParseBashProcessInput(input json.RawMessage) (BashProcessInput, error) {
	var in BashProcessInput
	if err := json.Unmarshal(input, &in); err != nil {
		return BashProcessInput{}, fmt.Errorf("invalid input: %v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return BashProcessInput{}, fmt.Errorf("command is required")
	}
	in.WaitFor = strings.TrimSpace(in.WaitFor)
	in.Cleanup = normalizeBackgroundCleanup(in.Cleanup)
	return in, nil
}

func RunBashProcess(ctx context.Context, workDir string, execPolicy *ExecPolicy, in BashProcessInput) (ToolResult, error) {
	in.WaitFor = strings.TrimSpace(in.WaitFor)
	in.Cleanup = normalizeBackgroundCleanup(in.Cleanup)

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

	capture, captureErr := newProcessOutputCapture()
	if captureErr != nil {
		return ToolResult{Error: captureErr.Error()}, nil
	}
	defer capture.cleanup()
	cmd.Stdout = capture.stdout
	cmd.Stderr = capture.stderr

	stopKeepAlive := startKeepAlive(runCtx, map[string]any{
		"process_name": "bash",
		"workdir":      workDir,
	})
	defer stopKeepAlive()

	err := cmd.Run()
	mayDetach := in.Background || commandMayStartBackgroundProcess(in.Command)
	err, detached := handleManagedCommandCompletion(runCtx, cmd, err, backgroundProcessOptions{
		MayDetach:   mayDetach,
		Explicit:    in.Background,
		WaitFor:     in.WaitFor,
		Cleanup:     in.Cleanup,
		ProcessName: "bash",
	})
	output, errOutput := capture.output()
	metadata := map[string]any{
		"process_name": "bash",
		"workdir":      workDir,
	}
	if detached {
		metadata["background_process_detached"] = true
		if in.Background {
			metadata["background_process_explicit"] = true
		}
		if in.WaitFor != "" {
			metadata["background_wait_for"] = in.WaitFor
		}
		metadata["background_cleanup"] = in.Cleanup
	}

	if err != nil {
		message := err.Error()
		if errors.Is(err, exec.ErrWaitDelay) {
			message = backgroundSubprocessMessage("bash")
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && !in.Background {
			message = "command timed out"
		}
		if errOutput != "" {
			message = errOutput
		}
		return ToolResult{
			Output:   output,
			Error:    message,
			Metadata: metadata,
		}, nil
	}

	if errOutput != "" {
		output += "\nSTDERR:\n" + errOutput
	}
	if detached {
		output = appendBackgroundProcessNotice(output)
	}

	return ToolResult{
		Output:   output,
		Metadata: metadata,
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
	in.WaitFor = strings.TrimSpace(in.WaitFor)
	in.Cleanup = normalizeBackgroundCleanup(in.Cleanup)
	if in.Script == "" && in.File == "" {
		return PythonProcessInput{}, fmt.Errorf("script or file is required")
	}
	return in, nil
}

func RunPythonProcess(ctx context.Context, workDir string, execPolicy *ExecPolicy, in PythonProcessInput) (ToolResult, error) {
	in.Script = strings.TrimSpace(in.Script)
	in.File = strings.TrimSpace(in.File)
	in.Binary = strings.TrimSpace(in.Binary)
	in.WaitFor = strings.TrimSpace(in.WaitFor)
	in.Cleanup = normalizeBackgroundCleanup(in.Cleanup)

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

	capture, captureErr := newProcessOutputCapture()
	if captureErr != nil {
		return ToolResult{Error: captureErr.Error()}, nil
	}
	defer capture.cleanup()
	cmd.Stdout = capture.stdout
	cmd.Stderr = capture.stderr

	stopKeepAlive := startKeepAlive(runCtx, map[string]any{
		"process_name": "python",
		"python_bin":   binary,
		"workdir":      workDir,
	})
	defer stopKeepAlive()

	err = cmd.Run()
	mayDetach := in.Background || pythonMayStartBackgroundProcess(in)
	err, detached := handleManagedCommandCompletion(runCtx, cmd, err, backgroundProcessOptions{
		MayDetach:   mayDetach,
		Explicit:    in.Background,
		WaitFor:     in.WaitFor,
		Cleanup:     in.Cleanup,
		ProcessName: "python",
	})
	output, errOutput := capture.output()
	metadata := map[string]any{
		"process_name": "python",
		"python_bin":   binary,
		"workdir":      workDir,
	}
	if detached {
		metadata["background_process_detached"] = true
		if in.Background {
			metadata["background_process_explicit"] = true
		}
		if in.WaitFor != "" {
			metadata["background_wait_for"] = in.WaitFor
		}
		metadata["background_cleanup"] = in.Cleanup
	}
	if err != nil {
		message := err.Error()
		if errors.Is(err, exec.ErrWaitDelay) {
			message = backgroundSubprocessMessage("python")
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && !in.Background {
			message = "python process timed out"
		}
		if errOutput != "" {
			message = errOutput
		}
		return ToolResult{
			Output:   output,
			Error:    message,
			Metadata: metadata,
		}, nil
	}

	if errOutput != "" {
		output += "\nSTDERR:\n" + errOutput
	}
	if detached {
		output = appendBackgroundProcessNotice(output)
	}

	return ToolResult{
		Output:   output,
		Metadata: metadata,
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

type processOutputCapture struct {
	stdout *os.File
	stderr *os.File
}

func newProcessOutputCapture() (*processOutputCapture, error) {
	stdout, err := os.CreateTemp("", "anyai-process-stdout-*")
	if err != nil {
		return nil, fmt.Errorf("create stdout capture: %w", err)
	}
	stderr, err := os.CreateTemp("", "anyai-process-stderr-*")
	if err != nil {
		_ = stdout.Close()
		_ = os.Remove(stdout.Name())
		return nil, fmt.Errorf("create stderr capture: %w", err)
	}
	return &processOutputCapture{stdout: stdout, stderr: stderr}, nil
}

func (c *processOutputCapture) output() (string, string) {
	if c == nil {
		return "", ""
	}
	return readCaptureFile(c.stdout), readCaptureFile(c.stderr)
}

func (c *processOutputCapture) cleanup() {
	if c == nil {
		return
	}
	if c.stdout != nil {
		name := c.stdout.Name()
		_ = c.stdout.Close()
		_ = os.Remove(name)
	}
	if c.stderr != nil {
		name := c.stderr.Name()
		_ = c.stderr.Close()
		_ = os.Remove(name)
	}
}

func readCaptureFile(file *os.File) string {
	if file == nil {
		return ""
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	return string(data)
}

type backgroundProcessOptions struct {
	MayDetach   bool
	Explicit    bool
	WaitFor     string
	Cleanup     string
	ProcessName string
}

func handleManagedCommandCompletion(ctx context.Context, cmd *exec.Cmd, err error, opts backgroundProcessOptions) (error, bool) {
	if !errors.Is(err, exec.ErrWaitDelay) && !(err == nil && opts.MayDetach) {
		return err, false
	}
	if err == nil && opts.MayDetach && !managedProcessGroupAlive(cmd) {
		return nil, false
	}
	if ctx.Err() != nil {
		cleanupManagedCommand(cmd)
		return err, false
	}
	if opts.WaitFor != "" {
		if waitErr := waitForBackgroundReady(ctx, opts.WaitFor); waitErr != nil {
			cleanupManagedCommand(cmd)
			return fmt.Errorf("%s background readiness check failed: %w", firstNonEmptyProcessName(opts.ProcessName), waitErr), false
		}
	}
	if opts.Cleanup == "none" {
		return nil, true
	}
	manager := BackgroundProcessManagerFromContext(ctx)
	if !manager.register(cmd) {
		cleanupManagedCommand(cmd)
		if err == nil && opts.MayDetach {
			err = exec.ErrWaitDelay
		}
		return err, false
	}
	return nil, true
}

func normalizeBackgroundCleanup(cleanup string) string {
	switch strings.ToLower(strings.TrimSpace(cleanup)) {
	case "", "run_end":
		return "run_end"
	case "none":
		return "none"
	default:
		return "run_end"
	}
}

func firstNonEmptyProcessName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "process"
	}
	return name
}

func waitForBackgroundReady(ctx context.Context, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	var lastErr error
	for {
		if err := checkBackgroundReady(ctx, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = context.DeadlineExceeded
			}
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func checkBackgroundReady(ctx context.Context, target string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme == "" {
		return probeTCP(ctx, target)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		client := http.Client{Timeout: 500 * time.Millisecond}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 500 {
			return nil
		}
		return fmt.Errorf("http readiness returned %d", resp.StatusCode)
	case "tcp":
		return probeTCP(ctx, parsed.Host)
	default:
		return fmt.Errorf("unsupported readiness scheme %q", parsed.Scheme)
	}
}

func probeTCP(ctx context.Context, address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return fmt.Errorf("empty tcp readiness address")
	}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

func appendBackgroundProcessNotice(output string) string {
	notice := "[Background subprocess detached; it will remain available to later tool calls in this run and be cleaned up when the run ends.]"
	if strings.TrimSpace(output) == "" {
		return notice
	}
	output = strings.TrimRight(output, "\n")
	return output + "\n\n" + notice
}

func commandMayStartBackgroundProcess(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false
	for i, r := range command {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		switch r {
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '&':
			if inSingleQuote || inDoubleQuote {
				continue
			}
			prev := runeAt(command, i-1)
			next := runeAt(command, i+1)
			if prev == '&' || next == '&' || prev == '>' || next == '>' {
				continue
			}
			return true
		}
	}
	return false
}

func pythonMayStartBackgroundProcess(in PythonProcessInput) bool {
	if in.File != "" {
		return false
	}
	return commandMayStartBackgroundProcess(in.Script)
}

func runeAt(text string, index int) rune {
	if index < 0 || index >= len(text) {
		return 0
	}
	return rune(text[index])
}
