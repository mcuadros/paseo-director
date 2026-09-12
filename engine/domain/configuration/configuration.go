// SPDX-License-Identifier: Apache-2.0

// Package configuration owns the closed paseo-director.json contract. It
// parses and validates Organizer configuration as immutable engine input; an
// Organizer is repository/configuration state, not an agent.
package configuration

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	pathpkg "path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/jsondocument"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	"github.com/mcuadros/director-engine/domain/safedata"
)

const (
	// SchemaVersion is the only configuration schema version accepted by this
	// engine build.
	SchemaVersion = 1
	// SchemaID is the exact $schema discovery marker required in
	// paseo-director.json.
	SchemaID = "https://github.com/mcuadros/paseo-director/engine/domain/configuration/paseo-director.schema.json"
	// MaximumDocumentBytes bounds untrusted Organizer configuration before it
	// is decoded or retained in a preview.
	MaximumDocumentBytes = 1 << 20
	// MaximumCanonicalDocumentBytes bounds the canonical representation which
	// is activated and embedded into Run snapshots. Bounding both forms makes
	// every admitted configuration snapshot restorable.
	MaximumCanonicalDocumentBytes = 1 << 20
)

//go:embed paseo-director.schema.json
var embeddedSchema []byte

var (
	identifierPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	tokenPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	githubActorPattern = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})|[A-Za-z0-9][A-Za-z0-9-]{0,38}\[bot\])$`)
)

// LaunchPolicy is the closed launch-policy vocabulary represented by this
// configuration skeleton.
type LaunchPolicy string

const (
	LaunchManual    LaunchPolicy = "manual"
	LaunchAutomatic LaunchPolicy = "automatic"
	LaunchInherit   LaunchPolicy = "inherit"
)

// DeliveryMode is the closed delivery vocabulary represented by this
// configuration skeleton.
type DeliveryMode string

const (
	DeliveryPullRequest DeliveryMode = "pull_request"
	DeliveryDirect      DeliveryMode = "direct"
	DeliveryInherit     DeliveryMode = "inherit"
)

// IntegrationMode is independent from launch policy. Manual is the product
// default; automatic is an explicit expansion of delivery authority.
type IntegrationMode string

const (
	IntegrationManual    IntegrationMode = "manual"
	IntegrationAutomatic IntegrationMode = "automatic"
	IntegrationInherit   IntegrationMode = "inherit"
)

// CancellationCleanupMode controls failed/cancelled Run recovery. The empty
// Project value applies the PLAN snapshot_then_delete default; overrides use
// inherit explicitly.
type CancellationCleanupMode string

const (
	CancellationCleanupSnapshotThenDelete CancellationCleanupMode = "snapshot_then_delete"
	CancellationCleanupRetain             CancellationCleanupMode = "retain"
	CancellationCleanupInherit            CancellationCleanupMode = "inherit"
)

// Provider is an admitted provider-family identity. Exact provider tuples are
// reconciled outside this schema before launch.
type Provider string

const (
	ProviderCodex      Provider = "codex"
	ProviderClaudeCode Provider = "claude-code"
	ProviderOpenCode   Provider = "opencode"
)

// ProviderOptionName is the closed set of non-secret provider-native options
// which Organizer configuration may select. Authentication material is never
// a provider option and cannot be represented by this contract.
type ProviderOptionName string

const (
	ProviderOptionNetworkAccess    ProviderOptionName = "networkAccess"
	ProviderOptionNativeWebSearch  ProviderOptionName = "nativeWebSearch"
	ProviderOptionReasoningSummary ProviderOptionName = "reasoningSummary"
)

// MCPCapability is an engine capability identifier, not a tool name. The
// scoped MCP bridge owned by M3.3 will translate only these frozen values.
type MCPCapability string

const (
	MCPProjectRead              MCPCapability = "project.read"
	MCPPlanningCommandSubmit    MCPCapability = "planning.command.submit"
	MCPTaskRead                 MCPCapability = "task.read"
	MCPTaskOutcomeSubmit        MCPCapability = "task.outcome.submit"
	MCPTaskHelperRequest        MCPCapability = "task.helper.request"
	MCPHelperContributionSubmit MCPCapability = "helper.contribution.submit"
	MCPCandidateRead            MCPCapability = "candidate.read"
	MCPReviewVerdictSubmit      MCPCapability = "review.verdict.submit"
)

// Project identifies the one Director Project represented by an Organizer.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Workspace identifies one canonical product repository. SourcePath is the
// absolute source checkout on the Project daemon, not an execution worktree.
type Workspace struct {
	ID                string `json:"id"`
	Name              string `json:"name,omitempty"`
	Remote            string `json:"remote"`
	SourcePath        string `json:"sourcePath"`
	DefaultBaseBranch string `json:"defaultBaseBranch"`
}

// ProviderOption is one bounded non-secret provider-native selection.
type ProviderOption struct {
	Name  ProviderOptionName `json:"name"`
	Value string             `json:"value"`
}

// AgentSelection is one complete provider choice. All fields are matched
// against one discovered provider variant; fields are never combined across
// variants or inferred by a connector.
type AgentSelection struct {
	Provider        Provider         `json:"provider"`
	Model           string           `json:"model"`
	Effort          string           `json:"effort"`
	Mode            string           `json:"mode"`
	PermissionMode  string           `json:"permissionMode"`
	ProviderOptions []ProviderOption `json:"providerOptions"`
	MCPCapabilities []MCPCapability  `json:"mcpCapabilities"`
}

// AgentProfile records one primary selection and an explicitly ordered
// fallback chain. FallbackChain is required in configuration and is empty by
// default; Director never manufactures entries.
type AgentProfile struct {
	AgentSelection
	FallbackChain []AgentSelection `json:"fallbackChain"`
}

// AgentProfiles keeps the optional planning context, Task Worker, and
// independent Reviewer policies distinct. Organizer is a profile for a
// non-authoritative planning context; it does not turn the Organizer
// repository into an agent or grant lifecycle authority.
type AgentProfiles struct {
	Organizer AgentProfile `json:"organizer"`
	Worker    AgentProfile `json:"worker"`
	Reviewer  AgentProfile `json:"reviewer"`
}

// Limits records Project-level capacity defaults. This package validates only
// intrinsic consistency; launch reducers must still reconcile current facts.
type Limits struct {
	MaxActiveTasks             int `json:"maxActiveTasks"`
	MaxActiveTasksPerWorkspace int `json:"maxActiveTasksPerWorkspace"`
	MaxConcurrentAgents        int `json:"maxConcurrentAgents"`
	MaxSubagentsPerTask        int `json:"maxSubagentsPerTask"`
}

// RunBudget records mandatory finite consumptive limits for a future Run.
type RunBudget struct {
	ElapsedSeconds int64 `json:"elapsedSeconds"`
	Tokens         int64 `json:"tokens"`
	Turns          int64 `json:"turns"`
	CICycles       int64 `json:"ciCycles"`
	// CostMicrousd is optional. Zero disables cost limiting; a positive value
	// is an exact millionth-of-a-US-dollar ceiling and is never inferred from
	// token pricing or another provider estimate.
	CostMicrousd int64 `json:"costMicrousd,omitempty"`
}

// GitHubRequiredCheck freezes the authoritative provider identity. Check Run
// names and legacy Commit Status contexts are never authoritative by display
// text alone.
type GitHubRequiredCheck struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	AppID        int64  `json:"appId,omitempty"`
	AppSlug      string `json:"appSlug,omitempty"`
	CreatorID    int64  `json:"creatorId,omitempty"`
	CreatorLogin string `json:"creatorLogin,omitempty"`
}

// GitHubCI is optional because direct-delivery Projects have no GitHub checks
// phase. Pull-request Runs which enable it freeze this complete object.
type GitHubCI struct {
	WorkflowID          int64                 `json:"workflowId"`
	WorkflowName        string                `json:"workflowName"`
	CycleRuntimeSeconds int64                 `json:"cycleRuntimeSeconds"`
	RequiredChecks      []GitHubRequiredCheck `json:"requiredChecks"`
}

// Defaults is the Project-level configuration inherited by Workspaces and
// Tasks. The application configuration package resolves the effective values.
type Defaults struct {
	LaunchPolicy                  LaunchPolicy            `json:"launchPolicy"`
	DeliveryMode                  DeliveryMode            `json:"deliveryMode"`
	IntegrationMode               IntegrationMode         `json:"integrationMode,omitempty"`
	Limits                        Limits                  `json:"limits"`
	RunBudget                     RunBudget               `json:"runBudget"`
	AutoFixCIFailures             bool                    `json:"autoFixCiFailures"`
	AutoFixReviewFeedback         bool                    `json:"autoFixReviewFeedback"`
	RequireDifferentReviewerModel bool                    `json:"requireDifferentReviewerModel,omitempty"`
	PublishBeforeReview           bool                    `json:"publishBeforeReview,omitempty"`
	TerminateOnCompletion         *bool                   `json:"terminateOnCompletion,omitempty"`
	CancellationCleanup           CancellationCleanupMode `json:"cancellationCleanup,omitempty"`
	DeleteRemoteTaskBranch        *bool                   `json:"deleteRemoteTaskBranch,omitempty"`
	RecoveryRetentionDays         int64                   `json:"recoveryRetentionDays,omitempty"`
	GitHubCI                      *GitHubCI               `json:"githubCi,omitempty"`
}

// WorkspaceOverride records explicit Inherit/concrete selections. Pointer
// fields distinguish an inherited value from an explicit zero or false value.
// Effective reduction remains in the engine application boundary.
type WorkspaceOverride struct {
	WorkspaceID                   string                  `json:"workspaceId"`
	LaunchPolicy                  LaunchPolicy            `json:"launchPolicy"`
	DeliveryMode                  DeliveryMode            `json:"deliveryMode"`
	IntegrationMode               IntegrationMode         `json:"integrationMode,omitempty"`
	MaxActiveTasks                *int                    `json:"maxActiveTasks,omitempty"`
	MaxActiveTasksPerWorkspace    *int                    `json:"maxActiveTasksPerWorkspace,omitempty"`
	MaxConcurrentAgents           *int                    `json:"maxConcurrentAgents,omitempty"`
	MaxSubagentsPerTask           *int                    `json:"maxSubagentsPerTask,omitempty"`
	ElapsedSeconds                *int64                  `json:"elapsedSeconds,omitempty"`
	Tokens                        *int64                  `json:"tokens,omitempty"`
	Turns                         *int64                  `json:"turns,omitempty"`
	CICycles                      *int64                  `json:"ciCycles,omitempty"`
	CostMicrousd                  *int64                  `json:"costMicrousd,omitempty"`
	AutoFixCIFailures             *bool                   `json:"autoFixCiFailures,omitempty"`
	AutoFixReviewFeedback         *bool                   `json:"autoFixReviewFeedback,omitempty"`
	RequireDifferentReviewerModel *bool                   `json:"requireDifferentReviewerModel,omitempty"`
	PublishBeforeReview           *bool                   `json:"publishBeforeReview,omitempty"`
	TerminateOnCompletion         *bool                   `json:"terminateOnCompletion,omitempty"`
	CancellationCleanup           CancellationCleanupMode `json:"cancellationCleanup,omitempty"`
	DeleteRemoteTaskBranch        *bool                   `json:"deleteRemoteTaskBranch,omitempty"`
	RecoveryRetentionDays         *int64                  `json:"recoveryRetentionDays,omitempty"`
}

// FileReference identifies one explicitly included Organizer file. Directory
// discovery is intentionally unsupported.
type FileReference struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// Configuration is the closed schema for paseo-director.json.
type Configuration struct {
	Schema             string              `json:"$schema"`
	SchemaVersion      int                 `json:"schemaVersion"`
	Project            Project             `json:"project"`
	Workspaces         []Workspace         `json:"workspaces"`
	AgentProfiles      AgentProfiles       `json:"agentProfiles"`
	Defaults           Defaults            `json:"defaults"`
	WorkspaceOverrides []WorkspaceOverride `json:"workspaceOverrides"`
	Skills             []FileReference     `json:"skills"`
	Templates          []FileReference     `json:"templates"`
}

// Issue is one deterministic schema or semantic validation result.
type Issue struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError contains the complete deterministic issue set for a
// rejected document.
type ValidationError struct {
	Issues []Issue
}

func (err *ValidationError) Error() string {
	if len(err.Issues) == 0 {
		return "invalid Organizer configuration"
	}
	return fmt.Sprintf("invalid Organizer configuration: %s at %s", err.Issues[0].Code, err.Issues[0].Path)
}

// ValidationIssues returns a defensive copy when err is a configuration
// validation error.
func ValidationIssues(err error) ([]Issue, bool) {
	var validation *ValidationError
	if !errors.As(err, &validation) {
		return nil, false
	}
	return slices.Clone(validation.Issues), true
}

// Document is one validated immutable configuration and its canonical bytes.
type Document struct {
	configuration Configuration
	canonical     []byte
	sha256        string
}

// Schema returns a defensive copy of the published JSON Schema.
func Schema() []byte {
	return slices.Clone(embeddedSchema)
}

// SchemaSHA256 identifies the canonical closed Organizer configuration
// contract so generated consumers and tests detect profile-schema drift.
func SchemaSHA256() (string, error) {
	canonical, err := jsondocument.Canonical(embeddedSchema)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// CanonicalJSON returns a defensive copy of the validated canonical document.
func (document Document) CanonicalJSON() []byte {
	return slices.Clone(document.canonical)
}

// SHA256 returns the lowercase SHA-256 of CanonicalJSON.
func (document Document) SHA256() string {
	return document.sha256
}

// Configuration returns a deep copy of the typed configuration.
func (document Document) Configuration() Configuration {
	return cloneConfiguration(document.configuration)
}

func cloneConfiguration(value Configuration) Configuration {
	value.Workspaces = slices.Clone(value.Workspaces)
	value.AgentProfiles.Organizer = cloneAgentProfile(value.AgentProfiles.Organizer)
	value.AgentProfiles.Worker = cloneAgentProfile(value.AgentProfiles.Worker)
	value.AgentProfiles.Reviewer = cloneAgentProfile(value.AgentProfiles.Reviewer)
	value.Defaults.TerminateOnCompletion = clonePointer(value.Defaults.TerminateOnCompletion)
	value.Defaults.DeleteRemoteTaskBranch = clonePointer(value.Defaults.DeleteRemoteTaskBranch)
	if value.Defaults.GitHubCI != nil {
		githubCI := *value.Defaults.GitHubCI
		githubCI.RequiredChecks = slices.Clone(githubCI.RequiredChecks)
		value.Defaults.GitHubCI = &githubCI
	}
	value.WorkspaceOverrides = cloneWorkspaceOverrides(value.WorkspaceOverrides)
	value.Skills = slices.Clone(value.Skills)
	value.Templates = slices.Clone(value.Templates)
	return value
}

func cloneAgentSelection(value AgentSelection) AgentSelection {
	value.ProviderOptions = slices.Clone(value.ProviderOptions)
	value.MCPCapabilities = slices.Clone(value.MCPCapabilities)
	return value
}

func cloneAgentProfile(value AgentProfile) AgentProfile {
	value.AgentSelection = cloneAgentSelection(value.AgentSelection)
	value.FallbackChain = slices.Clone(value.FallbackChain)
	for index := range value.FallbackChain {
		value.FallbackChain[index] = cloneAgentSelection(value.FallbackChain[index])
	}
	return value
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneWorkspaceOverrides(values []WorkspaceOverride) []WorkspaceOverride {
	cloned := slices.Clone(values)
	for index := range cloned {
		cloned[index].MaxActiveTasks = clonePointer(cloned[index].MaxActiveTasks)
		cloned[index].MaxActiveTasksPerWorkspace = clonePointer(cloned[index].MaxActiveTasksPerWorkspace)
		cloned[index].MaxConcurrentAgents = clonePointer(cloned[index].MaxConcurrentAgents)
		cloned[index].MaxSubagentsPerTask = clonePointer(cloned[index].MaxSubagentsPerTask)
		cloned[index].ElapsedSeconds = clonePointer(cloned[index].ElapsedSeconds)
		cloned[index].Tokens = clonePointer(cloned[index].Tokens)
		cloned[index].Turns = clonePointer(cloned[index].Turns)
		cloned[index].CICycles = clonePointer(cloned[index].CICycles)
		cloned[index].AutoFixCIFailures = clonePointer(cloned[index].AutoFixCIFailures)
		cloned[index].AutoFixReviewFeedback = clonePointer(cloned[index].AutoFixReviewFeedback)
		cloned[index].RequireDifferentReviewerModel = clonePointer(cloned[index].RequireDifferentReviewerModel)
		cloned[index].PublishBeforeReview = clonePointer(cloned[index].PublishBeforeReview)
		cloned[index].TerminateOnCompletion = clonePointer(cloned[index].TerminateOnCompletion)
		cloned[index].DeleteRemoteTaskBranch = clonePointer(cloned[index].DeleteRemoteTaskBranch)
		cloned[index].RecoveryRetentionDays = clonePointer(cloned[index].RecoveryRetentionDays)
	}
	return cloned
}

func issue(code, path, message string) Issue {
	return Issue{Code: code, Path: path, Message: message}
}

func invalidDocument(code, message string) error {
	return &ValidationError{Issues: []Issue{issue(code, "$", message)}}
}

func sortedValidationError(issues []Issue) error {
	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Path != issues[right].Path {
			return issues[left].Path < issues[right].Path
		}
		return issues[left].Code < issues[right].Code
	})
	return &ValidationError{Issues: slices.Clone(issues)}
}

func validIdentifier(value string) bool {
	return identifierPattern.MatchString(value)
}

func validBoundedName(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && len(value) > 0 && len(value) <= maximum &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func validToken(value string) bool {
	return tokenPattern.MatchString(value)
}

func decodeRemoteEscapes(value string) (string, bool) {
	decoded := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			decoded = append(decoded, value[index])
			continue
		}
		if index+2 >= len(value) {
			return "", false
		}
		part, err := strconv.ParseUint(value[index+1:index+3], 16, 8)
		if err != nil {
			return "", false
		}
		decoded = append(decoded, byte(part))
		index += 2
	}
	return string(decoded), utf8.Valid(decoded)
}

func unsafeWhitespaceOrControl(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf)
}

func hasUnsafeRemoteWhitespace(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unsafeWhitespaceOrControl(r)
	}) >= 0
}

func hasUnsafeRemoteCommandSyntax(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return strings.ContainsRune("\\\"'`$;&|<>", r)
	}) >= 0
}

