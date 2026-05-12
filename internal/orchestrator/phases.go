package orchestrator

import (
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/executor"
)

// BuildPhases returns the executor phase list for the given Plan config.
// When Plan.Enabled is true, returns the two-phase plan→execute sequence;
// otherwise returns nil so executor.Run falls back to its single-phase
// back-compat path. Used by both the orchestrator (serve) and cmd/gleaner/drain
// so the two commands generate identical phase pipelines.
func BuildPhases(plan config.Plan) []executor.Phase {
	if !plan.Enabled {
		return nil
	}
	return []executor.Phase{
		{Name: "plan", PromptTpl: plan.PromptTemplate, Required: false},
		{Name: "execute", PromptTpl: "", Required: true},
	}
}
