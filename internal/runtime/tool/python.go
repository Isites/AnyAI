package tools

import (
	"context"
	"encoding/json"
	"time"
)

// PythonTool executes a short python script or python file from the agent
// workspace and returns stdout/stderr.
type PythonTool struct {
	WorkDir    string
	ExecPolicy *ExecPolicy
}

func (t *PythonTool) Name() string { return "python" }

func (t *PythonTool) Description() string {
	return "Execute a python script or python file from the agent workspace and return stdout/stderr. Use it when the task is too structured or repetitive for a shell one-liner, when you need lightweight parsing/transforms, or when a short script is clearer than complex bash. Supports inline script text or a file path, optional argv arguments, and a configurable timeout."
}

func (t *PythonTool) ToolMetadata() ToolMetadata {
	return externalIOToolMetadata(t.Name(), longProcessTimeoutMS, true)
}

func (t *PythonTool) TimeoutHintForInput(input json.RawMessage, fallback time.Duration) (time.Duration, bool) {
	in, err := ParsePythonProcessInput(input)
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

func (t *PythonTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"script": {
				"type": "string",
				"description": "Inline python source to execute"
			},
			"file": {
				"type": "string",
				"description": "Relative or absolute path to a python file to execute"
			},
			"args": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional argv arguments for the python script or file"
			},
			"binary": {
				"type": "string",
				"description": "Optional interpreter override such as python3 or python"
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds (default: 120)"
			}
		}
	}`)
}

func (t *PythonTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	in, err := ParsePythonProcessInput(input)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	return RunPythonProcess(ctx, t.WorkDir, t.ExecPolicy, in)
}
