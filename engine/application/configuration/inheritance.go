// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domainreview "github.com/mcuadros/director-engine/domain/review"
)

var (
	ErrWorkspaceMissing        = errors.New("effective configuration requires a declared Workspace")
	ErrTaskOverrideInvalid     = errors.New("Task configuration override is invalid")
	ErrOutsideSecurityEnvelope = errors.New("effective configuration exceeds the human-approved security envelope")
)

// Scope is the closed precedence order reported with every effective value.
type Scope string

const (
	ScopeProject   Scope = "project"
	ScopeWorkspace Scope = "workspace"
	ScopeTask      Scope = "task"
)

// TaskOverride is the mutable Task-owned configuration layer. Nil scalar
// pointers mean Inherit; they preserve explicit zero and false values.
type TaskOverride struct {
	LaunchPolicy                  domainconfig.LaunchPolicy
	DeliveryMode                  domainconfig.DeliveryMode
	MaxActiveTasks                *int
	MaxActiveTasksPerWorkspace    *int
	MaxConcurrentAgents           *int
	MaxSubagentsPerTask           *int
	ElapsedSeconds                *int64
	Tokens                        *int64
	Turns                         *int64
	CICycles                      *int64
	CostMicrousd                  *int64
	AutoFixCIFailures             *bool
	AutoFixReviewFeedback         *bool
	RequireDifferentReviewerModel *bool
}

// EffectiveSources reports exactly which layer supplied each value.
type EffectiveSources struct {
	LaunchPolicy                  Scope `json:"launchPolicy"`
	DeliveryMode                  Scope `json:"deliveryMode"`
	MaxActiveTasks                Scope `json:"maxActiveTasks"`
	MaxActiveTasksPerWorkspace    Scope `json:"maxActiveTasksPerWorkspace"`
	MaxConcurrentAgents           Scope `json:"maxConcurrentAgents"`
	MaxSubagentsPerTask           Scope `json:"maxSubagentsPerTask"`
	ElapsedSeconds                Scope `json:"elapsedSeconds"`
	Tokens                        Scope `json:"tokens"`
	Turns                         Scope `json:"turns"`
	CICycles                      Scope `json:"ciCycles"`
	CostMicrousd                  Scope `json:"costMicrousd"`
	AutoFixCIFailures             Scope `json:"autoFixCiFailures"`
	AutoFixReviewFeedback         Scope `json:"autoFixReviewFeedback"`
	RequireDifferentReviewerModel Scope `json:"requireDifferentReviewerModel"`
}

// EffectiveConfiguration is the complete frozen Project -> Workspace -> Task
// result consumed by a future Run. It contains no fallback or host decision.
type EffectiveConfiguration struct {
	LaunchPolicy                  domainconfig.LaunchPolicy `json:"launchPolicy"`
	DeliveryMode                  domainconfig.DeliveryMode `json:"deliveryMode"`
	Limits                        domainconfig.Limits       `json:"limits"`
	RunBudget                     domainconfig.RunBudget    `json:"runBudget"`
	AutoFixCIFailures             bool                      `json:"autoFixCiFailures"`
	AutoFixReviewFeedback         bool                      `json:"autoFixReviewFeedback"`
	RequireDifferentReviewerModel bool                      `json:"requireDifferentReviewerModel"`
	Sources                       EffectiveSources          `json:"sources"`
}

// ReviewPolicy returns the frozen engine policy consumed by a Run. Connectors
// and provider adapters never infer or relax this configured choice.
func (configuration EffectiveConfiguration) ReviewPolicy() domainreview.ProfilePolicy {
	return domainreview.ProfilePolicy{RequireDifferentReviewerModel: configuration.RequireDifferentReviewerModel}
}

