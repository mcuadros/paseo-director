// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
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
	IntegrationMode               domainconfig.IntegrationMode
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
	PublishBeforeReview           *bool
	TerminateOnCompletion         *bool
	CancellationCleanup           domainconfig.CancellationCleanupMode
	DeleteRemoteTaskBranch        *bool
	RecoveryRetentionDays         *int64
}

// EffectiveSources reports exactly which layer supplied each value.
type EffectiveSources struct {
	LaunchPolicy                  Scope `json:"launchPolicy"`
	DeliveryMode                  Scope `json:"deliveryMode"`
	IntegrationMode               Scope `json:"integrationMode"`
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
	PublishBeforeReview           Scope `json:"publishBeforeReview"`
	TerminateOnCompletion         Scope `json:"terminateOnCompletion"`
	CancellationCleanup           Scope `json:"cancellationCleanup"`
	DeleteRemoteTaskBranch        Scope `json:"deleteRemoteTaskBranch"`
	RecoveryRetentionDays         Scope `json:"recoveryRetentionDays"`
}

// EffectiveConfiguration is the complete frozen Project -> Workspace -> Task
// result consumed by a future Run. It contains no fallback or host decision.
type EffectiveConfiguration struct {
	LaunchPolicy                  domainconfig.LaunchPolicy            `json:"launchPolicy"`
	DeliveryMode                  domainconfig.DeliveryMode            `json:"deliveryMode"`
	IntegrationMode               domainconfig.IntegrationMode         `json:"integrationMode"`
	Limits                        domainconfig.Limits                  `json:"limits"`
	RunBudget                     domainconfig.RunBudget               `json:"runBudget"`
	AutoFixCIFailures             bool                                 `json:"autoFixCiFailures"`
	AutoFixReviewFeedback         bool                                 `json:"autoFixReviewFeedback"`
	RequireDifferentReviewerModel bool                                 `json:"requireDifferentReviewerModel"`
	PublishBeforeReview           bool                                 `json:"publishBeforeReview"`
	TerminateOnCompletion         bool                                 `json:"terminateOnCompletion"`
	CancellationCleanup           domainconfig.CancellationCleanupMode `json:"cancellationCleanup"`
	DeleteRemoteTaskBranch        bool                                 `json:"deleteRemoteTaskBranch"`
	RecoveryRetentionDays         int64                                `json:"recoveryRetentionDays"`
	GitHubCI                      *domainconfig.GitHubCI               `json:"githubCi,omitempty"`
	Sources                       EffectiveSources                     `json:"sources"`
}

func effectiveIntegrationMode(value domainconfig.IntegrationMode) domainconfig.IntegrationMode {
	if value == "" || value == domainconfig.IntegrationInherit {
		return domainconfig.IntegrationManual
	}
	return value
}

func effectiveBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func effectiveCancellation(value domainconfig.CancellationCleanupMode) domainconfig.CancellationCleanupMode {
	if value == "" || value == domainconfig.CancellationCleanupInherit {
		return domainconfig.CancellationCleanupSnapshotThenDelete
	}
	return value
}

func effectiveRetentionDays(value int64) int64 {
	if value == 0 {
		return 7
	}
	return value
}

// CleanupPolicy freezes termination, cancellation, remote-ref, retention, and
// ADR-0013 resource bounds into the Run.
func (configuration EffectiveConfiguration) CleanupPolicy(configurationSHA256 string) (domaincleanup.Policy, bool) {
	policy, ok := domaincleanup.NewPolicy(configurationSHA256)
	if !ok {
		return domaincleanup.Policy{}, false
	}
	policy.TerminateOnCompletion = configuration.TerminateOnCompletion
	policy.CancellationMode = domaincleanup.CancellationMode(configuration.CancellationCleanup)
	policy.DeleteRemoteTaskBranch = configuration.DeleteRemoteTaskBranch
	policy.RetentionMillis = configuration.RecoveryRetentionDays * 24 * 60 * 60 * 1_000
	policy = domaincleanup.SealPolicy(policy)
	return policy, domaincleanup.ValidPolicy(policy)
}

// IntegrationPolicy freezes the Project -> Workspace -> Task decision into a
// pull-request Run. Direct delivery retains its exact-ref integration policy.
func (configuration EffectiveConfiguration) IntegrationPolicy(configurationSHA256 string) (domainintegration.Policy, bool) {
	if configuration.DeliveryMode != domainconfig.DeliveryPullRequest {
		return domainintegration.Policy{}, false
	}
	return domainintegration.NewPolicy(domainintegration.Mode(configuration.IntegrationMode), configurationSHA256)
}

