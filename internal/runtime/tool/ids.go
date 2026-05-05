package tools

import "github.com/Isites/anyai/internal/runtime/contract"

// NewOpaqueID returns a short opaque identifier with the given prefix.
func NewOpaqueID(prefix string) string {
	return contract.NewOpaqueID(prefix)
}

// NewRunID returns a per-run identifier.
func NewRunID() string {
	return contract.NewRunID()
}
