package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	runtimemcp "github.com/Isites/anyai/internal/runtime/mcp"
)

type MCPManager interface {
	ListTools(ctx context.Context) ([]runtimemcp.ToolDescriptor, error)
	CallTool(ctx context.Context, serverName, remoteToolName string, input json.RawMessage) (runtimemcp.CallResult, error)
	Close() error
}

type MCPTool struct {
	manager    MCPManager
	descriptor runtimemcp.ToolDescriptor
}

func RegisterMCPTools(reg *Registry, manager MCPManager, descriptors []runtimemcp.ToolDescriptor) {
	if reg == nil || manager == nil {
		return
	}
	for _, descriptor := range descriptors {
		if strings.TrimSpace(descriptor.Name) == "" ||
			strings.TrimSpace(descriptor.ServerName) == "" ||
			strings.TrimSpace(descriptor.RemoteName) == "" {
			continue
		}
		reg.Register(&MCPTool{
			manager:    manager,
			descriptor: cloneMCPToolDescriptor(descriptor),
		})
	}
}

func (t *MCPTool) Name() string {
	return strings.TrimSpace(t.descriptor.Name)
}

func (t *MCPTool) Description() string {
	return strings.TrimSpace(t.descriptor.Description)
}

func (t *MCPTool) Parameters() json.RawMessage {
	if len(t.descriptor.InputSchema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return append(json.RawMessage(nil), t.descriptor.InputSchema...)
}

func (t *MCPTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.manager == nil {
		return ToolResult{}, fmt.Errorf("mcp manager is not configured")
	}
	result, err := t.manager.CallTool(ctx, t.descriptor.ServerName, t.descriptor.RemoteName, input)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{
		Output:   result.Output,
		Error:    result.Error,
		Metadata: result.Metadata,
		Images:   result.Images,
	}, nil
}

func (t *MCPTool) ToolMetadata() ToolMetadata {
	if t.descriptor.ReadOnly {
		return buildToolMetadata(t.Name(), ToolEffectReadOnly, defaultToolTimeoutMS, true, false, "mcp", "external_io")
	}
	return buildToolMetadata(t.Name(), ToolEffectMutating, defaultToolTimeoutMS, false, false, "mcp", "external_io")
}

func cloneMCPToolDescriptor(descriptor runtimemcp.ToolDescriptor) runtimemcp.ToolDescriptor {
	descriptor.InputSchema = append(json.RawMessage(nil), descriptor.InputSchema...)
	return descriptor
}