// ReviewPolicy returns the frozen engine policy consumed by a Run. Connectors
// and provider adapters never infer or relax this configured choice.
func (configuration EffectiveConfiguration) ReviewPolicy() domainreview.ProfilePolicy {
	return domainreview.ProfilePolicy{RequireDifferentReviewerModel: configuration.RequireDifferentReviewerModel}
}

// PublicationPolicy returns the frozen engine-owned PR publication choice.
// A false PublishBeforeReview value is the review-before-PR default.
func (configuration EffectiveConfiguration) PublicationPolicy() publicationdomain.Policy {
	if configuration.DeliveryMode != domainconfig.DeliveryPullRequest {
		return publicationdomain.Policy{}
	}
	return publicationdomain.NewPolicy(string(configuration.DeliveryMode), configuration.PublishBeforeReview, nil)
}

// ValidationPolicy maps only an explicitly approved Organizer check
// configuration. An absent configuration disables GitHub CI; no adapter or
// caller may invent default check names or provider identities.
func (configuration EffectiveConfiguration) ValidationPolicy() (domainvalidation.Policy, bool) {
	if configuration.DeliveryMode != domainconfig.DeliveryPullRequest || configuration.GitHubCI == nil {
		return domainvalidation.Policy{}, false
	}
	checks := make([]domainvalidation.RequiredCheck, len(configuration.GitHubCI.RequiredChecks))
	for index, check := range configuration.GitHubCI.RequiredChecks {
		checks[index] = domainvalidation.RequiredCheck{ID: check.ID, Kind: domainvalidation.CheckKind(check.Kind), Name: check.Name,
			AppID: check.AppID, AppSlug: check.AppSlug, CreatorID: check.CreatorID, CreatorLogin: check.CreatorLogin}
	}
	return domainvalidation.NewPolicy(configuration.GitHubCI.WorkflowID, configuration.GitHubCI.WorkflowName, checks,
		uint64(configuration.GitHubCI.CycleRuntimeSeconds)*1_000)
}

func cloneGitHubCI(value *domainconfig.GitHubCI) *domainconfig.GitHubCI {
	if value == nil {
		return nil
	}
	result := *value
	result.RequiredChecks = append([]domainconfig.GitHubRequiredCheck(nil), value.RequiredChecks...)
	return &result
}

