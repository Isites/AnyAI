package examples

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/registry"
	runtimemcp "github.com/Isites/anyai/internal/runtime/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSingleAgentChromeDevToolsMCPE2E(t *testing.T) {
	if os.Getenv("ANYAI_MCP_CHROME_E2E") != "1" {
		t.Skip("set ANYAI_MCP_CHROME_E2E=1 to run Chrome DevTools MCP e2e")
	}

	projectRoot := filepath.Join("single-agent")
	project, err := registry.LoadProject(projectRoot)
	require.NoError(t, err)

	catalog, err := runtimemcp.BuildCatalog(project.Config)
	require.NoError(t, err)

	entry, err := project.SelectEntry("")
	require.NoError(t, err)

	manager := catalog.ManagerForAgent(entry.ID)
	require.NotNil(t, manager)
	t.Cleanup(func() { _ = manager.Close() })

	tools, err := manager.ListTools(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, tools)

	serverName, toolsByName := chromeMCPTools(t, tools)
	require.Contains(t, toolsByName, "new_page")
	require.Contains(t, toolsByName, "take_snapshot")
	require.Contains(t, toolsByName, "take_screenshot")

	_, err = manager.CallTool(t.Context(), serverName, toolsByName["new_page"], rawArgs(map[string]any{
		"url":     "https://www.baidu.com",
		"timeout": 30000,
	}))
	require.NoError(t, err)

	var snapshot runtimemcp.CallResult
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		result, callErr := manager.CallTool(t.Context(), serverName, toolsByName["take_snapshot"], rawArgs(map[string]any{}))
		require.NoError(collect, callErr)
		require.Contains(collect, result.Output, "百度")
		snapshot = result
	}, 30*time.Second, time.Second)

	description := describeBaiduSnapshot(snapshot.Output)
	require.NotEmpty(t, description)
	t.Logf("baidu description: %s", description)

	screenshotPath := os.Getenv("ANYAI_MCP_CHROME_SCREENSHOT")
	if screenshotPath == "" {
		screenshotPath = filepath.Join(projectRoot, "anyai", "e2e", "baidu-chrome-devtools-mcp.png")
	}
	screenshotPath, err = filepath.Abs(screenshotPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(screenshotPath), 0o755))
	result, err := manager.CallTool(t.Context(), serverName, toolsByName["take_screenshot"], rawArgs(map[string]any{
		"filePath": screenshotPath,
		"fullPage": true,
	}))
	require.NoError(t, err)
	require.Empty(t, result.Error)

	info, err := os.Stat(screenshotPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(1024))
	t.Logf("screenshot: %s (%d bytes)", screenshotPath, info.Size())
}

func chromeMCPTools(t *testing.T, tools []runtimemcp.ToolDescriptor) (string, map[string]string) {
	t.Helper()
	byRemote := map[string]string{}
	serverName := ""
	for _, tool := range tools {
		if !strings.Contains(tool.Name, "chrome") && !strings.Contains(tool.ServerName, "chrome") {
			continue
		}
		serverName = tool.ServerName
		byRemote[tool.RemoteName] = tool.RemoteName
	}
	if serverName == "" {
		t.Fatalf("chrome devtools MCP tools not found: %#v", tools)
	}
	return serverName, byRemote
}

func rawArgs(values map[string]any) json.RawMessage {
	data, _ := json.Marshal(values)
	return data
}

func describeBaiduSnapshot(snapshot string) string {
	lines := strings.Fields(snapshot)
	interesting := make([]string, 0, 16)
	for _, line := range lines {
		if strings.Contains(line, "百度") ||
			strings.Contains(line, "搜索") ||
			strings.Contains(strings.ToLower(line), "baidu") ||
			strings.Contains(line, "新闻") ||
			strings.Contains(line, "贴吧") ||
			strings.Contains(line, "地图") {
			interesting = append(interesting, line)
		}
		if len(interesting) >= 12 {
			break
		}
	}
	if len(interesting) == 0 {
		return ""
	}
	return "百度首页包含搜索入口，以及新闻、贴吧、地图等导航元素；页面核心是百度搜索。证据片段: " + strings.Join(interesting, " ")
}
