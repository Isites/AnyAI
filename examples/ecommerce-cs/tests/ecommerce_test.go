package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectStructure(t *testing.T) {
	projectDir := findProjectDir(t)

	required := []string{
		filepath.Join(projectDir, "anyai.yaml"),
		filepath.Join(projectDir, "agent.md"),
		filepath.Join(projectDir, "data", "orders.json"),
		filepath.Join(projectDir, "data", "inventory.json"),
		filepath.Join(projectDir, "data", "users.json"),
		filepath.Join(projectDir, "agents", "logistics-specialist", "skills", "logistics", "SKILL.md"),
	}

	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required file missing: %s", path)
		}
	}

	var agents []string
	err := filepath.WalkDir(projectDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && d.Name() == "agent.md" {
			agents = append(agents, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk project: %v", err)
	}

	if len(agents) != 7 {
		t.Fatalf("expected 7 agent.md files, got %d", len(agents))
	}
}

func TestAgentFrontmatter(t *testing.T) {
	projectDir := findProjectDir(t)

	err := filepath.WalkDir(projectDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Name() != "agent.md" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		if !strings.HasPrefix(text, "---\n") {
			t.Fatalf("%s is missing YAML frontmatter", path)
		}
		if !strings.Contains(text, "\nname:") {
			t.Fatalf("%s is missing name field", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk agent files: %v", err)
	}
}

func TestGatewayChatEndpoint(t *testing.T) {
	baseURL := os.Getenv("E2E_GATEWAY_URL")
	if baseURL == "" {
		t.Skip("set E2E_GATEWAY_URL to run HTTP smoke tests")
	}

	payload := map[string]any{
		"agentId": "main-cs",
		"text":    "你好",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/chat", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", resp.StatusCode)
	}
}

func findProjectDir(t *testing.T) string {
	t.Helper()

	if path := os.Getenv("ANYAI_ECOMMERCE_PROJECT"); path != "" {
		return path
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	current := wd
	for {
		candidate := filepath.Join(current, "examples", "ecommerce-cs")
		if _, err := os.Stat(filepath.Join(candidate, "anyai.yaml")); err == nil {
			return candidate
		}

		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("could not locate examples/ecommerce-cs")
		}
		current = parent
	}
}
