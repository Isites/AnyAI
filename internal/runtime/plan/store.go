package plan

// Store is the runtime storage contract for structured plan snapshots.
type Store interface {
	UpdateStructuredPlan(plan Plan) error
	GetStructuredPlan() (Plan, bool)
}
