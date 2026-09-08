// SPDX-License-Identifier: Apache-2.0

// Package escalation is the exclusive home of the pure, versioned escalation
// decision reducer.
package escalation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/mcuadros/director-engine/domain/execution"
)

const SchemaVersion = "director.reducer.escalation/v1"

// Facts is one typed, reconciled human-attention cause.
type Facts struct {
	SchemaVersion string             `json:"schemaVersion"`
	Scope         execution.Scope    `json:"scope"`
	CauseCode     execution.NeedCode `json:"causeCode"`
	Reconciled    bool               `json:"reconciled"`
	WakeCondition string             `json:"wakeCondition"`
}

// DecisionKind is the escalation result vocabulary.
type DecisionKind string

const (
	DecisionNeedsYou DecisionKind = "needs_you"
	DecisionRefuse   DecisionKind = "refuse"
)

// Decision is the sole reducer result which materializes a Needs-you record.
type Decision struct {
	SchemaVersion string             `json:"schemaVersion"`
	DecisionID    string             `json:"decisionId"`
	Kind          DecisionKind       `json:"kind"`
	NeedsYou      execution.NeedsYou `json:"needsYou"`
}

func decisionID(facts Facts, kind DecisionKind) string {
	encoded, err := json.Marshal(struct {
		Facts Facts        `json:"facts"`
		Kind  DecisionKind `json:"kind"`
	}{facts, kind})
	if err != nil {
		panic("marshal fixed escalation facts: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validScope(scope execution.Scope) bool {
	return scope.ProjectID != "" && scope.WorkspaceID != "" && scope.TaskID != "" && scope.RunID != ""
}

// Reduce emits Needs you only for a bounded reconciled cause and exact scope.
func Reduce(facts Facts) Decision {
	kind := DecisionRefuse
	needsYou := execution.NeedsYou{}
	if facts.SchemaVersion == SchemaVersion && validScope(facts.Scope) &&
		facts.CauseCode != "" && facts.Reconciled &&
		strings.TrimSpace(facts.WakeCondition) != "" && len(facts.WakeCondition) <= 256 {
		kind = DecisionNeedsYou
		needsYou = execution.NeedsYou{
			Code: facts.CauseCode, WakeCondition: facts.WakeCondition,
			CleanupAuthorized: false,
		}
	}
	return Decision{
		SchemaVersion: SchemaVersion, DecisionID: decisionID(facts, kind),
		Kind: kind, NeedsYou: needsYou,
	}
}
