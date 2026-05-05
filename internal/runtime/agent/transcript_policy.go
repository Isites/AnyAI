package agent

type transcriptPolicy struct {
	enabled                   bool
	mergeConsecutiveUserTurns bool
	repairToolPairs           bool
	dropOrphanToolResults     bool
	treatMetaAsSummaryContext bool
}

func defaultTranscriptPolicy() transcriptPolicy {
	return transcriptPolicy{
		enabled:                   true,
		mergeConsecutiveUserTurns: true,
		repairToolPairs:           true,
		dropOrphanToolResults:     true,
		treatMetaAsSummaryContext: true,
	}
}

func (r *Runtime) transcriptPolicy() transcriptPolicy {
	if r == nil {
		return defaultTranscriptPolicy()
	}
	policy := defaultTranscriptPolicy()
	cfg := r.TranscriptHygiene
	if cfg == (TranscriptHygieneConfig{}) {
		return policy
	}
	if !cfg.Enabled {
		policy.enabled = false
	}
	policy.mergeConsecutiveUserTurns = cfg.MergeConsecutiveUserTurns
	policy.repairToolPairs = cfg.RepairToolPairs
	policy.dropOrphanToolResults = cfg.DropOrphanToolResults
	policy.treatMetaAsSummaryContext = cfg.TreatMetaAsSummaryContext
	return policy
}
