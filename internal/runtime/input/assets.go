package input

import (
	"path/filepath"
	"strings"

	"github.com/Isites/anyai/internal/config"
)

// ProjectAssetsDir returns the project-local assets directory shared by
// runtime execution and external upload endpoints.
func ProjectAssetsDir(cfg *config.Config) string {
	if cfg != nil {
		if root := strings.TrimSpace(cfg.ProjectRoot); root != "" {
			return filepath.Join(root, "anyai", "assets")
		}
		if root := strings.TrimSpace(cfg.ProjectConfigDir); root != "" {
			return filepath.Join(root, "anyai", "assets")
		}
		if path := strings.TrimSpace(cfg.Path()); path != "" {
			return filepath.Join(filepath.Dir(path), "anyai", "assets")
		}
	}
	return filepath.Join(".", "anyai", "assets")
}
