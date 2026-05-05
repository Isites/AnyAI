package plan

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeCanonicalizesRunningAliases(t *testing.T) {
	normalized := Normalize(Plan{
		ID:    "plan_1",
		State: PlanState("in_progress"),
		Steps: []Step{
			{ID: "step_1", State: StepState("in-progress")},
			{ID: "step_2", State: StepState("doing")},
			{ID: "step_3", State: StepState("active")},
		},
	})

	assert.Equal(t, PlanStateRunning, normalized.State)
	assert.Equal(t, StepStateRunning, normalized.Steps[0].State)
	assert.Equal(t, StepStateRunning, normalized.Steps[1].State)
	assert.Equal(t, StepStateRunning, normalized.Steps[2].State)
}
