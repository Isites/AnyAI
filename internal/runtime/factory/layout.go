package factory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Isites/anyai/internal/config"
)

type ProjectLayout struct {
	DataDir     string
	SessionsDir string
	MemoryDir   string
	EventsDir   string
}

func PrepareProjectLayout(cfg *config.Config) (ProjectLayout, error) {
	if cfg == nil {
		return ProjectLayout{}, fmt.Errorf("config is required")
	}

	layout := ProjectLayout{
		DataDir: cfg.RuntimeDataDir(),
	}
	layout.SessionsDir = filepath.Join(layout.DataDir, "sessions")
	layout.EventsDir = filepath.Join(layout.DataDir, "events")
	layout.MemoryDir = strings.TrimSpace(cfg.Memory.Dir)
	if layout.MemoryDir == "" {
		layout.MemoryDir = filepath.Join(layout.DataDir, "memory")
	}

	if err := os.MkdirAll(layout.SessionsDir, 0o755); err != nil {
		return ProjectLayout{}, fmt.Errorf("create sessions dir: %w", err)
	}
	if err := os.MkdirAll(layout.MemoryDir, 0o755); err != nil {
		return ProjectLayout{}, fmt.Errorf("create memory dir: %w", err)
	}

	return layout, nil
}