func projectEffective(configuration domainconfig.Configuration) EffectiveConfiguration {
	return EffectiveConfiguration{
		LaunchPolicy:                  configuration.Defaults.LaunchPolicy,
		DeliveryMode:                  configuration.Defaults.DeliveryMode,
		Limits:                        configuration.Defaults.Limits,
		RunBudget:                     configuration.Defaults.RunBudget,
		AutoFixCIFailures:             configuration.Defaults.AutoFixCIFailures,
		AutoFixReviewFeedback:         configuration.Defaults.AutoFixReviewFeedback,
		RequireDifferentReviewerModel: configuration.Defaults.RequireDifferentReviewerModel,
		Sources: EffectiveSources{
			LaunchPolicy: ScopeProject, DeliveryMode: ScopeProject,
			MaxActiveTasks: ScopeProject, MaxActiveTasksPerWorkspace: ScopeProject,
			MaxConcurrentAgents: ScopeProject, MaxSubagentsPerTask: ScopeProject,
			ElapsedSeconds: ScopeProject, Tokens: ScopeProject, Turns: ScopeProject,
			CICycles: ScopeProject, CostMicrousd: ScopeProject, AutoFixCIFailures: ScopeProject,
			AutoFixReviewFeedback: ScopeProject, RequireDifferentReviewerModel: ScopeProject,
		},
	}
}

func applyInt(value *int, destination *int, source *Scope, scope Scope) {
	if value != nil {
		*destination = *value
		*source = scope
	}
}

func applyInt64(value *int64, destination *int64, source *Scope, scope Scope) {
	if value != nil {
		*destination = *value
		*source = scope
	}
}

func applyBool(value *bool, destination *bool, source *Scope, scope Scope) {
	if value != nil {
		*destination = *value
		*source = scope
	}
}

func applyWorkspace(result *EffectiveConfiguration, override domainconfig.WorkspaceOverride) {
	if override.LaunchPolicy != domainconfig.LaunchInherit {
		result.LaunchPolicy = override.LaunchPolicy
		result.Sources.LaunchPolicy = ScopeWorkspace
	}
	if override.DeliveryMode != domainconfig.DeliveryInherit {
		result.DeliveryMode = override.DeliveryMode
		result.Sources.DeliveryMode = ScopeWorkspace
	}
	applyInt(override.MaxActiveTasks, &result.Limits.MaxActiveTasks, &result.Sources.MaxActiveTasks, ScopeWorkspace)
	applyInt(override.MaxActiveTasksPerWorkspace, &result.Limits.MaxActiveTasksPerWorkspace, &result.Sources.MaxActiveTasksPerWorkspace, ScopeWorkspace)
	applyInt(override.MaxConcurrentAgents, &result.Limits.MaxConcurrentAgents, &result.Sources.MaxConcurrentAgents, ScopeWorkspace)
	applyInt(override.MaxSubagentsPerTask, &result.Limits.MaxSubagentsPerTask, &result.Sources.MaxSubagentsPerTask, ScopeWorkspace)
	applyInt64(override.ElapsedSeconds, &result.RunBudget.ElapsedSeconds, &result.Sources.ElapsedSeconds, ScopeWorkspace)
	applyInt64(override.Tokens, &result.RunBudget.Tokens, &result.Sources.Tokens, ScopeWorkspace)
	applyInt64(override.Turns, &result.RunBudget.Turns, &result.Sources.Turns, ScopeWorkspace)
	applyInt64(override.CICycles, &result.RunBudget.CICycles, &result.Sources.CICycles, ScopeWorkspace)
	applyInt64(override.CostMicrousd, &result.RunBudget.CostMicrousd, &result.Sources.CostMicrousd, ScopeWorkspace)
	applyBool(override.AutoFixCIFailures, &result.AutoFixCIFailures, &result.Sources.AutoFixCIFailures, ScopeWorkspace)
	applyBool(override.AutoFixReviewFeedback, &result.AutoFixReviewFeedback, &result.Sources.AutoFixReviewFeedback, ScopeWorkspace)
	applyBool(override.RequireDifferentReviewerModel, &result.RequireDifferentReviewerModel, &result.Sources.RequireDifferentReviewerModel, ScopeWorkspace)
}

