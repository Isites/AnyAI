package gateway

import (
	"fmt"

	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
)

func (s *Service) runtimeOrErr() (runtimeport.GatewayRuntime, error) {
	if s == nil || s.runtime == nil {
		return nil, fmt.Errorf("runtime not available")
	}
	return s.runtime, nil
}
