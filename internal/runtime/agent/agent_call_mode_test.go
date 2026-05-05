package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssembleSystemPromptChildAgentCallAddsDelegationBoundaries(t *testing.T) {
	result := assembleSystemPrompt(PromptContext{
		Agent: AgentSurface{
			ID:   "coder",
			Name: "Coder",
		},
		Tools: ToolSurface{
			Names: []string{"read_file", "save_output", "callagent"},
		},
		Collaboration: CollaborationSurface{
			RunMode:       "agent_call",
			ParentAgentID: "architect",
			TaskGoal:      "Implement the agreed patch",
		},
	})
	assert.Contains(t, result, "Stay narrowly scoped to the delegated task")
	assert.Contains(t, result, "your first action should be reading that exact file")
	assert.Contains(t, result, "return one concise grounded result to the parent")
}