func remoteAuthority(value string) string {
	if separator := strings.Index(value, "://"); separator >= 0 {
		value = value[separator+3:]
	}
	if slash := strings.IndexByte(value, '/'); slash >= 0 {
		return value[:slash]
	}
	return value
}

func hasPasswordUserinfo(value string) bool {
	authority := remoteAuthority(value)
	at := strings.LastIndexByte(authority, '@')
	return at >= 0 && strings.ContainsRune(authority[:at], ':')
}

func hasGitRemoteHelperDispatch(value string) bool {
	if strings.Contains(value, "://") {
		return false
	}
	authority := remoteAuthority(value)
	separator := strings.IndexByte(authority, ':')
	if separator < 0 || separator+1 >= len(authority) || authority[separator+1] != ':' {
		return false
	}
	usernameSeparator := strings.IndexByte(authority, '@')
	return usernameSeparator < 0 || usernameSeparator > separator
}

func validateRemote(value string) (repositorydomain.Remote, string, string) {
	if len(value) == 0 || len(value) > repositorydomain.MaximumRemoteBytes || strings.HasPrefix(value, "-") {
		return repositorydomain.Remote{}, "remote_invalid", "remote must be bounded and cannot begin with an option prefix"
	}
	decoded, ok := decodeRemoteEscapes(value)
	if !ok {
		return repositorydomain.Remote{}, "remote_format_invalid", "remote contains invalid percent-encoding or Unicode"
	}
	if hasUnsafeRemoteWhitespace(decoded) {
		return repositorydomain.Remote{}, "remote_whitespace_unsafe", "remote cannot contain whitespace or control characters"
	}
	if hasPasswordUserinfo(decoded) {
		return repositorydomain.Remote{}, "remote_userinfo_password", "remote userinfo cannot contain a password"
	}
	if hasUnsafeRemoteCommandSyntax(decoded) {
		return repositorydomain.Remote{}, "remote_command_unsafe", "remote cannot contain command-bearing syntax"
	}
	if hasGitRemoteHelperDispatch(decoded) {
		return repositorydomain.Remote{}, "remote_helper_unsupported", "Git remote-helper dispatch is not permitted"
	}
	if separator := strings.Index(decoded, "://"); separator >= 0 {
		scheme := strings.ToLower(decoded[:separator])
		if scheme != "https" && scheme != "ssh" && scheme != "git" {
			return repositorydomain.Remote{}, "remote_scheme_unsupported", "remote scheme must be https, ssh, or git"
		}
		authority := remoteAuthority(decoded)
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			if scheme != "ssh" {
				return repositorydomain.Remote{}, "remote_userinfo_forbidden", "remote userinfo is forbidden for this transport"
			}
			if authority[:at] != "git" {
				return repositorydomain.Remote{}, "remote_username_unsupported", "SSH remote username must be the closed non-secret git identity"
			}
		}
	} else if at := strings.LastIndexByte(remoteAuthority(decoded), '@'); at >= 0 &&
		remoteAuthority(decoded)[:at] != "git" {
		return repositorydomain.Remote{}, "remote_username_unsupported", "SCP remote username must be the closed non-secret git identity"
	}
	remote, err := repositorydomain.CanonicalRemote(value)
	if err != nil {
		return repositorydomain.Remote{}, "remote_format_invalid", "remote authority, address, port, or repository path is invalid"
	}
	return remote, "", ""
}

