// SPDX-License-Identifier: Apache-2.0

// Package correction defines the narrow application boundary used to submit a
// complete feedback batch to the existing correction-cycle owner.
package correction

import (
	"context"

	"github.com/mcuadros/director-engine/domain"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
)

type IngestCommand struct {
	RunID                 string
	ExpectedRunVersion    uint64
	LeaseEpoch            uint64
	OriginalTaskAgentUUID string
	CriterionIDs          []string
	FrozenPlanDigest      string
	CurrentPlanDigest     string
	FrozenSkillSetDigest  string
	CurrentSkillSetDigest string
	CurrentDecisionDigest string
	CurrentDiffDigest     string
	Policy                domaincorrection.Policy
	SourceSnapshots       []domaincorrection.SourceSnapshot
	Findings              []domaincorrection.FindingInput
	NowMillis             int64
}

type Result struct {
	Run        domain.Run
	Progressed bool
	Replayed   bool
	Code       string
}

type Router interface {
	IngestFeedback(context.Context, IngestCommand) (Result, error)
}
