package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

const defaultBashTimeout = 120 * time.Second

// ExecPolicy controls which commands the bash tool is allowed to execute.
type ExecPolicy struct {
	Level     string   // "deny", "allowlist", "full"
	Allowlist []string // command basenames allowed when Level is "allowlist"
}

// BashTool executes shell commands.
type BashTool struct {
	WorkDir    string
	ExecPolicy *ExecPolicy // nil means "full" (allow everything)
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Description() string {
	return "Execute bash commands from the agent workspace and return stdout/stderr. Use it for directory listing, file existence checks, project commands, and chaining standard shell tools. When a task becomes too repetitive or structured for a one-liner, you can also write and run a short script through bash, including invoking installed interpreters such as python3 when available. The command runs in a shell with a configurable timeout (default 120 seconds)."
}

func (t *BashTool) ToolMetadata() ToolMetadata {
	return externalIOToolMetadata(t.Name(), longProcessTimeoutMS, true)
}

func (t *BashTool) TimeoutHintForInput(input json.RawMessage, fallback time.Duration) (time.Duration, bool) {
	in, err := ParseBashProcessInput(input)
	if err != nil {
		return 0, false
	}
	if in.Timeout > 0 {
		return time.Duration(in.Timeout) * time.Second, true
	}
	if fallback > 0 {
		return fallback, true
	}
	return defaultBashTimeout, true
}

func (t *BashTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {
				"type": "string",
				"description": "The bash command or short shell script to execute. Multi-line commands, pipes, and invoking installed interpreters such as python3 are allowed when policy permits."
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds (default: 120)"
			},
			"background": {
				"type": "boolean",
				"description": "Set true when intentionally starting a long-running service for later tool calls in this run."
			},
			"wait_for": {
				"type": "string",
				"description": "Optional readiness check for background commands, such as http://127.0.0.1:3000/health or tcp://127.0.0.1:3000."
			},
			"cleanup": {
				"type": "string",
				"enum": ["run_end", "none"],
				"description": "Cleanup policy for background commands. Defaults to run_end."
			}
		},
		"required": ["command"]
	}`)
}

// extractCommands extracts executable names from a bash command string.
// It splits on pipes, semicolons, &&, and || to find each sub-command,
// then takes the first token (the executable) from each.
func extractCommands(cmd string) []string {
	// Split on shell operators
	var parts []string
	remaining := cmd
	for len(remaining) > 0 {
		// Find the earliest operator
		minIdx := len(remaining)
		opLen := 0
		for _, op := range []string{"&&", "||", "|", ";"} {
			if idx := strings.Index(remaining, op); idx != -1 && idx < minIdx {
				minIdx = idx
				opLen = len(op)
			}
		}

		part := strings.TrimSpace(remaining[:minIdx])
		if part != "" {
			parts = append(parts, part)
		}

		if minIdx+opLen >= len(remaining) {
			break
		}
		remaining = remaining[minIdx+opLen:]
	}

	var cmds []string
	for _, part := range parts {
		// Strip leading env vars (e.g., "FOO=bar command")
		tokens := strings.Fields(part)
		for _, tok := range tokens {
			if strings.Contains(tok, "=") && !strings.HasPrefix(tok, "-") {
				continue // skip env var assignments
			}
			cmds = append(cmds, filepath.Base(tok))
			break
		}
	}
	return cmds
}

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	in, err := ParseBashProcessInput(input)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	return RunBashProcess(ctx, t.WorkDir, t.ExecPolicy, in)
}