func validGitBranch(value string) bool {
	if len(value) == 0 || len(value) > 255 || value == "@" || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".") || strings.HasSuffix(value, ".lock") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") ||
		strings.Contains(value, "//") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r)
	}) < 0
}

func validSourcePath(value string) bool {
	return len(value) >= 2 && len(value) <= 4096 && strings.HasPrefix(value, "/") &&
		!strings.Contains(value, "\\") && pathpkg.Clean(value) == value &&
		strings.IndexFunc(value, unsafeWhitespaceOrControl) < 0
}

func validProviderOption(option ProviderOption) bool {
	switch option.Name {
	case ProviderOptionNetworkAccess, ProviderOptionNativeWebSearch:
		return option.Value == "disabled" || option.Value == "enabled"
	case ProviderOptionReasoningSummary:
		return option.Value == "disabled" || option.Value == "concise" || option.Value == "detailed"
	default:
		return false
	}
}

func capabilityAllowed(role string, capability MCPCapability) bool {
	switch role {
	case "organizer":
		return capability == MCPProjectRead || capability == MCPPlanningCommandSubmit
	case "worker":
		return capability == MCPProjectRead || capability == MCPTaskRead || capability == MCPTaskOutcomeSubmit ||
			capability == MCPTaskHelperRequest
	case "reviewer":
		return capability == MCPCandidateRead || capability == MCPReviewVerdictSubmit
	default:
		return false
	}
}

