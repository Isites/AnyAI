package examples

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