func applyTask(result *EffectiveConfiguration, override TaskOverride) {
	if override.LaunchPolicy != "" && override.LaunchPolicy != domainconfig.LaunchInherit {
		result.LaunchPolicy = override.LaunchPolicy
		result.Sources.LaunchPolicy = ScopeTask
	}
	if override.DeliveryMode != "" && override.DeliveryMode != domainconfig.DeliveryInherit {
		result.DeliveryMode = override.DeliveryMode
		result.Sources.DeliveryMode = ScopeTask
	}
	applyInt(override.MaxActiveTasks, &result.Limits.MaxActiveTasks, &result.Sources.MaxActiveTasks, ScopeTask)
	applyInt(override.MaxActiveTasksPerWorkspace, &result.Limits.MaxActiveTasksPerWorkspace, &result.Sources.MaxActiveTasksPerWorkspace, ScopeTask)
	applyInt(override.MaxConcurrentAgents, &result.Limits.MaxConcurrentAgents, &result.Sources.MaxConcurrentAgents, ScopeTask)
	applyInt(override.MaxSubagentsPerTask, &result.Limits.MaxSubagentsPerTask, &result.Sources.MaxSubagentsPerTask, ScopeTask)
	applyInt64(override.ElapsedSeconds, &result.RunBudget.ElapsedSeconds, &result.Sources.ElapsedSeconds, ScopeTask)
	applyInt64(override.Tokens, &result.RunBudget.Tokens, &result.Sources.Tokens, ScopeTask)
	applyInt64(override.Turns, &result.RunBudget.Turns, &result.Sources.Turns, ScopeTask)
	applyInt64(override.CICycles, &result.RunBudget.CICycles, &result.Sources.CICycles, ScopeTask)
	applyInt64(override.CostMicrousd, &result.RunBudget.CostMicrousd, &result.Sources.CostMicrousd, ScopeTask)
	applyBool(override.AutoFixCIFailures, &result.AutoFixCIFailures, &result.Sources.AutoFixCIFailures, ScopeTask)
	applyBool(override.AutoFixReviewFeedback, &result.AutoFixReviewFeedback, &result.Sources.AutoFixReviewFeedback, ScopeTask)
	applyBool(override.RequireDifferentReviewerModel, &result.RequireDifferentReviewerModel, &result.Sources.RequireDifferentReviewerModel, ScopeTask)
}

func taskOverrideValid(override TaskOverride) bool {
	if override.LaunchPolicy != "" && override.LaunchPolicy != domainconfig.LaunchInherit &&
		override.LaunchPolicy != domainconfig.LaunchManual && override.LaunchPolicy != domainconfig.LaunchAutomatic {
		return false
	}
	if override.DeliveryMode != "" && override.DeliveryMode != domainconfig.DeliveryInherit &&
		override.DeliveryMode != domainconfig.DeliveryPullRequest && override.DeliveryMode != domainconfig.DeliveryDirect {
		return false
	}
	for _, value := range []*int{override.MaxActiveTasks, override.MaxActiveTasksPerWorkspace, override.MaxConcurrentAgents} {
		if value != nil && *value < 1 {
			return false
		}
	}
	if override.MaxSubagentsPerTask != nil && *override.MaxSubagentsPerTask < 0 {
		return false
	}
	for _, value := range []*int64{override.ElapsedSeconds, override.Tokens} {
		if value != nil && *value < 1 {
			return false
		}
	}
	if override.Turns != nil && (*override.Turns < 1 || *override.Turns > 256) {
		return false
	}
	if override.CICycles != nil && (*override.CICycles < 1 || *override.CICycles > 256) {
		return false
	}
	if override.CostMicrousd != nil && *override.CostMicrousd < 0 {
		return false
	}
	return true
}

func effectiveConsistent(value EffectiveConfiguration) bool {
	return value.Limits.MaxActiveTasks >= 1 && value.Limits.MaxActiveTasksPerWorkspace >= 1 &&
		value.Limits.MaxConcurrentAgents >= 1 && value.Limits.MaxSubagentsPerTask >= 0 &&
		value.Limits.MaxActiveTasksPerWorkspace <= value.Limits.MaxActiveTasks &&
		value.Limits.MaxActiveTasks <= value.Limits.MaxConcurrentAgents &&
		value.RunBudget.ElapsedSeconds >= 1 && value.RunBudget.Tokens >= 1 &&
		value.RunBudget.Turns >= 1 && value.RunBudget.Turns <= 256 &&
		value.RunBudget.CICycles >= 1 && value.RunBudget.CICycles <= 256
}