func requiredCapabilities(role string) []MCPCapability {
	switch role {
	case "organizer":
		return []MCPCapability{MCPProjectRead, MCPPlanningCommandSubmit}
	case "worker":
		return []MCPCapability{MCPTaskRead, MCPTaskOutcomeSubmit}
	case "reviewer":
		return []MCPCapability{MCPCandidateRead, MCPReviewVerdictSubmit}
	default:
		return nil
	}
}

func selectionKey(selection AgentSelection) string {
	selection.ProviderOptions = slices.Clone(selection.ProviderOptions)
	sort.Slice(selection.ProviderOptions, func(left, right int) bool {
		if selection.ProviderOptions[left].Name != selection.ProviderOptions[right].Name {
			return selection.ProviderOptions[left].Name < selection.ProviderOptions[right].Name
		}
		return selection.ProviderOptions[left].Value < selection.ProviderOptions[right].Value
	})
	selection.MCPCapabilities = slices.Clone(selection.MCPCapabilities)
	sort.Slice(selection.MCPCapabilities, func(left, right int) bool {
		return selection.MCPCapabilities[left] < selection.MCPCapabilities[right]
	})
	encoded, _ := json.Marshal(selection)
	return string(encoded)
}

func validateSelection(path, role string, selection AgentSelection, issues *[]Issue) {
	if selection.Provider != ProviderCodex && selection.Provider != ProviderClaudeCode && selection.Provider != ProviderOpenCode {
		*issues = append(*issues, issue("provider_unsupported", path+".provider", "provider must be codex, claude-code, or opencode"))
	}
	for field, value := range map[string]string{
		"model": selection.Model, "effort": selection.Effort, "mode": selection.Mode,
		"permissionMode": selection.PermissionMode,
	} {
		if !validToken(value) {
			*issues = append(*issues, issue("token_invalid", path+"."+field, "value must be a bounded provider token"))
		}
	}
	if selection.PermissionMode != "read-only" && selection.PermissionMode != "workspace-write" {
		*issues = append(*issues, issue("permission_mode_unsupported", path+".permissionMode", "permission mode must be read-only or workspace-write"))
	}
	if role == "reviewer" && selection.PermissionMode != "read-only" {
		*issues = append(*issues, issue("reviewer_permission_expansion", path+".permissionMode", "Reviewer permission mode must be read-only"))
	}
	if role == "organizer" && selection.PermissionMode != "read-only" {
		*issues = append(*issues, issue("organizer_permission_expansion", path+".permissionMode", "Organizer planning context permission mode must be read-only"))
	}
	if selection.ProviderOptions == nil {
		*issues = append(*issues, issue("field_required", path+".providerOptions", "an explicit provider option array is required"))
	}
	if len(selection.ProviderOptions) > 16 {
		*issues = append(*issues, issue("provider_option_count", path+".providerOptions", "at most 16 provider options are permitted"))
	}
	optionNames := make(map[ProviderOptionName]struct{}, len(selection.ProviderOptions))
	for index, option := range selection.ProviderOptions {
		base := fmt.Sprintf("%s.providerOptions[%d]", path, index)
		if !validProviderOption(option) {
			*issues = append(*issues, issue("provider_option_unsupported", base, "provider option name and value must use the closed non-secret vocabulary"))
		}
		if _, duplicate := optionNames[option.Name]; duplicate {
			*issues = append(*issues, issue("provider_option_duplicate", base+".name", "provider option names must be unique"))
		}
		optionNames[option.Name] = struct{}{}
	}
	if selection.MCPCapabilities == nil {
		*issues = append(*issues, issue("field_required", path+".mcpCapabilities", "an explicit MCP capability array is required"))
	}
	if len(selection.MCPCapabilities) == 0 || len(selection.MCPCapabilities) > 16 {
		*issues = append(*issues, issue("mcp_capability_count", path+".mcpCapabilities", "between 1 and 16 MCP capabilities are required"))
	}
	capabilities := make(map[MCPCapability]struct{}, len(selection.MCPCapabilities))
	for index, capability := range selection.MCPCapabilities {
		base := fmt.Sprintf("%s.mcpCapabilities[%d]", path, index)
		if !capabilityAllowed(role, capability) {
			*issues = append(*issues, issue("mcp_capability_forbidden", base, "MCP capability is not permitted for this role"))
		}
		if _, duplicate := capabilities[capability]; duplicate {
			*issues = append(*issues, issue("mcp_capability_duplicate", base, "MCP capabilities must be unique"))
		}
		capabilities[capability] = struct{}{}
	}
	for _, required := range requiredCapabilities(role) {
		if _, present := capabilities[required]; !present {
			*issues = append(*issues, issue("mcp_capability_required", path+".mcpCapabilities", "role-required MCP capability is missing"))
		}
	}
}

