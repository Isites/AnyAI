package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var (
	version = "1.0.0"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "anyai",
		Short: "AnyAI project-first agent runtime",
		Long:  "AnyAI is a project-first Go runtime for building and running agent systems defined by anyai.yaml, agent.md, and SKILL.md.",
	}

	rootCmd.AddCommand(
		chatCmd(),
		startCmd(),
		initCmd(),
		versionCmd(),
	)
	return rootCmd
}

func chatCmd() *cobra.Command {
	var projectPath string
	cmd := &cobra.Command{
		Use:   "chat [agent-id]",
		Short: "Start a CLI chat in the current AnyAI project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, agentID, err := resolveProjectTargetAndAgent(projectPath, args)
			if err != nil {
				return err
			}
			return runProject(target, agentID)
		},
	}
	cmd.Flags().StringVar(&projectPath, "project", "", "path to the AnyAI project root or agent.md")
	return cmd
}

func startCmd() *cobra.Command {
	var projectPath string
	cmd := &cobra.Command{
		Use:   "start [agent-id]",
		Short: "Start the long-running AnyAI gateway for a project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, agentID, err := resolveProjectTargetAndAgent(projectPath, args)
			if err != nil {
				return err
			}
			return runProjectStart(target, agentID)
		},
	}
	cmd.Flags().StringVar(&projectPath, "project", "", "path to the AnyAI project root or agent.md")
	return cmd
}

func initCmd() *cobra.Command {
	var templateName string
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Initialize a new AnyAI project from a template",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "."
			if len(args) > 0 {
				target = args[0]
			}
			if templateName == "" {
				templateName = "single-agent"
			}
			return runInit(templateName, target)
		},
	}
	cmd.Flags().StringVar(&templateName, "template", "single-agent", "built-in template name")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print AnyAI version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("anyai %s\n", version)
		},
	}
}

func projectPathOrCWD(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	return cwd, nil
}

// 这里逻辑是为了简单，避免一定要传--project参数麻烦
func resolveProjectTargetAndAgent(projectPath string, args []string) (string, string, error) {
	resolved, err := projectPathOrCWD(projectPath)
	if err != nil {
		return "", "", err
	}
	agentID := ""
	if len(args) > 0 {
		agentID = args[0]
	}
	return resolved, agentID, nil
}

func runInit(templateName, target string) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target dir: %w", err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	files, ok := builtinTemplates()[templateName]
	if !ok {
		return fmt.Errorf("unknown template %q; available templates: %s", templateName, strings.Join(availableTemplateNames(), ", "))
	}
	if err := writeTemplateFiles(target, files); err != nil {
		return err
	}

	fmt.Printf("Initialized AnyAI project from template %q at %s\n", templateName, target)
	return nil
}

func writeTemplateFiles(targetRoot string, files map[string]string) error {
	for rel, content := range files {
		dst := filepath.Join(targetRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("target already contains %s", dst)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func availableTemplateNames() []string {
	var names []string
	for name := range builtinTemplates() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func builtinTemplates() map[string]map[string]string {
	return map[string]map[string]string{
		"single-agent": {
			"anyai.yaml": "# AnyAI defaults already include:\n# - memory recall and auto-capture\n# - runtime agent call limits\n# - `anyai start` listening on 127.0.0.1:18789\n# Add overrides here only when you really need them.\nname: single-agent\nmodels:\n  default: anthropic/claude-sonnet-4-5\n",
			"agent.md":   "---\nid: assistant\nname: Project Assistant\nentry: true\nmodel: anthropic/claude-sonnet-4-5\nmax_turns: 12\ntools:\n  allow:\n    - read_file\n    - write_file\n    - edit_file\n    - bash\n    - callagent\n---\n\n你是项目的主 Agent。\n\n工作原则：\n- 先理解用户目标，再执行\n- 能直接完成的任务直接完成\n- 需要拆解时再委派给子 Agent\n- 输出时说明结果与风险\n",
			"README.md":  "# Single Agent Template\n\n1. 配置 Provider 环境变量。\n2. 运行 `anyai chat` 进入当前项目。\n3. 运行 `anyai start` 启动 HTTP / WebSocket Gateway。\n",
		},
		"coding": {
			"anyai.yaml":                        "# Defaults already include memory, runtime agent call limits, and the HTTP gateway.\n# Keep this file focused on project-specific model choices and overrides.\nname: coding-project\nmodels:\n  default: anthropic/claude-sonnet-4-5\n  aliases:\n    fast: openai/gpt-4.1-mini\n",
			"agent.md":                          "---\nid: lead\nname: Engineering Lead\nentry: true\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n    - write_file\n    - edit_file\n    - bash\n    - callagent\n---\n\n你是项目主控 Agent。\n\n工作原则：\n- 先分辨是实现、审查还是测试问题\n- 需要并行时委派给 coder、reviewer、tester\n- 汇总子 Agent 结果后再向用户输出\n",
			"agents/coder/agent.md":             "---\nid: coder\nname: Coder\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n    - write_file\n    - edit_file\n    - bash\n    - callagent\n---\n\n你负责实现代码和最小必要测试。\n",
			"agents/reviewer/agent.md":          "---\nid: reviewer\nname: Reviewer\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n    - bash\n---\n\n你负责风险审查、回归点检查和实现质量把关。\n",
			"agents/tester/agent.md":            "---\nid: tester\nname: Tester\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n    - bash\n---\n\n你负责测试设计、测试执行和失败定位。\n",
			"common/skills/code-style/SKILL.md": "---\nname: code-style\ndescription: 通用编码协作规范\ntags: [coding, review]\n---\n修改代码时优先小步提交、保持行为清晰、先修风险再做风格整理。\n",
		},
		"support": {
			"anyai.yaml":                "# Defaults already include memory, runtime agent call limits, and the HTTP gateway.\nname: support-project\nmodels:\n  default: anthropic/claude-sonnet-4-5\n",
			"agent.md":                  "---\nid: support\nname: Support Lead\nentry: true\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n    - callagent\n---\n\n你负责处理用户问题，并在必要时委派给 refund、logistics 等专项 Agent。\n",
			"agents/refund/agent.md":    "---\nid: refund\nname: Refund Specialist\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n---\n\n你负责退款规则解释、退款条件判断和升级建议。\n",
			"agents/logistics/agent.md": "---\nid: logistics\nname: Logistics Specialist\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n---\n\n你负责物流时效、异常场景和补偿建议。\n",
		},
		"research": {
			"anyai.yaml":                  "# Defaults already include memory, runtime agent call limits, and the HTTP gateway.\nname: research-project\nmodels:\n  default: anthropic/claude-sonnet-4-5\n",
			"agent.md":                    "---\nid: researcher\nname: Research Lead\nentry: true\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n    - web_fetch\n    - web_search\n    - callagent\n---\n\n你负责研究任务拆解、证据归纳和最终结论整合。\n",
			"agents/collector/agent.md":   "---\nid: collector\nname: Evidence Collector\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - web_fetch\n    - web_search\n---\n\n你负责搜集资料、摘取关键事实并注明来源。\n",
			"agents/synthesizer/agent.md": "---\nid: synthesizer\nname: Synthesizer\nmodel: anthropic/claude-sonnet-4-5\ntools:\n  allow:\n    - read_file\n---\n\n你负责聚合证据、比较观点并形成结构化结论。\n",
		},
	}
}