func costWithin(boundary, proposed int64) bool {
	// No configured boundary is intentionally unlimited, so adding a finite
	// limit tightens it. Removing or raising a finite limit expands authority.
	return boundary == 0 || (proposed > 0 && proposed <= boundary)
}

func resolve(configuration domainconfig.Configuration, workspaceID string, task TaskOverride) (EffectiveConfiguration, error) {
	workspaceFound := false
	for _, workspace := range configuration.Workspaces {
		if workspace.ID == workspaceID {
			workspaceFound = true
			break
		}
	}
	if !workspaceFound {
		return EffectiveConfiguration{}, ErrWorkspaceMissing
	}
	if !taskOverrideValid(task) {
		return EffectiveConfiguration{}, ErrTaskOverrideInvalid
	}
	result := projectEffective(configuration)
	for _, override := range configuration.WorkspaceOverrides {
		if override.WorkspaceID == workspaceID {
			applyWorkspace(&result, override)
			break
		}
	}
	applyTask(&result, task)
	if !effectiveConsistent(result) {
		return EffectiveConfiguration{}, ErrTaskOverrideInvalid
	}
	return result, nil
}

// SecurityEnvelope is an immutable upper authority bound established from an
// exact configuration only by a server-authenticated human action.
type SecurityEnvelope struct {
	boundary domainconfig.Document
	sha256   string
}

func securityEnvelopeFromDocument(document domainconfig.Document) SecurityEnvelope {
	digest := sha256.Sum256(append([]byte("director.security-envelope/v1\x00"), document.CanonicalJSON()...))
	return SecurityEnvelope{boundary: document, sha256: hex.EncodeToString(digest[:])}
}

// NewSecurityEnvelope establishes a bound from the exact approved canonical
// configuration. The confirmation binds to the configuration SHA-256.
func NewSecurityEnvelope(document domainconfig.Document, confirmation HumanConfirmation) (SecurityEnvelope, error) {
	if confirmation.ActorKind != ActorHuman || !confirmation.Confirmed ||
		!validHumanActor(confirmation.ActorID) || confirmation.Revision != document.SHA256() {
		return SecurityEnvelope{}, ErrSecurityEnvelopeInvalid
	}
	return securityEnvelopeFromDocument(document), nil
}

func (envelope SecurityEnvelope) valid() bool {
	return envelope.sha256 != "" && envelope.boundary.SHA256() != "" &&
		securityEnvelopeFromDocument(envelope.boundary).sha256 == envelope.sha256
}

// SHA256 identifies the complete immutable envelope without exposing its
// configuration bytes.
func (envelope SecurityEnvelope) SHA256() string {
	if !envelope.valid() {
		return ""
	}
	return envelope.sha256
}

func launchWithin(boundary, proposed domainconfig.LaunchPolicy) bool {
	return boundary == domainconfig.LaunchAutomatic || proposed == domainconfig.LaunchManual
}

func deliveryWithin(boundary, proposed domainconfig.DeliveryMode) bool {
	return boundary == domainconfig.DeliveryDirect || proposed == domainconfig.DeliveryPullRequest
}

func effectiveWithin(boundary, proposed EffectiveConfiguration) bool {
	return launchWithin(boundary.LaunchPolicy, proposed.LaunchPolicy) &&
		deliveryWithin(boundary.DeliveryMode, proposed.DeliveryMode) &&
		proposed.Limits.MaxActiveTasks <= boundary.Limits.MaxActiveTasks &&
		proposed.Limits.MaxActiveTasksPerWorkspace <= boundary.Limits.MaxActiveTasksPerWorkspace &&
		proposed.Limits.MaxConcurrentAgents <= boundary.Limits.MaxConcurrentAgents &&
		proposed.Limits.MaxSubagentsPerTask <= boundary.Limits.MaxSubagentsPerTask &&
		proposed.RunBudget.ElapsedSeconds <= boundary.RunBudget.ElapsedSeconds &&
		proposed.RunBudget.Tokens <= boundary.RunBudget.Tokens &&
		proposed.RunBudget.Turns <= boundary.RunBudget.Turns &&
		proposed.RunBudget.CICycles <= boundary.RunBudget.CICycles &&
		costWithin(boundary.RunBudget.CostMicrousd, proposed.RunBudget.CostMicrousd) &&
		(!proposed.AutoFixCIFailures || boundary.AutoFixCIFailures) &&
		(!proposed.AutoFixReviewFeedback || boundary.AutoFixReviewFeedback) &&
		(!boundary.RequireDifferentReviewerModel || proposed.RequireDifferentReviewerModel)
}

