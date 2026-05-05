package gateway

import (
	"strings"

	"github.com/Isites/anyai/internal/config"
	gatewayrouter "github.com/Isites/anyai/internal/gateway/router"
)

// ApplyRuntimeConfig updates gateway-owned views that are derived from the
// active runtime config. Runtime state remains owned by runtime.
func (s *Service) ApplyRuntimeConfig(cfg *config.Config, fallbackAgentID string) {
	if s == nil || cfg == nil {
		return
	}
	s.SetRouteResolver(gatewayrouter.NewRouter(cfg.Bindings, fallbackAgentIDForConfig(cfg, fallbackAgentID)))
}

func fallbackAgentIDForConfig(cfg *config.Config, preferred string) string {
	if preferred = strings.TrimSpace(preferred); preferred != "" {
		return preferred
	}
	if cfg == nil || len(cfg.Agents.List) == 0 {
		return "default"
	}
	return strings.TrimSpace(cfg.Agents.List[0].ID)
}
