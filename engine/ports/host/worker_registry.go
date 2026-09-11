// SPDX-License-Identifier: Apache-2.0

package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/execution"
)

// WorkerRole is a Director-launched parentless role. Helpers remain
// Task-Agent-created and are deliberately outside this registry.
type WorkerRole string

const (
	WorkerRoleTaskAgent WorkerRole = "task-agent"
	WorkerRoleReviewer  WorkerRole = "reviewer"
)

const (
	WorkerPhaseBuilding  = "building"
	WorkerPhaseReviewing = "reviewing"
)

var (
	workerIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,199}$`)
	workerPhasePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	workerSHAPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	workerDigestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// WorkerRegistration is frozen before a public top-level agent create. Paseo
// assigns the native agent ID; the engine persists that returned ID before any
// later effect while these labels make the record discoverable from its root.
type WorkerRegistration struct {
	RootWorkspaceID      string
	ExecutionWorkspaceID string
	Scope                execution.Scope
	Role                 WorkerRole
	Phase                string
	CandidateSHA         string
	BaseSHA              string
	EffectID             string
	ProfileSHA256        string
	SessionSHA256        string
	RegisteredAt         string
	StartedAt            string
}

// WorkerLabels creates the exact label set admitted by the host contract.
func WorkerLabels(registration WorkerRegistration) (map[string]string, error) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		return nil, err
	}
	if !workerIdentityPattern.MatchString(registration.RootWorkspaceID) ||
		!workerIdentityPattern.MatchString(registration.ExecutionWorkspaceID) ||
		!workerIdentityPattern.MatchString(registration.Scope.ProjectID) ||
		!workerIdentityPattern.MatchString(registration.Scope.WorkspaceID) ||
		!workerIdentityPattern.MatchString(registration.Scope.TaskID) ||
		!workerIdentityPattern.MatchString(registration.Scope.RunID) {
		return nil, errors.New("worker launch registry identity is invalid")
	}
	if registration.Role != WorkerRoleTaskAgent && registration.Role != WorkerRoleReviewer {
		return nil, errors.New("worker launch registry role is invalid")
	}
	if !workerPhasePattern.MatchString(registration.Phase) {
		return nil, errors.New("worker launch registry phase is invalid")
	}
	if !workerSHAPattern.MatchString(registration.BaseSHA) ||
		(registration.CandidateSHA != "" && !workerSHAPattern.MatchString(registration.CandidateSHA)) {
		return nil, errors.New("worker launch registry commit identity is invalid")
	}
	if !workerIdentityPattern.MatchString(registration.EffectID) ||
		!workerDigestPattern.MatchString(registration.ProfileSHA256) ||
		!workerDigestPattern.MatchString(registration.SessionSHA256) {
		return nil, errors.New("worker launch correlation is invalid")
	}
	for _, value := range []string{registration.RegisteredAt, registration.StartedAt} {
		parsed, parseErr := time.Parse(time.RFC3339Nano, value)
		if parseErr != nil || parsed.Location() != time.UTC {
			return nil, errors.New("worker launch registry timestamp is invalid")
		}
	}
	keys := definition.WorkerRegistry.Labels
	labels := map[string]string{
		keys.Project:            registration.Scope.ProjectID,
		keys.RootWorkspace:      registration.RootWorkspaceID,
		keys.Workspace:          registration.Scope.WorkspaceID,
		keys.ExecutionWorkspace: registration.ExecutionWorkspaceID,
		keys.Task:               registration.Scope.TaskID,
		keys.Run:                registration.Scope.RunID,
		keys.Role:               string(registration.Role),
		keys.Phase:              registration.Phase,
		keys.Base:               registration.BaseSHA,
		keys.Effect:             registration.EffectID,
		keys.Profile:            registration.ProfileSHA256,
		keys.Session:            registration.SessionSHA256,
		keys.RegisteredAt:       registration.RegisteredAt,
		keys.StartedAt:          registration.StartedAt,
	}
	if registration.CandidateSHA != "" {
		labels[keys.Candidate] = registration.CandidateSHA
	}
	return labels, nil
}

// ValidateWorkerLabels rejects any missing, extra, parented, or changed launch
// label before the connector may create a Task Agent or Reviewer.
func ValidateWorkerLabels(actual map[string]string, expected WorkerRegistration) error {
	wanted, err := WorkerLabels(expected)
	if err != nil {
		return err
	}
	if actual["paseo.parent-agent-id"] != "" {
		return errors.New("Director-launched worker is parented")
	}
	if !maps.Equal(actual, wanted) {
		return errors.New("worker launch registry labels are absent or changed")
	}
	return nil
}

// RegistrationFromVisibility rebuilds the exact launch registration from the
// durable engine record. The role is re-admitted here rather than trusted as a
// stored string, so a persisted value outside the published vocabulary cannot
// reach the label set.
func RegistrationFromVisibility(scope execution.Scope, visibility execution.WorkerVisibility) (WorkerRegistration, error) {
	if !execution.ValidWorkerVisibility(visibility) {
		return WorkerRegistration{}, errors.New("worker launch registration is incomplete")
	}
	role := WorkerRole(visibility.Role)
	if role != WorkerRoleTaskAgent && role != WorkerRoleReviewer {
		return WorkerRegistration{}, errors.New("worker launch registry role is invalid")
	}
	return WorkerRegistration{
		RootWorkspaceID:      visibility.RootWorkspaceID,
		ExecutionWorkspaceID: visibility.ExecutionWorkspaceID,
		Scope:                scope,
		Role:                 role,
		Phase:                visibility.Phase,
		CandidateSHA:         visibility.CandidateSHA,
		BaseSHA:              visibility.BaseSHA,
		EffectID:             visibility.EffectID,
		ProfileSHA256:        visibility.ProfileSHA256,
		SessionSHA256:        visibility.SessionSHA256,
		RegisteredAt:         visibility.RegisteredAt,
		StartedAt:            visibility.StartedAt,
	}, nil
}

// RegistrationDigest binds one launch registration to the exact label set it
// publishes. The engine freezes this digest before any agent-creation effect
// and the launch reducer refuses to create an agent without it, which is what
// makes an invisible worker impossible rather than merely undesirable.
func RegistrationDigest(registration WorkerRegistration) (string, error) {
	labels, err := WorkerLabels(registration)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(labels)
	if err != nil {
		return "", fmt.Errorf("marshal worker launch labels: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// AdmitAgentCreate is the last engine-owned gate before a host connector may
// create a parentless Task Agent or Reviewer. It proves the command carries
// exactly the frozen registry labels and claims no parent, so a worker that
// would not appear in the root-workspace aggregate never starts.
func AdmitAgentCreate(command Command, registration WorkerRegistration) error {
	expectedKind := execution.EffectAgentCreate
	if registration.Role == WorkerRoleReviewer {
		expectedKind = execution.EffectReviewerAgentCreate
	}
	if command.Arguments.EffectKind != expectedKind {
		return errors.New("worker launch admission requires an agent-create command")
	}
	if command.Arguments.ParentAgentID != nil {
		return errors.New("Director-launched worker is parented")
	}
	if command.Arguments.InitialPrompt != ZeroWorkBootstrapPrompt || command.Arguments.NotifyOnFinish {
		return errors.New("worker creation requires the exact zero-work bootstrap")
	}
	if registration.Role == WorkerRoleTaskAgent {
		if err := admitPrimaryContext(command.Arguments, registration, false); err != nil {
			return err
		}
	} else if err := admitReviewerContext(command.Arguments, registration, false); err != nil {
		return err
	}
	if command.Arguments.Scope != registration.Scope {
		return errors.New("worker launch registration is bound to another Run")
	}
	if command.Arguments.EffectID != registration.EffectID {
		return errors.New("worker launch registration is bound to another effect")
	}
	expectedCapability := CapabilityTaskAgentCreate
	if registration.Role == WorkerRoleReviewer {
		expectedCapability = CapabilityReviewerAgentCreate
	}
	if command.Capability != expectedCapability {
		return errors.New("worker creation capability does not match its registered role")
	}
	return ValidateWorkerLabels(command.Arguments.Labels, registration)
}

// ParseWorkerLabels recovers the registration a label set claims. It is the
// check a host connector can perform on its own, without the engine's frozen
// record: the set must carry exactly the published keys, and re-publishing the
// recovered registration must reproduce it byte for byte. An incomplete,
// renamed, or extended set therefore cannot pass.
func ParseWorkerLabels(labels map[string]string) (WorkerRegistration, error) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		return WorkerRegistration{}, err
	}
	keys := definition.WorkerRegistry.Labels
	registration := WorkerRegistration{
		RootWorkspaceID:      labels[keys.RootWorkspace],
		ExecutionWorkspaceID: labels[keys.ExecutionWorkspace],
		Scope: execution.Scope{
			ProjectID: labels[keys.Project], WorkspaceID: labels[keys.Workspace],
			TaskID: labels[keys.Task], RunID: labels[keys.Run],
		},
		Role:          WorkerRole(labels[keys.Role]),
		Phase:         labels[keys.Phase],
		CandidateSHA:  labels[keys.Candidate],
		BaseSHA:       labels[keys.Base],
		EffectID:      labels[keys.Effect],
		ProfileSHA256: labels[keys.Profile],
		SessionSHA256: labels[keys.Session],
		RegisteredAt:  labels[keys.RegisteredAt],
		StartedAt:     labels[keys.StartedAt],
	}
	if err := ValidateWorkerLabels(labels, registration); err != nil {
		return WorkerRegistration{}, err
	}
	return registration, nil
}

// AdmitAgentCreateLabels is the host-side gate over one agent-create command.
// It proves the command is parentless, bound to the Run it claims, and carries
// a self-consistent published registration, so a connector refuses an
// invisible worker without holding engine state.
func AdmitAgentCreateLabels(command Command) (WorkerRegistration, error) {
	if command.Arguments.ParentAgentID != nil {
		return WorkerRegistration{}, errors.New("Director-launched worker is parented")
	}
	if command.Arguments.InitialPrompt != ZeroWorkBootstrapPrompt || command.Arguments.NotifyOnFinish {
		return WorkerRegistration{}, errors.New("worker creation requires the exact zero-work bootstrap")
	}
	registration, err := ParseWorkerLabels(command.Arguments.Labels)
	if err != nil {
		return WorkerRegistration{}, err
	}
	if registration.Scope != command.Arguments.Scope {
		return WorkerRegistration{}, errors.New("worker launch registration is bound to another Run")
	}
	if registration.EffectID != command.Arguments.EffectID {
		return WorkerRegistration{}, errors.New("worker launch registration is bound to another effect")
	}
	expectedKind := execution.EffectAgentCreate
	if registration.Role == WorkerRoleReviewer {
		expectedKind = execution.EffectReviewerAgentCreate
	}
	if command.Arguments.EffectKind != expectedKind {
		return WorkerRegistration{}, errors.New("worker launch admission requires an agent-create command")
	}
	expectedCapability := CapabilityTaskAgentCreate
	if registration.Role == WorkerRoleReviewer {
		expectedCapability = CapabilityReviewerAgentCreate
	}
	if command.Capability != expectedCapability {
		return WorkerRegistration{}, errors.New("worker creation capability does not match its registered role")
	}
	if registration.Role == WorkerRoleTaskAgent {
		if err := admitPrimaryContext(command.Arguments, registration, false); err != nil {
			return WorkerRegistration{}, err
		}
	} else if err := admitReviewerContext(command.Arguments, registration, false); err != nil {
		return WorkerRegistration{}, err
	}
	return registration, nil
}

func admitPrimaryContext(arguments Arguments, registration WorkerRegistration, bound bool) error {
	contractHash, contractErr := agentbridge.SchemaSHA256()
	if arguments.ClientMessageID == "" || arguments.BoundaryID == "" || arguments.OperationalObservationID == "" ||
		arguments.Profile == nil || arguments.Session == nil ||
		arguments.Profile.SHA256 != registration.ProfileSHA256 ||
		arguments.Session.SessionSHA256 != registration.SessionSHA256 ||
		arguments.Session.ContractVersion != agentbridge.ContractVersion ||
		contractErr != nil || arguments.Session.ContractHash != contractHash || arguments.Session.Role != "worker" ||
		arguments.Session.Provider != arguments.Profile.Provider ||
		arguments.Session.Model != arguments.Profile.Model || len(arguments.Session.Tools) == 0 ||
		arguments.Session.Server.Name == "" || arguments.Session.Server.Command == "" ||
		arguments.Profile.Provider == "" || arguments.Profile.Model == "" ||
		arguments.Profile.Effort == "" || arguments.Profile.Mode == "" ||
		arguments.Profile.PermissionMode == "" {
		return errors.New("worker profile or scoped MCP context is incomplete")
	}
	if !slices.IsSorted(arguments.Session.Tools) {
		return errors.New("worker scoped MCP catalog is not canonical")
	}
	if bound {
		if !workerDigestPattern.MatchString(arguments.SessionBindingSHA256) {
			return errors.New("worker scoped MCP context is not bound to the native agent")
		}
	} else if arguments.SessionBindingSHA256 != "" {
		return errors.New("bootstrap create carries a premature native session binding")
	}
	return nil
}

func admitReviewerContext(arguments Arguments, registration WorkerRegistration, bound bool) error {
	contractHash, contractErr := agentbridge.SchemaSHA256()
	expectedTools := []string{"director_candidate_read", "director_review_verdict_submit"}
	if arguments.ClientMessageID == "" || arguments.Profile == nil || arguments.Session == nil ||
		arguments.Profile.SHA256 != registration.ProfileSHA256 || arguments.Profile.PermissionMode != "read-only" ||
		arguments.Session.SessionSHA256 != registration.SessionSHA256 ||
		arguments.Session.ContractVersion != agentbridge.ContractVersion || contractErr != nil ||
		arguments.Session.ContractHash != contractHash || arguments.Session.Role != "reviewer" ||
		arguments.Session.Provider != arguments.Profile.Provider || arguments.Session.Model != arguments.Profile.Model ||
		!slices.Equal(arguments.Session.Tools, expectedTools) || arguments.Session.Server.Name == "" ||
		arguments.Session.Server.Command == "" || len(arguments.Session.Server.Env) != 0 ||
		arguments.Profile.Provider == "" || arguments.Profile.Model == "" || arguments.Profile.Effort == "" ||
		arguments.Profile.Mode == "" {
		return errors.New("reviewer profile or scoped MCP context is incomplete")
	}
	if bound {
		if !workerDigestPattern.MatchString(arguments.SessionBindingSHA256) {
			return errors.New("reviewer scoped MCP context is not bound to the native agent")
		}
	} else if arguments.SessionBindingSHA256 != "" {
		return errors.New("reviewer bootstrap carries a premature native session binding")
	} else if !arguments.PreparationReady || !workerDigestPattern.MatchString(arguments.PreparationBarrierHash) ||
		!workerDigestPattern.MatchString(arguments.LifecycleDigest) || !workerDigestPattern.MatchString(arguments.IsolationDigest) ||
		!workerIdentityPattern.MatchString(arguments.BoundaryID) ||
		!workerIdentityPattern.MatchString(arguments.OperationalObservationID) ||
		arguments.WorktreePath == "" || arguments.WorkspaceID != registration.ExecutionWorkspaceID {
		return errors.New("reviewer bootstrap lacks the frozen execution boundary")
	}
	return nil
}

// AdmitAgentPrompt is the last engine-owned gate before real Task or Review
// work starts. The persisted native agent identity is mandatory and the real
// prompt can cross the host boundary only with terminal notification enabled.
func AdmitAgentPrompt(command Command, registration WorkerRegistration, agentID string) error {
	if command.Capability != CapabilityAgentPrompt || command.Arguments.EffectKind != execution.EffectAgentPrompt {
		return errors.New("worker prompt admission requires an agent-prompt command")
	}
	if command.Arguments.Scope != registration.Scope || command.Arguments.WorkspaceID != registration.ExecutionWorkspaceID ||
		command.Arguments.AgentID == "" || command.Arguments.AgentID != agentID ||
		command.Arguments.ParentAgentID != nil || strings.TrimSpace(command.Arguments.InitialPrompt) == "" ||
		command.Arguments.InitialPrompt == ZeroWorkBootstrapPrompt || !command.Arguments.NotifyOnFinish ||
		len(command.Arguments.Labels) != 0 {
		return errors.New("worker prompt is not bound to the persisted worker identity and notified real turn")
	}
	if registration.Role == WorkerRoleTaskAgent {
		if err := admitPrimaryContext(command.Arguments, registration, true); err != nil {
			return err
		}
	} else if err := admitReviewerContext(command.Arguments, registration, true); err != nil {
		return err
	}
	return nil
}