func envelopeIssue(path, message string) domainconfig.Issue {
	return domainconfig.Issue{Code: "security_envelope_expansion", Path: path, Message: message}
}

func (envelope SecurityEnvelope) issues(document domainconfig.Document) []domainconfig.Issue {
	if !envelope.valid() {
		return []domainconfig.Issue{envelopeIssue("$", "the human-approved security envelope is missing or invalid")}
	}
	boundary := envelope.boundary.Configuration()
	proposed := document.Configuration()
	if proposed.Project.ID != boundary.Project.ID {
		return []domainconfig.Issue{envelopeIssue("$.project.id", "Project identity is outside the approved security envelope")}
	}
	if !reflect.DeepEqual(proposed.AgentProfiles, boundary.AgentProfiles) {
		return []domainconfig.Issue{envelopeIssue("$.agentProfiles", "provider, model, effort, or permission authority exceeds the approved security envelope")}
	}
	allowed := make(map[string]domainconfig.Workspace, len(boundary.Workspaces))
	for _, workspace := range boundary.Workspaces {
		allowed[workspace.ID] = workspace
	}
	for index, workspace := range proposed.Workspaces {
		approved, ok := allowed[workspace.ID]
		if !ok || workspace.Remote != approved.Remote || workspace.SourcePath != approved.SourcePath ||
			workspace.DefaultBaseBranch != approved.DefaultBaseBranch {
			return []domainconfig.Issue{envelopeIssue(fmt.Sprintf("$.workspaces[%d]", index), "repository, path, or target branch authority exceeds the approved security envelope")}
		}
		approvedEffective, approvedErr := resolve(boundary, workspace.ID, TaskOverride{})
		proposedEffective, proposedErr := resolve(proposed, workspace.ID, TaskOverride{})
		if approvedErr != nil || proposedErr != nil || !effectiveWithin(approvedEffective, proposedEffective) {
			return []domainconfig.Issue{envelopeIssue(fmt.Sprintf("$.workspaceOverrides[%s]", workspace.ID), "execution, delivery, capacity, or budget authority exceeds the approved security envelope")}
		}
	}
	return []domainconfig.Issue{}
}

// NewState creates empty active-revision state inside an immutable approved
// security envelope.
func NewState(envelope SecurityEnvelope) (State, error) {
	if !envelope.valid() {
		return State{}, ErrSecurityEnvelopeInvalid
	}
	return State{envelope: envelope}, nil
}

// ResolveEffective computes the active Project -> Workspace -> Task result and
// refuses any Task override outside the immutable envelope.
func (envelope SecurityEnvelope) ResolveEffective(document domainconfig.Document, workspaceID string, task TaskOverride) (EffectiveConfiguration, error) {
	if !envelope.valid() {
		return EffectiveConfiguration{}, ErrSecurityEnvelopeInvalid
	}
	if issues := envelope.issues(document); len(issues) > 0 {
		return EffectiveConfiguration{}, ErrOutsideSecurityEnvelope
	}
	proposed, err := resolve(document.Configuration(), workspaceID, task)
	if err != nil {
		return EffectiveConfiguration{}, err
	}
	boundary, err := resolve(envelope.boundary.Configuration(), workspaceID, TaskOverride{})
	if err != nil || !effectiveWithin(boundary, proposed) {
		return EffectiveConfiguration{}, ErrOutsideSecurityEnvelope
	}
	return proposed, nil
}

// Effective resolves a Task only from this Run's frozen active revision and
// immutable security envelope.
func (snapshot RunConfigurationSnapshot) Effective(workspaceID string, task TaskOverride) (EffectiveConfiguration, error) {
	return snapshot.envelope.ResolveEffective(snapshot.document, workspaceID, task)
}
