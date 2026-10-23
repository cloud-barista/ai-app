package rest

import (
	"context"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Archive record kinds produced by direct API calls, as opposed to agent runs.
const (
	kindDeployment = "deployment"
	kindControl    = "control"
)

// archiveDirect records a deployment or lifecycle call made without the agent.
//
// The first-year deliverable is "deploy/control information archiving", not
// "agent run archiving": an operator who bypasses the agent and calls the API
// directly must leave the same trail, or the archive stops being a full record
// of what happened to a deployment.
func (h *Handler) archiveDirect(ctx context.Context, nsID, kind, tool string, started time.Time, input, output any, dryRun bool, callErr error) {
	record := model.ArchiveRecord{
		RunID:     newDirectRunID(),
		StartedAt: started,
		EndedAt:   time.Now(),
		Namespace: nsID,
		Kind:      kind,
		Steps: []model.ToolStep{{
			Index:    1,
			Tool:     tool,
			Grade:    "write",
			Input:    input,
			Executed: !dryRun,
			Output:   output,
			CalledAt: started,
			Duration: time.Since(started).Round(time.Millisecond).String(),
		}},
	}
	if callErr != nil {
		record.Error = callErr.Error()
		record.Steps[0].Error = callErr.Error()
		record.Steps[0].Executed = false
	}

	if err := h.archive.Append(ctx, record); err != nil {
		// Losing a record does not invalidate the call that already happened, but
		// it does cost the feedback loop an input, so it is logged loudly.
		log.Error().Err(err).Str("nsId", nsID).Str("kind", kind).Msg("Failed to archive call")
	}
}

func newDirectRunID() string {
	return "call-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
}