func validateProfile(path, role string, profile AgentProfile, issues *[]Issue) {
	validateSelection(path, role, profile.AgentSelection, issues)
	if profile.FallbackChain == nil {
		*issues = append(*issues, issue("field_required", path+".fallbackChain", "an explicit ordered fallback array is required"))
	}
	if len(profile.FallbackChain) > 8 {
		*issues = append(*issues, issue("fallback_count", path+".fallbackChain", "at most eight explicit fallbacks are permitted"))
	}
	seen := map[string]struct{}{selectionKey(profile.AgentSelection): {}}
	for index, fallback := range profile.FallbackChain {
		base := fmt.Sprintf("%s.fallbackChain[%d]", path, index)
		validateSelection(base, role, fallback, issues)
		key := selectionKey(fallback)
		if _, duplicate := seen[key]; duplicate {
			*issues = append(*issues, issue("fallback_duplicate", base, "fallback selections must be unique and cannot repeat the primary"))
		}
		seen[key] = struct{}{}
	}
}

func validateReferences(field, prefix, suffix string, references []FileReference, issues *[]Issue) {
	if references == nil {
		*issues = append(*issues, issue("field_required", field, "an explicit reference array is required"))
		return
	}
	if len(references) == 0 || len(references) > 128 {
		*issues = append(*issues, issue("reference_count", field, "between 1 and 128 explicit references are required"))
	}
	identifiers := make(map[string]struct{}, len(references))
	paths := make(map[string]struct{}, len(references))
	for index, reference := range references {
		base := fmt.Sprintf("%s[%d]", field, index)
		if !validIdentifier(reference.ID) {
			*issues = append(*issues, issue("identifier_invalid", base+".id", "reference id must be a lowercase Director identifier"))
		} else if _, duplicate := identifiers[reference.ID]; duplicate {
			*issues = append(*issues, issue("identifier_duplicate", base+".id", "reference id must be unique in its collection"))
		}
		identifiers[reference.ID] = struct{}{}
		validPath := len(reference.Path) <= 512 && !strings.Contains(reference.Path, "\\") &&
			!pathpkg.IsAbs(reference.Path) && pathpkg.Clean(reference.Path) == reference.Path &&
			strings.HasPrefix(reference.Path, prefix) && strings.HasSuffix(reference.Path, suffix) &&
			strings.IndexFunc(reference.Path, unsafeWhitespaceOrControl) < 0
		if !validPath {
			*issues = append(*issues, issue("reference_path_invalid", base+".path", "reference path must be a clean relative path in its declared Organizer directory"))
		} else if _, duplicate := paths[reference.Path]; duplicate {
			*issues = append(*issues, issue("reference_path_duplicate", base+".path", "reference path must be unique in its collection"))
		}
		paths[reference.Path] = struct{}{}
	}
}

func workspaceOverrideEmpty(override WorkspaceOverride) bool {
	return override.LaunchPolicy == LaunchInherit && override.DeliveryMode == DeliveryInherit &&
		(override.IntegrationMode == "" || override.IntegrationMode == IntegrationInherit) &&
		override.MaxActiveTasks == nil && override.MaxActiveTasksPerWorkspace == nil &&
		override.MaxConcurrentAgents == nil && override.MaxSubagentsPerTask == nil &&
		override.ElapsedSeconds == nil && override.Tokens == nil && override.Turns == nil &&
		override.CICycles == nil && override.CostMicrousd == nil && override.AutoFixCIFailures == nil &&
		override.AutoFixReviewFeedback == nil && override.RequireDifferentReviewerModel == nil && override.PublishBeforeReview == nil &&
		override.TerminateOnCompletion == nil &&
		(override.CancellationCleanup == "" || override.CancellationCleanup == CancellationCleanupInherit) &&
		override.DeleteRemoteTaskBranch == nil && override.RecoveryRetentionDays == nil
}

func effectiveWorkspaceLimits(project Limits, override WorkspaceOverride) Limits {
	result := project
	for destination, source := range map[*int]*int{
		&result.MaxActiveTasks:             override.MaxActiveTasks,
		&result.MaxActiveTasksPerWorkspace: override.MaxActiveTasksPerWorkspace,
		&result.MaxConcurrentAgents:        override.MaxConcurrentAgents,
		&result.MaxSubagentsPerTask:        override.MaxSubagentsPerTask,
	} {
		if source != nil {
			*destination = *source
		}
	}
	return result
}

