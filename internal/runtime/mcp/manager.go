package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/runtime/llm"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const clientName = "anyai"

var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{}}`)

// Manager owns the live MCP client sessions for one effective agent scope.
type Manager struct {
	servers []ServerConfig

	mu       sync.Mutex
	sessions map[string]*serverSession
	tools    []ToolDescriptor
}

type serverSession struct {
	config  ServerConfig
	client  *sdkmcp.Client
	session *sdkmcp.ClientSession
	tools   []ToolDescriptor
}

func NewManager(servers []ServerConfig) *Manager {
	return &Manager{
		servers:  cloneServers(servers),
		sessions: map[string]*serverSession{},
	}
}

func (m *Manager) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	if m == nil {
		return nil, nil
	}

	m.mu.Lock()
	if len(m.tools) > 0 {
		tools := cloneToolDescriptors(m.tools)
		m.mu.Unlock()
		return tools, nil
	}
	m.mu.Unlock()

	var out []ToolDescriptor
	var errs []error
	for _, server := range m.servers {
		session, err := m.ensureSession(ctx, server)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", server.Name, err))
			runtimelogging.Warn("failed to connect mcp server", "server", server.Name, "error", err)
			continue
		}
		out = append(out, session.tools...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	m.mu.Lock()
	m.tools = cloneToolDescriptors(out)
	m.mu.Unlock()

	return cloneToolDescriptors(out), joinErrors(errs)
}

func (m *Manager) CallTool(ctx context.Context, serverName, remoteToolName string, input json.RawMessage) (CallResult, error) {
	if m == nil {
		return CallResult{}, fmt.Errorf("mcp manager is not configured")
	}

	server, ok := m.serverByName(serverName)
	if !ok {
		return CallResult{}, fmt.Errorf("unknown mcp server %q", serverName)
	}
	if !serverAllowsTool(server, remoteToolName) {
		return CallResult{Error: fmt.Sprintf("mcp tool %q is not allowed by server policy", remoteToolName)}, nil
	}

	session, err := m.ensureSession(ctx, server)
	if err != nil {
		return CallResult{}, err
	}

	callCtx := ctx
	cancel := func() {}
	if timeout := server.ToolTimeout(); timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	args := json.RawMessage(input)
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage(`{}`)
	}

	result, err := session.session.CallTool(callCtx, &sdkmcp.CallToolParams{
		Name:      remoteToolName,
		Arguments: args,
	})
	if err != nil {
		return CallResult{}, err
	}
	return convertCallResult(server, remoteToolName, result), nil
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	sessions := make([]*serverSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = map[string]*serverSession{}
	m.tools = nil
	m.mu.Unlock()

	var errs []error
	for _, session := range sessions {
		if session == nil || session.session == nil {
			continue
		}
		if err := session.session.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return joinErrors(errs)
}

func (m *Manager) serverByName(name string) (ServerConfig, bool) {
	name = strings.TrimSpace(name)
	for _, server := range m.servers {
		if strings.EqualFold(server.Name, name) {
			return server, true
		}
	}
	return ServerConfig{}, false
}

func (m *Manager) ensureSession(ctx context.Context, server ServerConfig) (*serverSession, error) {
	key := strings.ToLower(strings.TrimSpace(server.Name))
	if key == "" {
		return nil, fmt.Errorf("mcp server name is required")
	}

	m.mu.Lock()
	if session := m.sessions[key]; session != nil {
		m.mu.Unlock()
		return session, nil
	}
	m.mu.Unlock()

	connectCtx := ctx
	cancel := func() {}
	if timeout := server.StartupTimeout(); timeout > 0 {
		connectCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: clientName, Version: "1.0.0"}, &sdkmcp.ClientOptions{
		KeepAlive: 30 * time.Second,
	})
	addClientRoot(client, server)

	transport, err := buildTransport(server)
	if err != nil {
		return nil, err
	}
	sdkSession, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, err
	}
	session := &serverSession{
		config:  server.Clone(),
		client:  client,
		session: sdkSession,
	}
	tools, err := listServerTools(connectCtx, server, sdkSession)
	if err != nil {
		_ = sdkSession.Close()
		return nil, err
	}
	session.tools = tools

	m.mu.Lock()
	if existing := m.sessions[key]; existing != nil {
		m.mu.Unlock()
		_ = sdkSession.Close()
		return existing, nil
	}
	m.sessions[key] = session
	m.mu.Unlock()
	return session, nil
}

func buildTransport(server ServerConfig) (sdkmcp.Transport, error) {
	switch server.TransportType() {
	case TransportStdio:
		cmd := exec.Command(server.Command, server.Args...)
		if dir := strings.TrimSpace(server.BaseDir); dir != "" {
			cmd.Dir = dir
		}
		cmd.Env = mergeEnv(os.Environ(), server.Env)
		return &sdkmcp.CommandTransport{Command: cmd}, nil
	case TransportSSE:
		return &sdkmcp.SSEClientTransport{
			Endpoint:   server.URL,
			HTTPClient: httpClientWithHeaders(server.Headers),
		}, nil
	case TransportStreamable:
		return &sdkmcp.StreamableClientTransport{
			Endpoint:   server.URL,
			HTTPClient: httpClientWithHeaders(server.Headers),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport type %q", server.Type)
	}
}

func addClientRoot(client *sdkmcp.Client, server ServerConfig) {
	if client == nil {
		return
	}
	root := strings.TrimSpace(server.Root)
	if root == "" {
		root = strings.TrimSpace(server.BaseDir)
	}
	if root == "" {
		return
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(server.BaseDir, root)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return
	}
	client.AddRoots(&sdkmcp.Root{
		URI:  "file://" + filepath.ToSlash(abs),
		Name: server.Name,
	})
}

func listServerTools(ctx context.Context, server ServerConfig, session *sdkmcp.ClientSession) ([]ToolDescriptor, error) {
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	var descriptors []ToolDescriptor
	for _, tool := range result.Tools {
		if tool == nil || !serverAllowsTool(server, tool.Name) {
			continue
		}
		name := ToolName(server.Name, tool.Name)
		if name == "" {
			continue
		}
		descriptors = append(descriptors, ToolDescriptor{
			Name:        name,
			ServerName:  server.Name,
			RemoteName:  tool.Name,
			Description: toolDescription(server, tool),
			InputSchema: rawSchema(tool.InputSchema),
			Scope:       server.Scope,
			Source:      server.Source,
			ReadOnly:    tool.Annotations != nil && tool.Annotations.ReadOnlyHint,
		})
	}
	sort.SliceStable(descriptors, func(i, j int) bool { return descriptors[i].Name < descriptors[j].Name })
	return descriptors, nil
}

func serverAllowsTool(server ServerConfig, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return false
	}
	for _, denied := range server.Tools.Deny {
		if policyPatternMatches(denied, toolName) {
			return false
		}
	}
	if len(server.Tools.Allow) == 0 {
		return true
	}
	for _, allowed := range server.Tools.Allow {
		if policyPatternMatches(allowed, toolName) {
			return true
		}
	}
	return false
}

func policyPatternMatches(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	name = strings.TrimSpace(name)
	if pattern == "" {
		return false
	}
	if pattern == "*" || pattern == name {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

func convertCallResult(server ServerConfig, remoteToolName string, result *sdkmcp.CallToolResult) CallResult {
	if result == nil {
		return CallResult{Metadata: map[string]any{
			"server": server.Name,
			"tool":   remoteToolName,
		}}
	}

	var chunks []string
	var images []llm.ImageContent
	for _, content := range result.Content {
		if text, ok := contentToText(content); ok && strings.TrimSpace(text) != "" {
			chunks = append(chunks, text)
		}
		if image, ok := contentToImage(server.Name, remoteToolName, content); ok {
			images = append(images, image)
		}
	}
	if result.StructuredContent != nil {
		if data, err := json.MarshalIndent(result.StructuredContent, "", "  "); err == nil && len(data) > 0 {
			chunks = append(chunks, string(data))
		}
	}

	output := strings.Join(chunks, "\n\n")
	metadata := map[string]any{
		"server": server.Name,
		"tool":   remoteToolName,
	}
	if len(images) > 0 {
		metadata["images"] = len(images)
	}
	if len(result.Meta) > 0 {
		metadata["meta"] = map[string]any(result.Meta)
	}
	if result.IsError {
		return CallResult{Error: output, Metadata: metadata, Images: images}
	}
	return CallResult{Output: output, Metadata: metadata, Images: images}
}

func contentToText(content sdkmcp.Content) (string, bool) {
	switch c := content.(type) {
	case *sdkmcp.TextContent:
		return c.Text, true
	case *sdkmcp.ResourceLink:
		parts := []string{"resource: " + c.URI}
		if c.Name != "" {
			parts = append(parts, "name: "+c.Name)
		}
		if c.Description != "" {
			parts = append(parts, "description: "+c.Description)
		}
		if c.MIMEType != "" {
			parts = append(parts, "mime_type: "+c.MIMEType)
		}
		return strings.Join(parts, "\n"), true
	case *sdkmcp.EmbeddedResource:
		if c.Resource == nil {
			return "", false
		}
		if c.Resource.Text != "" {
			return c.Resource.Text, true
		}
		if len(c.Resource.Blob) > 0 {
			return fmt.Sprintf("resource: %s\nmime_type: %s\nbytes: %d", c.Resource.URI, c.Resource.MIMEType, len(c.Resource.Blob)), true
		}
		return fmt.Sprintf("resource: %s", c.Resource.URI), true
	default:
		if data, err := content.MarshalJSON(); err == nil {
			return string(data), true
		}
		return "", false
	}
}

func contentToImage(serverName, toolName string, content sdkmcp.Content) (llm.ImageContent, bool) {
	c, ok := content.(*sdkmcp.ImageContent)
	if !ok || len(c.Data) == 0 {
		return llm.ImageContent{}, false
	}
	return llm.ImageContent{
		ID:       fmt.Sprintf("mcp-%s-%s-%d", safeToolPart(serverName), safeToolPart(toolName), time.Now().UnixNano()),
		Name:     fmt.Sprintf("%s-%s", serverName, toolName),
		MimeType: c.MIMEType,
		Size:     len(c.Data),
		Data:     append([]byte(nil), c.Data...),
	}, true
}

func toolDescription(server ServerConfig, tool *sdkmcp.Tool) string {
	if tool == nil {
		return ""
	}
	desc := strings.TrimSpace(tool.Description)
	if desc == "" && tool.Annotations != nil {
		desc = strings.TrimSpace(tool.Annotations.Title)
	}
	if desc == "" {
		desc = fmt.Sprintf("Tool %s from MCP server %s.", tool.Name, server.Name)
	}
	if server.Description != "" {
		return fmt.Sprintf("%s\n\nMCP server: %s. %s", desc, server.Name, server.Description)
	}
	return fmt.Sprintf("%s\n\nMCP server: %s.", desc, server.Name)
}

func rawSchema(schema any) json.RawMessage {
	if schema == nil {
		return append(json.RawMessage(nil), emptyObjectSchema...)
	}
	data, err := json.Marshal(schema)
	if err != nil || !json.Valid(data) || len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
		return append(json.RawMessage(nil), emptyObjectSchema...)
	}
	return append(json.RawMessage(nil), data...)
}

func mergeEnv(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return append([]string(nil), base...)
	}
	env := append([]string(nil), base...)
	index := make(map[string]int, len(env))
	for i, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			index[key] = i
		}
	}
	keys := make([]string, 0, len(overlay))
	for key := range overlay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := overlay[key]
		item := key + "=" + value
		if i, ok := index[key]; ok {
			env[i] = item
			continue
		}
		env = append(env, item)
	}
	return env
}

func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{
		Transport: headerRoundTripper{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	cloned := req.Clone(req.Context())
	for key, value := range t.headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		cloned.Header.Set(key, value)
	}
	return base.RoundTrip(cloned)
}

func cloneToolDescriptors(items []ToolDescriptor) []ToolDescriptor {
	if len(items) == 0 {
		return nil
	}
	out := make([]ToolDescriptor, len(items))
	copy(out, items)
	for i := range out {
		out[i].InputSchema = append(json.RawMessage(nil), items[i].InputSchema...)
	}
	return out
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}
