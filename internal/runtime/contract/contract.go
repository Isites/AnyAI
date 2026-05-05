// Package contract provides canonical type definitions for the runtime.
// It sits at the bottom of the runtime dependency graph and should not
// import any other runtime packages.
package contract

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// NewOpaqueID returns a short opaque identifier with the given prefix.
func NewOpaqueID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

// NewRunID returns a per-run identifier.
func NewRunID() string {
	return NewOpaqueID("run")
}

// Contract is the structured execution contract for tasks and agent calls.
type Contract struct {
	TaskGoal        string   `json:"task_goal,omitempty"`
	Workspace       string   `json:"workspace,omitempty"`
	ExpectedOutputs []string `json:"expected_outputs,omitempty"`
	InputArtifacts  []string `json:"input_artifacts,omitempty"`
	ReturnMode      string   `json:"return_mode,omitempty"`
}

// Normalized returns a normalized copy of the contract with trimmed strings
// and deduplicated arrays.
func (c Contract) Normalized() Contract {
	c.TaskGoal = strings.TrimSpace(c.TaskGoal)
	c.Workspace = strings.TrimSpace(c.Workspace)
	c.ReturnMode = strings.TrimSpace(c.ReturnMode)
	c.ExpectedOutputs = compactStrings(c.ExpectedOutputs)
	c.InputArtifacts = compactStrings(c.InputArtifacts)
	return c
}

// Empty returns true if the contract has no meaningful content.
func (c Contract) Empty() bool {
	c = c.Normalized()
	return c.TaskGoal == "" &&
		c.Workspace == "" &&
		c.ReturnMode == "" &&
		len(c.ExpectedOutputs) == 0 &&
		len(c.InputArtifacts) == 0
}

// MergeMissing fills in any empty fields from another contract.
func (c Contract) MergeMissing(other Contract) Contract {
	c = c.Normalized()
	other = other.Normalized()
	if c.TaskGoal == "" {
		c.TaskGoal = other.TaskGoal
	}
	if c.Workspace == "" {
		c.Workspace = other.Workspace
	}
	if c.ReturnMode == "" {
		c.ReturnMode = other.ReturnMode
	}
	if len(c.ExpectedOutputs) == 0 {
		c.ExpectedOutputs = append([]string(nil), other.ExpectedOutputs...)
	}
	if len(c.InputArtifacts) == 0 {
		c.InputArtifacts = append([]string(nil), other.InputArtifacts...)
	}
	return c
}

// ToMap converts the contract to a map for JSON serialization.
func (c Contract) ToMap() map[string]any {
	c = c.Normalized()
	if c.Empty() {
		return nil
	}
	payload := map[string]any{}
	if c.TaskGoal != "" {
		payload["task_goal"] = c.TaskGoal
	}
	if c.Workspace != "" {
		payload["workspace"] = c.Workspace
	}
	if len(c.ExpectedOutputs) > 0 {
		payload["expected_outputs"] = append([]string(nil), c.ExpectedOutputs...)
	}
	if len(c.InputArtifacts) > 0 {
		payload["input_artifacts"] = append([]string(nil), c.InputArtifacts...)
	}
	if c.ReturnMode != "" {
		payload["return_mode"] = c.ReturnMode
	}
	return payload
}

func compactStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