func effectiveWorkspaceBudget(project RunBudget, override WorkspaceOverride) RunBudget {
	result := project
	for destination, source := range map[*int64]*int64{
		&result.ElapsedSeconds: override.ElapsedSeconds,
		&result.Tokens:         override.Tokens,
		&result.Turns:          override.Turns,
		&result.CICycles:       override.CICycles,
		&result.CostMicrousd:   override.CostMicrousd,
	} {
		if source != nil {
			*destination = *source
		}
	}
	return result
}

func validateLimits(path string, limits Limits, issues *[]Issue) {
	for field, current := range map[string]int{
		"maxActiveTasks":             limits.MaxActiveTasks,
		"maxActiveTasksPerWorkspace": limits.MaxActiveTasksPerWorkspace,
		"maxConcurrentAgents":        limits.MaxConcurrentAgents,
	} {
		if current < 1 {
			*issues = append(*issues, issue("limit_invalid", path+"."+field, "limit must be a positive integer"))
		}
	}
	if limits.MaxSubagentsPerTask < 0 {
		*issues = append(*issues, issue("limit_invalid", path+".maxSubagentsPerTask", "subagent limit cannot be negative"))
	}
	if limits.MaxActiveTasksPerWorkspace > limits.MaxActiveTasks {
		*issues = append(*issues, issue("limit_inconsistent", path+".maxActiveTasksPerWorkspace", "per-Workspace active Task limit cannot exceed the Project limit"))
	}
	if limits.MaxActiveTasks > limits.MaxConcurrentAgents {
		*issues = append(*issues, issue("limit_inconsistent", path+".maxConcurrentAgents", "agent capacity cannot be lower than active Task capacity"))
	}
}

func validateRunBudget(path string, budget RunBudget, issues *[]Issue) {
	for field, current := range map[string]int64{
		"elapsedSeconds": budget.ElapsedSeconds,
		"tokens":         budget.Tokens,
		"turns":          budget.Turns,
		"ciCycles":       budget.CICycles,
	} {
		if current < 1 {
			*issues = append(*issues, issue("budget_invalid", path+"."+field, "Run budget must be a positive integer"))
		}
	}
	if budget.Turns > 256 {
		*issues = append(*issues, issue("budget_invalid", path+".turns", "Run turn budget cannot exceed the durable usage ledger bound of 256"))
	}
	if budget.CICycles > 256 {
		*issues = append(*issues, issue("budget_invalid", path+".ciCycles", "Run CI cycle budget cannot exceed the durable activity ledger bound of 256"))
	}
	if budget.CostMicrousd < 0 {
		*issues = append(*issues, issue("budget_invalid", path+".costMicrousd", "optional Run cost budget cannot be negative"))
	}
}

func validateGitHubCI(path string, configuration *GitHubCI, budget RunBudget, delivery DeliveryMode, issues *[]Issue) {
	if configuration == nil {
		return
	}
	if delivery != DeliveryPullRequest {
		*issues = append(*issues, issue("github_ci_delivery_invalid", path, "GitHub CI can be configured only for pull-request delivery"))
	}
	if configuration.WorkflowID <= 0 {
		*issues = append(*issues, issue("github_ci_workflow_invalid", path+".workflowId", "workflow database ID must be positive"))
	}
	if !validBoundedName(configuration.WorkflowName, 200) || safedata.ContainsSecret(configuration.WorkflowName) {
		*issues = append(*issues, issue("github_ci_workflow_invalid", path+".workflowName", "workflow name must be bounded, trimmed, and non-secret"))
	}
	if configuration.CycleRuntimeSeconds < 1 || configuration.CycleRuntimeSeconds > 21_600 ||
		configuration.CycleRuntimeSeconds >= budget.ElapsedSeconds {
		*issues = append(*issues, issue("github_ci_runtime_invalid", path+".cycleRuntimeSeconds", "CI runtime must be positive, at most six hours, and below the Run elapsed-time budget"))
	}
	if budget.CICycles > 4 {
		*issues = append(*issues, issue("github_ci_cycle_limit_invalid", "$.defaults.runBudget.ciCycles", "GitHub CI permits at most four total cycles per Run"))
	}
	if len(configuration.RequiredChecks) == 0 || len(configuration.RequiredChecks) > 32 {
		*issues = append(*issues, issue("github_ci_checks_invalid", path+".requiredChecks", "between one and 32 required checks are required"))
	}
	seen := make(map[string]struct{}, len(configuration.RequiredChecks))
	providers := make(map[string]struct{}, len(configuration.RequiredChecks))
	for index, check := range configuration.RequiredChecks {
		base := fmt.Sprintf("%s.requiredChecks[%d]", path, index)
		if !validIdentifier(check.ID) {
			*issues = append(*issues, issue("github_ci_check_invalid", base+".id", "check id must be a lowercase Director identifier"))
		} else if _, duplicate := seen[check.ID]; duplicate {
			*issues = append(*issues, issue("github_ci_check_duplicate", base+".id", "check id must be unique"))
		}
		seen[check.ID] = struct{}{}
		provider := strings.Join([]string{check.Kind, check.Name, strconv.FormatInt(check.AppID, 10), strconv.FormatInt(check.CreatorID, 10)}, "\x1f")
		if _, duplicate := providers[provider]; duplicate {
			*issues = append(*issues, issue("github_ci_check_provider_duplicate", base, "provider-bound check identity must be unique"))
		}
		providers[provider] = struct{}{}
		if !validBoundedName(check.Name, 200) || safedata.ContainsSecret(check.Name) {
			*issues = append(*issues, issue("github_ci_check_invalid", base+".name", "check name must be bounded, trimmed, and non-secret"))
		}
		switch check.Kind {
		case "check_run":
			if check.AppID <= 0 || !validToken(check.AppSlug) || check.CreatorID != 0 || check.CreatorLogin != "" {
				*issues = append(*issues, issue("github_ci_check_identity_invalid", base, "Check Run identity requires only appId and appSlug"))
			}
		case "commit_status":
			if check.CreatorID <= 0 || !githubActorPattern.MatchString(check.CreatorLogin) || check.AppID != 0 || check.AppSlug != "" {
				*issues = append(*issues, issue("github_ci_check_identity_invalid", base, "Commit Status identity requires only creatorId and creatorLogin"))
			}
		default:
			*issues = append(*issues, issue("github_ci_check_kind_invalid", base+".kind", "check kind must be check_run or commit_status"))
		}
	}
}

