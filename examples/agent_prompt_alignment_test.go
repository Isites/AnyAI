package examples

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Isites/anyai/internal/registry"
	runtimemcp "github.com/Isites/anyai/internal/runtime/mcp"
)

func TestMultiAgentEntryPromptsFavorNaturalLanguage(t *testing.T) {
	root := examplesRoot(t)
	projects := []string{
		"parallel-workflow",
		"runtime-lab",
		"ecommerce-cs",
		"harness-analytics",
		"harness-coding",
		"harness-google-review",
	}

	disallowed := []string{
		`callagent({`,
		`"mode": "parallel"`,
		`"tasks": [`,
	}
	requiredAny := []string{
		"自然语言",
		"用户只需要",
	}

	for _, project := range projects {
		path := filepath.Join(root, project, "agent.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)

		for _, needle := range disallowed {
			if strings.Contains(content, needle) {
				t.Fatalf("%s should avoid structured delegation syntax %q", path, needle)
			}
		}

		foundNaturalLanguageGuidance := false
		for _, needle := range requiredAny {
			if strings.Contains(content, needle) {
				foundNaturalLanguageGuidance = true
				break
			}
		}
		if !foundNaturalLanguageGuidance {
			t.Fatalf("%s should explain that users can drive the workflow with natural language", path)
		}
	}
}

func TestHarnessCodingLeadPromptSupportsDirectNonCodingReplies(t *testing.T) {
	root := examplesRoot(t)
	path := filepath.Join(root, "harness-coding", "agent.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)

	required := []string{
		"你是谁",
		"直接简短回答",
		"不进入多阶段流程",
		"禁止空响应",
	}
	for _, needle := range required {
		if !strings.Contains(content, needle) {
			t.Fatalf("%s should include %q", path, needle)
		}
	}
}

func TestHarnessCodingLeadPromptIncludesDelegationSkeleton(t *testing.T) {
	root := examplesRoot(t)
	path := filepath.Join(root, "harness-coding", "agent.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)

	required := []string{
		"统一委派任务骨架",
		"路径化委派协议",
		"输入文件路径",
		"本轮唯一职责",
		"目标产物文件",
		"由该专家自己写回",
		"拒收判定",
	}
	for _, needle := range required {
		if !strings.Contains(content, needle) {
			t.Fatalf("%s should include %q", path, needle)
		}
	}
}

func TestH5OnlineLeadDelegatesExecutionToSkills(t *testing.T) {
	root := examplesRoot(t)
	projectDir := filepath.Join(root, "h5-online")
	leadPath := filepath.Join(projectDir, "agent.md")

	leadData, err := os.ReadFile(leadPath)
	if err != nil {
		t.Fatalf("read %s: %v", leadPath, err)
	}
	lead := string(leadData)
	project, err := registry.LoadProject(projectDir)
	if err != nil {
		t.Fatalf("load h5-online: %v", err)
	}
	leadCfg, ok := project.Config.GetAgent("h5-online")
	if !ok {
		t.Fatalf("h5-online agent missing")
	}
	if !containsString(leadCfg.Tools.Allow, "python") {
		t.Fatalf("h5-online should allow python tool")
	}
	if containsString(leadCfg.Tools.Allow, "bash") {
		t.Fatalf("h5-online should not allow bash tool")
	}

	for _, needle := range []string{
		`skill_get("harness-google-review-http")`,
		`skill_get("harness-coding-http")`,
		"固定闭环",
		"对应 skill",
		"不亲自审核站点，也不亲自修改代码",
		"- python",
		"不使用 `bash` 包装 Python 代码",
	} {
		if !strings.Contains(lead, needle) {
			t.Fatalf("%s should include %q", leadPath, needle)
		}
	}
	for _, forbidden := range []string{
		"scripts/anyai_http_run.py",
		"--base-url",
		"--agent-id",
		"--session-id",
		"--run-id",
		"/api/runs",
		"curl",
		"urllib",
		"- bash",
		"--force-new",
		"queued",
		"running",
		"anyai start",
		"pkill",
		"lsof",
	} {
		if strings.Contains(lead, forbidden) {
			t.Fatalf("%s should leave execution detail %q to the HTTP skills", leadPath, forbidden)
		}
	}

	for _, rel := range []string{
		filepath.Join("common", "skills", "harness-google-review-http", "SKILL.md"),
		filepath.Join("common", "skills", "harness-coding-http", "SKILL.md"),
	} {
		path := filepath.Join(projectDir, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, needle := range []string{
			"scripts/anyai_http_run.py",
			"python` 工具的 `file`",
			"不能用 heredoc/inline script 包装",
			"--base-url",
			"--session-id",
			"返回给主控的摘要",
		} {
			if !strings.Contains(content, needle) {
				t.Fatalf("%s should include %q", path, needle)
			}
		}
	}
}

func TestHarnessCodingUIPromptRequiresChromeDevToolsMCP(t *testing.T) {
	root := examplesRoot(t)
	leadPath := filepath.Join(root, "harness-coding", "agent.md")
	uiPath := filepath.Join(root, "harness-coding", "agents", "ui-test-engineer", "agent.md")

	leadData, err := os.ReadFile(leadPath)
	if err != nil {
		t.Fatalf("read %s: %v", leadPath, err)
	}
	lead := string(leadData)
	for _, needle := range []string{
		"ui-test-engineer",
		"06-ui-test-report-rN.md",
		"Chrome DevTools MCP",
		"拒绝使用 AnyAI 内置 `browser`",
	} {
		if !strings.Contains(lead, needle) {
			t.Fatalf("%s should include %q", leadPath, needle)
		}
	}

	uiData, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatalf("read %s: %v", uiPath, err)
	}
	ui := string(uiData)
	for _, needle := range []string{
		"id: ui-test-engineer",
		"- browser",
		"mcp__chrome_devtools__*",
		"严禁使用 AnyAI 内置 `browser`",
		"06-ui-test-report-rN.md",
		"Chrome DevTools MCP 的 `filePath` 必须使用绝对路径",
		"严禁把截图、MCP 证据或浏览器导出文件保存到 `examples/harness-coding/agents/ui-test-engineer/`",
		"目标产物文件",
		"workflow-artifacts/screenshots/",
	} {
		if !strings.Contains(ui, needle) {
			t.Fatalf("%s should include %q", uiPath, needle)
		}
	}
}

func TestHarnessCodingTranslatesWorkflowPromptAndAgents(t *testing.T) {
	root := examplesRoot(t)
	projectDir := filepath.Join(root, "harness-coding")
	leadPath := filepath.Join(projectDir, "agent.md")

	leadData, err := os.ReadFile(leadPath)
	if err != nil {
		t.Fatalf("read %s: %v", leadPath, err)
	}
	lead := string(leadData)
	for _, needle := range []string{
		"翻译子工作流门禁",
		"`translates`",
		"翻译子工作流门禁发生在 `context-analyst` 完成初始需求与项目现状分析之后",
		"不要绕过 `context-analyst` 直接判断并启动翻译子工作流",
		"translation-scope -> translation-manifest -> chunk translation dispatch -> merge translated chunks -> write back locale data -> translation QA",
		"`coder` 不得直接批量自由翻译长文案",
		"`<translation_work_dir>/07-translation-final.md`",
		"翻译子工作流产物不默认写入 `workflow-artifacts/`",
		"翻译 QA 不通过或翻译产物缺失：回到 `translates`",
		"`<translation_work_dir>/04-translation-results.json`",
	} {
		if !strings.Contains(lead, needle) {
			t.Fatalf("%s should include %q", leadPath, needle)
		}
	}

	project, err := registry.LoadProject(projectDir)
	if err != nil {
		t.Fatalf("load harness-coding: %v", err)
	}
	requiredAgents := []string{
		"translates",
		"translation-scope",
		"translation-manifest",
		"chunk-translation-dispatch",
		"merge-translated-chunks",
		"write-back-locale-data",
		"translation-qa",
	}
	for _, agentID := range requiredAgents {
		agentCfg, ok := project.Config.GetAgent(agentID)
		if !ok {
			t.Fatalf("harness-coding missing agent %q", agentID)
		}
		if !strings.Contains(filepath.ToSlash(agentCfg.Workspace), "harness-coding/agents/translates") {
			t.Fatalf("%s workspace should live under agents/translates, got %q", agentID, agentCfg.Workspace)
		}
	}

	translates, ok := project.Config.GetAgent("translates")
	if !ok {
		t.Fatal("translates agent missing")
	}
	if !containsString(translates.Tools.Allow, "callagent") {
		t.Fatalf("translates should allow callagent")
	}
	translatesPath := filepath.Join(projectDir, "agents", "translates", "agent.md")
	translatesData, err := os.ReadFile(translatesPath)
	if err != nil {
		t.Fatalf("read %s: %v", translatesPath, err)
	}
	translatesPrompt := string(translatesData)
	for _, needle := range []string{
		"独立的翻译子工作流入口",
		"translation-workspace/<translation_task_id>/",
		"00-task-request.md",
		"机械处理 + 小块模型翻译",
		"不要依赖上下文压缩保存翻译状态",
		"02-translation-items.jsonl",
		"03-translation-chunk-plan.jsonl",
		"07-translation-final.md",
		"<work_dir>/01-translation-scope.md",
		"<work_dir>/04-translation-results.json",
		"不同 session 或不同翻译任务不得共用同一个工作目录",
	} {
		if !strings.Contains(translatesPrompt, needle) {
			t.Fatalf("%s should include %q", translatesPath, needle)
		}
	}
	for _, forbidden := range []string{
		"tech-lead",
		"Tech Lead",
		"补齐 locale / i18n / JSON / Markdown / 页面内容",
		"给 Tech Lead 的交接",
	} {
		if strings.Contains(translatesPrompt, forbidden) {
			t.Fatalf("%s should not include %q", translatesPath, forbidden)
		}
	}
	contextPath := filepath.Join(projectDir, "agents", "context-analyst", "agent.md")
	contextData, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("read %s: %v", contextPath, err)
	}
	contextPrompt := string(contextData)
	for _, needle := range []string{
		"翻译子工作流判断",
		"是否需要 `translates`",
		"源语言和目标语言候选",
		"等 `translates` 产物返回后",
	} {
		if !strings.Contains(contextPrompt, needle) {
			t.Fatalf("%s should include %q", contextPath, needle)
		}
	}
	for _, agentID := range requiredAgents[1:] {
		path := filepath.Join(projectDir, "agents", "translates", agentID, "agent.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, needle := range []string{
			"正式产物",
			"write_file",
			"<work_dir>",
			"上下文",
		} {
			if !strings.Contains(content, needle) {
				t.Fatalf("%s should include %q", path, needle)
			}
		}
	}
}

func TestHarnessCodingChromeDevToolsMCPScope(t *testing.T) {
	root := examplesRoot(t)
	project, err := registry.LoadProject(filepath.Join(root, "harness-coding"))
	if err != nil {
		t.Fatalf("load harness-coding: %v", err)
	}
	catalog, err := runtimemcp.BuildCatalog(project.Config)
	if err != nil {
		t.Fatalf("build mcp catalog: %v", err)
	}

	techLead, ok := project.Config.GetAgent("tech-lead")
	if !ok {
		t.Fatal("tech-lead agent missing")
	}
	uiAgent, ok := project.Config.GetAgent("ui-test-engineer")
	if !ok {
		t.Fatal("ui-test-engineer agent missing")
	}
	if !containsString(techLead.Tools.Deny, "browser") {
		t.Fatalf("tech-lead should deny built-in browser")
	}
	if !containsString(uiAgent.Tools.Deny, "browser") {
		t.Fatalf("ui-test-engineer should deny built-in browser")
	}
	if project.Config.SharedMCPsDir != filepath.Join(root, "harness-coding", "common", "mcps") {
		t.Fatalf("harness-coding shared MCPs should load from common/mcps, got %q", project.Config.SharedMCPsDir)
	}

	techLeadChrome, ok := mcpServerByName(catalog.ServersForAgent("tech-lead"), "chrome-devtools")
	if !ok {
		t.Fatalf("tech-lead should have chrome-devtools MCP")
	}
	if techLeadChrome.Scope != runtimemcp.ScopeShared {
		t.Fatalf("tech-lead chrome-devtools MCP should be shared scope, got %q", techLeadChrome.Scope)
	}
	uiChrome, ok := mcpServerByName(catalog.ServersForAgent("ui-test-engineer"), "chrome-devtools")
	if !ok {
		t.Fatalf("ui-test-engineer should have chrome-devtools MCP")
	}
	if uiChrome.Scope != runtimemcp.ScopeShared {
		t.Fatalf("ui-test-engineer chrome-devtools MCP should be shared scope, got %q", uiChrome.Scope)
	}
	if !strings.Contains(filepath.ToSlash(uiChrome.Source), "harness-coding/common/mcps/chrome-devtools.yaml") {
		t.Fatalf("ui-test-engineer chrome-devtools MCP should come from common/mcps, got %q", uiChrome.Source)
	}
	if !techLead.InheritSharedMCPs {
		t.Fatalf("tech-lead should inherit shared MCPs")
	}
	if !uiAgent.InheritSharedMCPs {
		t.Fatalf("ui-test-engineer should inherit shared MCPs")
	}
	for _, agent := range project.Config.Agents.List {
		if agent.ID == "tech-lead" || agent.ID == "ui-test-engineer" {
			continue
		}
		if agent.InheritSharedMCPs {
			t.Fatalf("%s should opt out of shared MCPs", agent.ID)
		}
		if hasMCPServer(catalog.ServersForAgent(agent.ID), "chrome-devtools") {
			t.Fatalf("%s should not inherit chrome-devtools MCP", agent.ID)
		}
	}
}

func TestHarnessGoogleReviewPromptsGateMissingArtifacts(t *testing.T) {
	root := examplesRoot(t)
	projectDir := filepath.Join(root, "harness-google-review")
	project, err := registry.LoadProject(projectDir)
	if err != nil {
		t.Fatalf("load harness-google-review: %v", err)
	}

	leadPath := filepath.Join(projectDir, "agent.md")
	leadData, err := os.ReadFile(leadPath)
	if err != nil {
		t.Fatalf("read %s: %v", leadPath, err)
	}
	lead := string(leadData)
	for _, needle := range []string{
		"串行产物门控",
		"任务正文必须列出本轮具体的输入文件路径和目标输出文件路径",
		"INPUT_VALIDATION_ERROR",
		"先重跑对应上游生产 agent",
	} {
		if !strings.Contains(lead, needle) {
			t.Fatalf("%s should include %q", leadPath, needle)
		}
	}
	for _, forbidden := range []string{
		"`input_artifacts`",
		"`expected_outputs`",
		"runtime contract",
		"callagent contract",
	} {
		if strings.Contains(lead, forbidden) {
			t.Fatalf("%s should not include %q", leadPath, forbidden)
		}
	}

	leadCfg, ok := project.Config.GetAgent("review-lead")
	if !ok {
		t.Fatalf("harness-google-review missing review-lead")
	}
	if !containsString(leadCfg.Tools.Allow, "callagent") {
		t.Fatalf("review-lead should allow callagent")
	}
	for _, forbiddenTool := range []string{"read_file", "write_file", "edit_file", "bash"} {
		if containsString(leadCfg.Tools.Allow, forbiddenTool) {
			t.Fatalf("review-lead should not allow %s", forbiddenTool)
		}
	}

	requiredAgents := []string{
		"intake-triager",
		"site-crawler",
		"content-analyzer",
		"duplication-auditor",
		"seo-analyzer",
		"ux-analyzer",
		"policy-analyzer",
		"ad-inventory-analyzer",
		"rejection-mapper",
		"report-generator",
		"requirement-generator",
		"qa-verifier",
	}
	for _, agentID := range requiredAgents {
		agentCfg, ok := project.Config.GetAgent(agentID)
		if !ok {
			t.Fatalf("harness-google-review missing agent %q", agentID)
		}
		if agentCfg.Workspace != projectDir {
			t.Fatalf("%s workspace should be project root %q, got %q", agentID, projectDir, agentCfg.Workspace)
		}

		path := filepath.Join(projectDir, "agents", agentID, "agent.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		requiredNeedles := []string{
			"## 输入完整性契约",
			"INPUT_VALIDATION_ERROR",
		}
		if agentID == "intake-triager" {
			requiredNeedles = append(requiredNeedles, "missing_required_inputs")
		} else {
			requiredNeedles = append(requiredNeedles, "missing_input_artifacts")
		}
		for _, needle := range requiredNeedles {
			if !strings.Contains(content, needle) {
				t.Fatalf("%s should include %q", path, needle)
			}
		}
	}
}

func TestHarnessGoogleReviewChromeDevToolsMCPScope(t *testing.T) {
	root := examplesRoot(t)
	projectDir := filepath.Join(root, "harness-google-review")
	project, err := registry.LoadProject(projectDir)
	if err != nil {
		t.Fatalf("load harness-google-review: %v", err)
	}
	catalog, err := runtimemcp.BuildCatalog(project.Config)
	if err != nil {
		t.Fatalf("build mcp catalog: %v", err)
	}

	agentsExpectedToUseChrome := map[string]bool{
		"site-crawler":          true,
		"content-analyzer":      true,
		"duplication-auditor":   true,
		"seo-analyzer":          true,
		"ux-analyzer":           true,
		"policy-analyzer":       true,
		"ad-inventory-analyzer": true,
		"qa-verifier":           true,
	}
	for _, agent := range project.Config.Agents.List {
		hasChrome := hasMCPServer(catalog.ServersForAgent(agent.ID), "chrome-devtools")
		if agentsExpectedToUseChrome[agent.ID] {
			if !agent.InheritSharedMCPs {
				t.Fatalf("%s should inherit shared MCPs", agent.ID)
			}
			if !hasChrome {
				t.Fatalf("%s should have chrome-devtools MCP", agent.ID)
			}
			continue
		}
		if agent.InheritSharedMCPs {
			t.Fatalf("%s should not inherit shared MCPs unless it performs browser evidence checks", agent.ID)
		}
		if hasChrome {
			t.Fatalf("%s should not inherit chrome-devtools MCP unless it performs browser evidence checks", agent.ID)
		}
	}
}

func TestSingleAgentPromptIncludesGenericTaskSkeleton(t *testing.T) {
	root := examplesRoot(t)
	path := filepath.Join(root, "single-agent", "agent.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)

	required := []string{
		"统一任务处理骨架",
		"当前目标",
		"产出目标",
		"不懂装懂是禁止的",
	}
	for _, needle := range required {
		if !strings.Contains(content, needle) {
			t.Fatalf("%s should include %q", path, needle)
		}
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func hasMCPServer(servers []runtimemcp.ServerConfig, name string) bool {
	_, ok := mcpServerByName(servers, name)
	return ok
}

func mcpServerByName(servers []runtimemcp.ServerConfig, name string) (runtimemcp.ServerConfig, bool) {
	for _, server := range servers {
		if server.Name == name {
			return server, true
		}
	}
	return runtimemcp.ServerConfig{}, false
}
