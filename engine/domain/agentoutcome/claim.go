// SPDX-License-Identifier: Apache-2.0

// Package agentoutcome owns the seven closed, engine-defined
// AgentOutcomeClaim schemas. Claims are bounded inputs and never evidence or
// lifecycle decisions.
package agentoutcome

import (
	"embed"
	"slices"
)

// Kind is the closed AgentOutcomeClaim outcome vocabulary.
type Kind string

const (
	Completed           Kind = "completed"
	NeedsValidation     Kind = "needs_validation"
	NeedsReview         Kind = "needs_review"
	NeedsHumanDecision  Kind = "needs_human_decision"
	BlockedByDependency Kind = "blocked_by_dependency"
	BlockedByAccess     Kind = "blocked_by_access"
	BudgetExhausted     Kind = "budget_exhausted"
)

var kinds = []Kind{
	Completed,
	NeedsValidation,
	NeedsReview,
	NeedsHumanDecision,
	BlockedByDependency,
	BlockedByAccess,
	BudgetExhausted,
}

var schemaNames = map[Kind]string{
	Completed:           "schemas/completed.schema.json",
	NeedsValidation:     "schemas/needs_validation.schema.json",
	NeedsReview:         "schemas/needs_review.schema.json",
	NeedsHumanDecision:  "schemas/needs_human_decision.schema.json",
	BlockedByDependency: "schemas/blocked_by_dependency.schema.json",
	BlockedByAccess:     "schemas/blocked_by_access.schema.json",
	BudgetExhausted:     "schemas/budget_exhausted.schema.json",
}

//go:embed schemas/*.schema.json
var schemas embed.FS

// Kinds returns a defensive copy of the closed outcome vocabulary.
func Kinds() []Kind {
	return slices.Clone(kinds)
}

// Schema returns the engine-owned JSON schema for an outcome.
func Schema(kind Kind) ([]byte, bool) {
	name, ok := schemaNames[kind]
	if !ok {
		return nil, false
	}
	schema, err := schemas.ReadFile(name)
	if err != nil {
		return nil, false
	}
	return schema, true
}