func validate(value Configuration) error {
	var issues []Issue
	if value.Schema != SchemaID {
		issues = append(issues, issue("schema_id_unsupported", "$.$schema", "configuration must name the engine-owned schema"))
	}
	if value.SchemaVersion != SchemaVersion {
		issues = append(issues, issue("schema_version_unsupported", "$.schemaVersion", "configuration schemaVersion must be 1"))
	}
	if !validIdentifier(value.Project.ID) {
		issues = append(issues, issue("identifier_invalid", "$.project.id", "project id must be a lowercase Director identifier"))
	}
	if !validBoundedName(value.Project.Name, 128) {
		issues = append(issues, issue("name_invalid", "$.project.name", "project name must be non-empty, trimmed, and at most 128 bytes"))
	}
	if value.Workspaces == nil {
		issues = append(issues, issue("field_required", "$.workspaces", "an explicit Workspace array is required"))
	}
	if len(value.Workspaces) == 0 || len(value.Workspaces) > 128 {
		issues = append(issues, issue("workspace_count", "$.workspaces", "between 1 and 128 Workspaces are required"))
	}
	workspaceIDs := make(map[string]struct{}, len(value.Workspaces))
	workspaceRepositories := make(map[string]struct{}, len(value.Workspaces))
	workspacePaths := make(map[string]struct{}, len(value.Workspaces))
	for index, workspace := range value.Workspaces {
		base := fmt.Sprintf("$.workspaces[%d]", index)
		if !validIdentifier(workspace.ID) {
			issues = append(issues, issue("identifier_invalid", base+".id", "workspace id must be a lowercase Director identifier"))
		} else if _, duplicate := workspaceIDs[workspace.ID]; duplicate {
			issues = append(issues, issue("workspace_duplicate", base+".id", "workspace id must be unique"))
		}
		workspaceIDs[workspace.ID] = struct{}{}
		if workspace.Name != "" && !validBoundedName(workspace.Name, 128) {
			issues = append(issues, issue("name_invalid", base+".name", "Workspace name must be trimmed and at most 128 bytes"))
		}
		if remote, code, message := validateRemote(workspace.Remote); code != "" {
			issues = append(issues, issue(code, base+".remote", message))
		} else if _, duplicate := workspaceRepositories[remote.Key]; duplicate {
			issues = append(issues, issue("workspace_repository_duplicate", base+".remote", "one canonical repository can map to only one Workspace in a Project"))
		} else {
			workspaceRepositories[remote.Key] = struct{}{}
		}
		if !validSourcePath(workspace.SourcePath) {
			issues = append(issues, issue("source_path_invalid", base+".sourcePath", "source path must be a clean absolute Linux path"))
		} else if _, duplicate := workspacePaths[workspace.SourcePath]; duplicate {
			issues = append(issues, issue("workspace_source_duplicate", base+".sourcePath", "one canonical source path can map to only one Workspace in a Project"))
		} else {
			workspacePaths[workspace.SourcePath] = struct{}{}
		}
		if !validGitBranch(workspace.DefaultBaseBranch) {
			issues = append(issues, issue("base_branch_invalid", base+".defaultBaseBranch", "default base branch is not a safe Git branch name"))
		}
	}
	validateProfile("$.agentProfiles.organizer", "organizer", value.AgentProfiles.Organizer, &issues)
	validateProfile("$.agentProfiles.worker", "worker", value.AgentProfiles.Worker, &issues)
	validateProfile("$.agentProfiles.reviewer", "reviewer", value.AgentProfiles.Reviewer, &issues)
	if value.Defaults.LaunchPolicy != LaunchManual && value.Defaults.LaunchPolicy != LaunchAutomatic {
		issues = append(issues, issue("launch_policy_invalid", "$.defaults.launchPolicy", "Project launch policy must be manual or automatic"))
	}
	if value.Defaults.DeliveryMode != DeliveryPullRequest && value.Defaults.DeliveryMode != DeliveryDirect {
		issues = append(issues, issue("delivery_mode_invalid", "$.defaults.deliveryMode", "Project delivery mode must be pull_request or direct"))
	}
	if value.Defaults.IntegrationMode != "" && value.Defaults.IntegrationMode != IntegrationManual && value.Defaults.IntegrationMode != IntegrationAutomatic {
		issues = append(issues, issue("integration_mode_invalid", "$.defaults.integrationMode", "Project integration mode must be manual or automatic"))
	}
	if value.Defaults.CancellationCleanup != "" && value.Defaults.CancellationCleanup != CancellationCleanupRetain &&
		value.Defaults.CancellationCleanup != CancellationCleanupSnapshotThenDelete {
		issues = append(issues, issue("cancellation_cleanup_invalid", "$.defaults.cancellationCleanup", "cancellation cleanup must be retain or snapshot_then_delete"))
	}
	if value.Defaults.RecoveryRetentionDays < 0 || value.Defaults.RecoveryRetentionDays > 7 {
		issues = append(issues, issue("recovery_retention_invalid", "$.defaults.recoveryRetentionDays", "recovery retention must be between one and seven days when set"))
	}
	validateLimits("$.defaults.limits", value.Defaults.Limits, &issues)
	validateRunBudget("$.defaults.runBudget", value.Defaults.RunBudget, &issues)
	validateGitHubCI("$.defaults.githubCi", value.Defaults.GitHubCI, value.Defaults.RunBudget, value.Defaults.DeliveryMode, &issues)
	if value.WorkspaceOverrides == nil {
		issues = append(issues, issue("field_required", "$.workspaceOverrides", "an explicit Workspace override array is required"))
	}
	if len(value.WorkspaceOverrides) > 128 {
		issues = append(issues, issue("override_count", "$.workspaceOverrides", "at most 128 Workspace overrides are permitted"))
	}
	overridden := make(map[string]struct{}, len(value.WorkspaceOverrides))
	for index, override := range value.WorkspaceOverrides {
		base := fmt.Sprintf("$.workspaceOverrides[%d]", index)
		if _, known := workspaceIDs[override.WorkspaceID]; !known {
			issues = append(issues, issue("workspace_unknown", base+".workspaceId", "override must reference a declared Workspace"))
		} else if _, duplicate := overridden[override.WorkspaceID]; duplicate {
			issues = append(issues, issue("override_duplicate", base+".workspaceId", "a Workspace can have at most one override"))
		}
		overridden[override.WorkspaceID] = struct{}{}
		if override.LaunchPolicy != LaunchInherit && override.LaunchPolicy != LaunchManual && override.LaunchPolicy != LaunchAutomatic {
			issues = append(issues, issue("launch_policy_invalid", base+".launchPolicy", "override launch policy must be inherit, manual, or automatic"))
		}
		if override.DeliveryMode != DeliveryInherit && override.DeliveryMode != DeliveryPullRequest && override.DeliveryMode != DeliveryDirect {
			issues = append(issues, issue("delivery_mode_invalid", base+".deliveryMode", "override delivery mode must be inherit, pull_request, or direct"))
		}
		if override.IntegrationMode != "" && override.IntegrationMode != IntegrationInherit && override.IntegrationMode != IntegrationManual && override.IntegrationMode != IntegrationAutomatic {
			issues = append(issues, issue("integration_mode_invalid", base+".integrationMode", "override integration mode must be inherit, manual, or automatic"))
		}
		if override.CancellationCleanup != "" && override.CancellationCleanup != CancellationCleanupInherit &&
			override.CancellationCleanup != CancellationCleanupRetain && override.CancellationCleanup != CancellationCleanupSnapshotThenDelete {
			issues = append(issues, issue("cancellation_cleanup_invalid", base+".cancellationCleanup", "override cancellation cleanup must be inherit, retain, or snapshot_then_delete"))
		}
		if override.RecoveryRetentionDays != nil && (*override.RecoveryRetentionDays < 1 || *override.RecoveryRetentionDays > 7) {
			issues = append(issues, issue("recovery_retention_invalid", base+".recoveryRetentionDays", "override recovery retention must be between one and seven days"))
		}
		if workspaceOverrideEmpty(override) {
			issues = append(issues, issue("override_empty", base, "an override must contain at least one concrete value"))
		}
		validateLimits(base, effectiveWorkspaceLimits(value.Defaults.Limits, override), &issues)
		validateRunBudget(base, effectiveWorkspaceBudget(value.Defaults.RunBudget, override), &issues)
	}
	validateReferences("$.skills", "skills/", "/SKILL.md", value.Skills, &issues)
	validateReferences("$.templates", "templates/", ".md", value.Templates, &issues)
	return sortedValidationError(issues)
}

