// SPDX-License-Identifier: Apache-2.0

package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/mcuadros/director-engine/domain/execution"
)

// HelperRegistration is not a root-workspace Worker entry. It is the exact
// parent-bound label set used only to observe and contain one Task-Agent-
// created helper through the fixed helperAgent.observe capability.
type HelperRegistration struct {
	RootWorkspaceID      string
	ExecutionWorkspaceID string
	Scope                execution.Scope
	ParentAgentID        string
	BaseSHA              string
	EffectID             string
	ProfileSHA256        string
	SessionSHA256        string
	RegisteredAt         string
	StartedAt            string
}

func HelperLabels(registration HelperRegistration) (map[string]string, error) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		return nil, err
	}
	if !workerIdentityPattern.MatchString(registration.RootWorkspaceID) ||
		!workerIdentityPattern.MatchString(registration.ExecutionWorkspaceID) ||
		!workerIdentityPattern.MatchString(registration.Scope.ProjectID) ||
		!workerIdentityPattern.MatchString(registration.Scope.WorkspaceID) ||
		!workerIdentityPattern.MatchString(registration.Scope.TaskID) ||
		!workerIdentityPattern.MatchString(registration.Scope.RunID) ||
		!workerIdentityPattern.MatchString(registration.ParentAgentID) ||
		!workerSHAPattern.MatchString(registration.BaseSHA) ||
		!workerIdentityPattern.MatchString(registration.EffectID) ||
		!workerDigestPattern.MatchString(registration.ProfileSHA256) ||
		!workerDigestPattern.MatchString(registration.SessionSHA256) {
		return nil, errors.New("helper correlation identity is invalid")
	}
	for _, value := range []string{registration.RegisteredAt, registration.StartedAt} {
		parsed, parseErr := time.Parse(time.RFC3339Nano, value)
		if parseErr != nil || parsed.Location() != time.UTC {
			return nil, errors.New("helper timestamp is invalid")
		}
	}
	keys := definition.WorkerRegistry.Labels
	return map[string]string{
		keys.Project:            registration.Scope.ProjectID,
		keys.RootWorkspace:      registration.RootWorkspaceID,
		keys.Workspace:          registration.Scope.WorkspaceID,
		keys.ExecutionWorkspace: registration.ExecutionWorkspaceID,
		keys.Task:               registration.Scope.TaskID,
		keys.Run:                registration.Scope.RunID,
		keys.Role:               "helper",
		keys.Phase:              WorkerPhaseBuilding,
		keys.Base:               registration.BaseSHA,
		keys.Effect:             registration.EffectID,
		keys.Profile:            registration.ProfileSHA256,
		keys.Session:            registration.SessionSHA256,
		keys.RegisteredAt:       registration.RegisteredAt,
		keys.StartedAt:          registration.StartedAt,
	}, nil
}

func HelperRegistrationDigest(registration HelperRegistration) (string, error) {
	labels, err := HelperLabels(registration)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(labels)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateHelperLabels(actual map[string]string, expected HelperRegistration) error {
	wanted, err := HelperLabels(expected)
	if err != nil {
		return err
	}
	if !maps.Equal(actual, wanted) {
		return errors.New("helper labels are absent or changed")
	}
	return nil
}

// AdmitHelperObserve is shared by the engine and connector. It refuses a
// caller-selected workspace or parent and cannot create a helper.
func AdmitHelperObserve(command Command, registration HelperRegistration, nativeAgentID string) error {
	if command.Capability != CapabilityHelperAgentObserve ||
		(command.Arguments.EffectKind != execution.EffectHelperAgentObserve && command.Arguments.EffectKind != execution.EffectHelperAgentArchive) ||
		command.Arguments.Scope != registration.Scope || command.Arguments.WorkspaceID != registration.ExecutionWorkspaceID ||
		command.Arguments.ParentAgentID == nil || *command.Arguments.ParentAgentID != registration.ParentAgentID ||
		command.Arguments.InitialPrompt != execution.HelperBootstrapPrompt || command.Arguments.ClientMessageID == "" ||
		command.Arguments.NotifyOnFinish || command.Arguments.Profile != nil ||
		command.Arguments.Session != nil || command.Arguments.AgentID != nativeAgentID {
		return errors.New("helper observation is not fixed to its admitted parent and Run")
	}
	return ValidateHelperLabels(command.Arguments.Labels, registration)
}

func AdmitHelperArchive(command Command, registration HelperRegistration, nativeAgentID string) error {
	if command.Capability != CapabilityAgentArchive || command.Arguments.EffectKind != execution.EffectHelperAgentArchive ||
		nativeAgentID == "" || command.Arguments.AgentID != nativeAgentID {
		return errors.New("helper archive is not fixed to its observed identity")
	}
	if command.Arguments.ParentAgentID == nil || *command.Arguments.ParentAgentID != registration.ParentAgentID ||
		command.Arguments.Scope != registration.Scope || command.Arguments.WorkspaceID != registration.ExecutionWorkspaceID {
		return errors.New("helper archive is bound to another parent or Run")
	}
	return ValidateHelperLabels(command.Arguments.Labels, registration)
}