func projectEffective(configuration domainconfig.Configuration) EffectiveConfiguration {
	return EffectiveConfiguration{
		LaunchPolicy:                  configuration.Defaults.LaunchPolicy,
		DeliveryMode:                  configuration.Defaults.DeliveryMode,
		IntegrationMode:               effectiveIntegrationMode(configuration.Defaults.IntegrationMode),
		Limits:                        configuration.Defaults.Limits,
		RunBudget:                     configuration.Defaults.RunBudget,
		AutoFixCIFailures:             configuration.Defaults.AutoFixCIFailures,
		AutoFixReviewFeedback:         configuration.Defaults.AutoFixReviewFeedback,
		RequireDifferentReviewerModel: configuration.Defaults.RequireDifferentReviewerModel,
		PublishBeforeReview:           configuration.Defaults.PublishBeforeReview,
		TerminateOnCompletion:         effectiveBool(configuration.Defaults.TerminateOnCompletion, true),
		CancellationCleanup:           effectiveCancellation(configuration.Defaults.CancellationCleanup),
		DeleteRemoteTaskBranch:        effectiveBool(configuration.Defaults.DeleteRemoteTaskBranch, true),
		RecoveryRetentionDays:         effectiveRetentionDays(configuration.Defaults.RecoveryRetentionDays),
		GitHubCI:                      cloneGitHubCI(configuration.Defaults.GitHubCI),
		Sources: EffectiveSources{
			LaunchPolicy: ScopeProject, DeliveryMode: ScopeProject, IntegrationMode: ScopeProject,
			MaxActiveTasks: ScopeProject, MaxActiveTasksPerWorkspace: ScopeProject,
			MaxConcurrentAgents: ScopeProject, MaxSubagentsPerTask: ScopeProject,
			ElapsedSeconds: ScopeProject, Tokens: ScopeProject, Turns: ScopeProject,
			CICycles: ScopeProject, CostMicrousd: ScopeProject, AutoFixCIFailures: ScopeProject,
			AutoFixReviewFeedback: ScopeProject, RequireDifferentReviewerModel: ScopeProject,
			PublishBeforeReview:   ScopeProject,
			TerminateOnCompletion: ScopeProject, CancellationCleanup: ScopeProject,
			DeleteRemoteTaskBranch: ScopeProject, RecoveryRetentionDays: ScopeProject,
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
	if override.IntegrationMode != "" && override.IntegrationMode != domainconfig.IntegrationInherit {
		result.IntegrationMode = override.IntegrationMode
		result.Sources.IntegrationMode = ScopeWorkspace
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
	applyBool(override.PublishBeforeReview, &result.PublishBeforeReview, &result.Sources.PublishBeforeReview, ScopeWorkspace)
	applyBool(override.TerminateOnCompletion, &result.TerminateOnCompletion, &result.Sources.TerminateOnCompletion, ScopeWorkspace)
	if override.CancellationCleanup != "" && override.CancellationCleanup != domainconfig.CancellationCleanupInherit {
		result.CancellationCleanup, result.Sources.CancellationCleanup = override.CancellationCleanup, ScopeWorkspace
	}
	applyBool(override.DeleteRemoteTaskBranch, &result.DeleteRemoteTaskBranch, &result.Sources.DeleteRemoteTaskBranch, ScopeWorkspace)
	applyInt64(override.RecoveryRetentionDays, &result.RecoveryRetentionDays, &result.Sources.RecoveryRetentionDays, ScopeWorkspace)
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
	if override.IntegrationMode != "" && override.IntegrationMode != domainconfig.IntegrationInherit {
		result.IntegrationMode = override.IntegrationMode
		result.Sources.IntegrationMode = ScopeTask
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
	applyBool(override.PublishBeforeReview, &result.PublishBeforeReview, &result.Sources.PublishBeforeReview, ScopeTask)
	applyBool(override.TerminateOnCompletion, &result.TerminateOnCompletion, &result.Sources.TerminateOnCompletion, ScopeTask)
	if override.CancellationCleanup != "" && override.CancellationCleanup != domainconfig.CancellationCleanupInherit {
		result.CancellationCleanup, result.Sources.CancellationCleanup = override.CancellationCleanup, ScopeTask
	}
	applyBool(override.DeleteRemoteTaskBranch, &result.DeleteRemoteTaskBranch, &result.Sources.DeleteRemoteTaskBranch, ScopeTask)
	applyInt64(override.RecoveryRetentionDays, &result.RecoveryRetentionDays, &result.Sources.RecoveryRetentionDays, ScopeTask)
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
	if override.IntegrationMode != "" && override.IntegrationMode != domainconfig.IntegrationInherit &&
		override.IntegrationMode != domainconfig.IntegrationManual && override.IntegrationMode != domainconfig.IntegrationAutomatic {
		return false
	}
	if override.CancellationCleanup != "" && override.CancellationCleanup != domainconfig.CancellationCleanupInherit &&
		override.CancellationCleanup != domainconfig.CancellationCleanupRetain &&
		override.CancellationCleanup != domainconfig.CancellationCleanupSnapshotThenDelete {
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
	if override.RecoveryRetentionDays != nil && (*override.RecoveryRetentionDays < 1 || *override.RecoveryRetentionDays > 7) {
		return false
	}
	return true
}

func effectiveConsistent(value EffectiveConfiguration) bool {
	return (value.IntegrationMode == domainconfig.IntegrationManual || value.IntegrationMode == domainconfig.IntegrationAutomatic) &&
		(value.CancellationCleanup == domainconfig.CancellationCleanupRetain || value.CancellationCleanup == domainconfig.CancellationCleanupSnapshotThenDelete) &&
		value.RecoveryRetentionDays >= 1 && value.RecoveryRetentionDays <= 7 &&
		value.Limits.MaxActiveTasks >= 1 && value.Limits.MaxActiveTasksPerWorkspace >= 1 &&
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

func integrationWithin(boundary, proposed domainconfig.IntegrationMode) bool {
	return boundary == domainconfig.IntegrationAutomatic || proposed == domainconfig.IntegrationManual
}

func cleanupWithin(boundary, proposed domainconfig.CancellationCleanupMode) bool {
	return boundary == domainconfig.CancellationCleanupSnapshotThenDelete || proposed == domainconfig.CancellationCleanupRetain
}

func effectiveWithin(boundary, proposed EffectiveConfiguration) bool {
	return launchWithin(boundary.LaunchPolicy, proposed.LaunchPolicy) &&
		deliveryWithin(boundary.DeliveryMode, proposed.DeliveryMode) &&
		integrationWithin(boundary.IntegrationMode, proposed.IntegrationMode) &&
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
		(!boundary.RequireDifferentReviewerModel || proposed.RequireDifferentReviewerModel) &&
		(!proposed.PublishBeforeReview || boundary.PublishBeforeReview) &&
		(!proposed.TerminateOnCompletion || boundary.TerminateOnCompletion) &&
		cleanupWithin(boundary.CancellationCleanup, proposed.CancellationCleanup) &&
		(!proposed.DeleteRemoteTaskBranch || boundary.DeleteRemoteTaskBranch) &&
		proposed.RecoveryRetentionDays <= boundary.RecoveryRetentionDays &&
		reflect.DeepEqual(proposed.GitHubCI, boundary.GitHubCI)
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