// Parse strictly decodes, schema-checks, and semantically validates one
// paseo-director.json document. Unknown fields, duplicate keys, trailing
// values, invalid Unicode, non-integer numeric spellings, raw or canonical
// oversize input, unsupported versions, and invalid cross-field relationships
// all fail closed.
func Parse(input []byte) (Document, error) {
	if len(input) == 0 {
		return Document{}, invalidDocument("document_empty", "configuration document is required")
	}
	if len(input) > MaximumDocumentBytes {
		return Document{}, invalidDocument("document_too_large", "configuration document exceeds the 1 MiB limit")
	}
	canonical, err := jsondocument.CanonicalLimit(input, MaximumCanonicalDocumentBytes)
	if err != nil {
		if errors.Is(err, jsondocument.ErrCanonicalDocumentTooLarge) {
			return Document{}, invalidDocument("canonical_document_too_large", "canonical configuration exceeds the 1 MiB activation limit")
		}
		if errors.Is(err, jsondocument.ErrInvalidUTF8) || errors.Is(err, jsondocument.ErrInvalidUnicodeSurrogate) {
			return Document{}, invalidDocument("json_encoding_invalid", "configuration must contain exact valid UTF-8 and paired Unicode escapes")
		}
		return Document{}, invalidDocument("json_invalid", "configuration must be one complete JSON value with unique object keys and integer numbers")
	}
	if len(canonical) > MaximumCanonicalDocumentBytes {
		return Document{}, invalidDocument("canonical_document_too_large", "canonical configuration exceeds the 1 MiB activation limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var value Configuration
	if err := decoder.Decode(&value); err != nil {
		return Document{}, invalidDocument("schema_mismatch", "configuration does not match the closed version 1 schema")
	}
	var presence struct {
		Defaults struct {
			AutoFixCIFailures             *bool `json:"autoFixCiFailures"`
			AutoFixReviewFeedback         *bool `json:"autoFixReviewFeedback"`
			RequireDifferentReviewerModel *bool `json:"requireDifferentReviewerModel"`
			Limits                        struct {
				MaxSubagentsPerTask *int `json:"maxSubagentsPerTask"`
			} `json:"limits"`
		} `json:"defaults"`
		WorkspaceOverrides []map[string]json.RawMessage `json:"workspaceOverrides"`
	}
	if err := json.Unmarshal(canonical, &presence); err != nil ||
		presence.Defaults.Limits.MaxSubagentsPerTask == nil ||
		presence.Defaults.AutoFixCIFailures == nil ||
		presence.Defaults.AutoFixReviewFeedback == nil {
		return Document{}, invalidDocument("schema_mismatch", "configuration does not match the closed version 1 schema")
	}
	var cleanupPresence struct {
		Defaults map[string]json.RawMessage `json:"defaults"`
	}
	if err := json.Unmarshal(canonical, &cleanupPresence); err != nil {
		return Document{}, invalidDocument("schema_mismatch", "configuration does not match the closed version 1 schema")
	}
	for _, field := range []string{"terminateOnCompletion", "cancellationCleanup", "deleteRemoteTaskBranch", "recoveryRetentionDays"} {
		if raw, exists := cleanupPresence.Defaults[field]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Document{}, invalidDocument("schema_mismatch", "configuration does not match the closed version 1 schema")
		}
	}
	if _, exists := cleanupPresence.Defaults["recoveryRetentionDays"]; exists && value.Defaults.RecoveryRetentionDays < 1 {
		return Document{}, invalidDocument("schema_mismatch", "configuration does not match the closed version 1 schema")
	}
	optionalOverrideFields := []string{
		"maxActiveTasks", "maxActiveTasksPerWorkspace", "maxConcurrentAgents", "maxSubagentsPerTask",
		"elapsedSeconds", "tokens", "turns", "ciCycles", "costMicrousd", "autoFixCiFailures", "autoFixReviewFeedback", "requireDifferentReviewerModel",
		"publishBeforeReview", "terminateOnCompletion", "cancellationCleanup", "deleteRemoteTaskBranch", "recoveryRetentionDays",
	}
	for _, override := range presence.WorkspaceOverrides {
		for _, field := range optionalOverrideFields {
			if raw, exists := override[field]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return Document{}, invalidDocument("schema_mismatch", "configuration does not match the closed version 1 schema")
			}
		}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Document{}, invalidDocument("json_invalid", "configuration must contain exactly one JSON value")
	}
	if err := validate(value); err != nil {
		return Document{}, err
	}
	if safedata.ClassifyJSON(canonical, safedata.ScanRules{
		MaximumBytes: MaximumCanonicalDocumentBytes, RejectSensitiveKeys: true,
	}) == safedata.Secret {
		return Document{}, invalidDocument("secret_value_unsupported", "configuration cannot contain credential material")
	}
	digest := sha256.Sum256(canonical)
	return Document{
		configuration: cloneConfiguration(value),
		canonical:     slices.Clone(canonical),
		sha256:        hex.EncodeToString(digest[:]),
	}, nil
}
