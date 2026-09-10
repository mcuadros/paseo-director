// SPDX-License-Identifier: Apache-2.0

// Package agentbridge owns the closed, engine-defined session MCP contract.
// It contains the role/capability/tool mapping, fixed-scope binding, and exact
// provider preflight rules. Transports and Paseo connectors may only render or
// translate these values.
package agentbridge

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/jsondocument"
)

const (
	ContractVersion       = "director.agent-mcp/v1"
	SessionSchemaVersion  = "director.agent-mcp-session/v1"
	MaximumRequestBytes   = 64 * 1024
	MaximumResponseBytes  = 64 * 1024
	MaximumCommandsPerRun = 64
)

//go:embed schemas/director-agent-mcp.v1.json
var embeddedSchema []byte

var (
	identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// ToolDefinition is the exact catalog row exposed to an admitted MCP session.
type ToolDefinition struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Capability  domainconfig.MCPCapability `json:"capability"`
	Roles       []agentprofile.Role        `json:"roles"`
	Mutating    bool                       `json:"mutating"`
	InputSchema json.RawMessage            `json:"inputSchema"`
}

// Definition is the canonical generator-facing MCP contract.
type Definition struct {
	MetaSchema           string                           `json:"$schema"`
	SchemaVersion        int                              `json:"schemaVersion"`
	ContractVersion      string                           `json:"contractVersion"`
	SupportedPaseo       string                           `json:"supportedPaseoVersion"`
	MaximumRequestBytes  int                              `json:"maximumRequestBytes"`
	MaximumResponseBytes int                              `json:"maximumResponseBytes"`
	MaximumCommands      int                              `json:"maximumCommandsPerRun"`
	ProviderCLIVersions  map[domainconfig.Provider]string `json:"providerCliVersions"`
	Roles                []agentprofile.Role              `json:"roles"`
	Capabilities         []domainconfig.MCPCapability     `json:"capabilities"`
	Tools                []ToolDefinition                 `json:"tools"`
}

func closedToolSchema(value any) bool {
	switch current := value.(type) {
	case []any:
		for _, child := range current {
			if !closedToolSchema(child) {
				return false
			}
		}
	case map[string]any:
		if current["type"] == "object" {
			if _, hasProperties := current["properties"]; hasProperties {
				closed, ok := current["additionalProperties"].(bool)
				if !ok || closed {
					return false
				}
			}
		}
		for _, child := range current {
			if !closedToolSchema(child) {
				return false
			}
		}
	}
	return true
}

// SessionBinding is server-created immutable authority for one MCP process.
// The model-facing tool schemas contain none of these selectors.
type SessionBinding struct {
	SchemaVersion             string            `json:"schemaVersion"`
	Audience                  string            `json:"audience"`
	Role                      agentprofile.Role `json:"role"`
	ProjectID                 string            `json:"projectId"`
	WorkspaceID               string            `json:"workspaceId"`
	TaskID                    string            `json:"taskId"`
	RunID                     string            `json:"runId"`
	HelperID                  string            `json:"helperId,omitempty"`
	CandidateID               string            `json:"candidateId"`
	CandidateSHA              string            `json:"candidateSha"`
	NativeAgentID             string            `json:"nativeAgentId"`
	TurnID                    string            `json:"turnId"`
	ExpectedProjectVersion    uint64            `json:"expectedProjectVersion"`
	ExpectedWorkspaceVersion  uint64            `json:"expectedWorkspaceVersion"`
	ExpectedTaskVersion       uint64            `json:"expectedTaskVersion"`
	ExpectedRunVersion        uint64            `json:"expectedRunVersion"`
	ExpectedRunStateSHA256    string            `json:"expectedRunStateSha256"`
	EffectiveProfilesSHA256   string            `json:"effectiveProfilesSha256"`
	OrganizerRevision         string            `json:"organizerRevision"`
	ConfigurationSHA256       string            `json:"configurationSha256"`
	ProviderDiscoveryRevision string            `json:"providerDiscoveryRevision"`
}

// PreflightCode is a closed, redacted reason why a selected provider cannot
// receive the session MCP before launch.
type PreflightCode string

const (
	PreflightDiscoveryInvalid        PreflightCode = "PROVIDER_DISCOVERY_INVALID"
	PreflightDiscoveryStale          PreflightCode = "PROVIDER_DISCOVERY_STALE"
	PreflightRevisionMismatch        PreflightCode = "PROVIDER_DISCOVERY_REVISION_MISMATCH"
	PreflightProfileMismatch         PreflightCode = "EFFECTIVE_PROFILE_MISMATCH"
	PreflightPaseoUnsupported        PreflightCode = "PASEO_VERSION_UNSUPPORTED"
	PreflightProviderMissing         PreflightCode = "PROVIDER_NOT_DISCOVERED"
	PreflightCLIVersionUnsupported   PreflightCode = "PROVIDER_CLI_VERSION_UNSUPPORTED"
	PreflightProviderUnavailable     PreflightCode = "PROVIDER_UNAVAILABLE"
	PreflightModelUnsupported        PreflightCode = "PROVIDER_MODEL_UNSUPPORTED"
	PreflightCombinationUnsupported  PreflightCode = "PROVIDER_VARIANT_UNSUPPORTED"
	PreflightOptionsUnsupported      PreflightCode = "PROVIDER_OPTIONS_UNSUPPORTED"
	PreflightCapabilitiesUnsupported PreflightCode = "MCP_CAPABILITY_UNSUPPORTED"
	PreflightSessionMCPUnavailable   PreflightCode = "SESSION_STDIO_MCP_UNPROVEN"
	PreflightToolPolicyUnavailable   PreflightCode = "EXACT_MCP_TOOL_POLICY_UNPROVEN"
	PreflightRuntimeProbeUnavailable PreflightCode = "MCP_RUNTIME_PROBE_UNPROVEN"
)

// PreflightError carries no provider output, path, or credential material.
type PreflightError struct {
	Code     PreflightCode
	Role     agentprofile.Role
	Provider domainconfig.Provider
}

func (failure *PreflightError) Error() string {
	return fmt.Sprintf("provider preflight failed: %s", failure.Code)
}

// CanonicalJSON returns the duplicate-safe canonical contract.
func CanonicalJSON(schema []byte) ([]byte, error) {
	canonical, err := jsondocument.Canonical(schema)
	if err != nil {
		return nil, fmt.Errorf("canonicalize agent MCP contract: %w", err)
	}
	return canonical, nil
}

// CanonicalSHA256 identifies an exact contract revision.
func CanonicalSHA256(schema []byte) (string, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func uniqueStrings[T ~string](values []T) bool {
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func expectedCapabilities() []domainconfig.MCPCapability {
	return []domainconfig.MCPCapability{
		domainconfig.MCPProjectRead,
		domainconfig.MCPPlanningCommandSubmit,
		domainconfig.MCPTaskRead,
		domainconfig.MCPTaskOutcomeSubmit,
		domainconfig.MCPTaskHelperRequest,
		domainconfig.MCPHelperContributionSubmit,
		domainconfig.MCPCandidateRead,
		domainconfig.MCPReviewVerdictSubmit,
	}
}

func toolRoleAllowed(capability domainconfig.MCPCapability, role agentprofile.Role) bool {
	switch role {
	case agentprofile.RoleOrganizer:
		return capability == domainconfig.MCPProjectRead || capability == domainconfig.MCPPlanningCommandSubmit
	case agentprofile.RoleWorker:
		return capability == domainconfig.MCPProjectRead || capability == domainconfig.MCPTaskRead || capability == domainconfig.MCPTaskOutcomeSubmit ||
			capability == domainconfig.MCPTaskHelperRequest
	case agentprofile.RoleHelper:
		return capability == domainconfig.MCPTaskRead || capability == domainconfig.MCPHelperContributionSubmit
	case agentprofile.RoleReviewer:
		return capability == domainconfig.MCPCandidateRead || capability == domainconfig.MCPReviewVerdictSubmit
	default:
		return false
	}
}

type expectedTool struct {
	name     string
	roles    []agentprofile.Role
	mutating bool
}

func expectedTools() map[domainconfig.MCPCapability]expectedTool {
	return map[domainconfig.MCPCapability]expectedTool{
		domainconfig.MCPProjectRead:              {"director_project_read", []agentprofile.Role{agentprofile.RoleOrganizer, agentprofile.RoleWorker}, false},
		domainconfig.MCPPlanningCommandSubmit:    {"director_planning_command_submit", []agentprofile.Role{agentprofile.RoleOrganizer}, true},
		domainconfig.MCPTaskRead:                 {"director_task_read", []agentprofile.Role{agentprofile.RoleWorker, agentprofile.RoleHelper}, false},
		domainconfig.MCPTaskOutcomeSubmit:        {"director_task_outcome_submit", []agentprofile.Role{agentprofile.RoleWorker}, true},
		domainconfig.MCPTaskHelperRequest:        {"director_task_helper_request", []agentprofile.Role{agentprofile.RoleWorker}, true},
		domainconfig.MCPHelperContributionSubmit: {"director_helper_contribution_submit", []agentprofile.Role{agentprofile.RoleHelper}, true},
		domainconfig.MCPCandidateRead:            {"director_candidate_read", []agentprofile.Role{agentprofile.RoleReviewer}, false},
		domainconfig.MCPReviewVerdictSubmit:      {"director_review_verdict_submit", []agentprofile.Role{agentprofile.RoleReviewer}, true},
	}
}

// ParseDefinition validates the exact role/capability/tool mapping and every
// closed input schema before it can become an advertised catalog.
func ParseDefinition(schema []byte) (Definition, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return Definition{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var definition Definition
	if err := decoder.Decode(&definition); err != nil {
		return Definition{}, fmt.Errorf("decode agent MCP contract: %w", err)
	}
	if definition.SchemaVersion != 1 || definition.ContractVersion != ContractVersion ||
		definition.SupportedPaseo != agentprofile.SupportedPaseoVersion ||
		definition.MaximumRequestBytes != MaximumRequestBytes ||
		definition.MaximumResponseBytes != MaximumResponseBytes ||
		definition.MaximumCommands != MaximumCommandsPerRun ||
		!slices.Equal(definition.Roles, []agentprofile.Role{agentprofile.RoleOrganizer, agentprofile.RoleWorker, agentprofile.RoleHelper, agentprofile.RoleReviewer}) ||
		!slices.Equal(definition.Capabilities, expectedCapabilities()) || !uniqueStrings(definition.Roles) ||
		!uniqueStrings(definition.Capabilities) || len(definition.ProviderCLIVersions) != 3 || len(definition.Tools) != 8 {
		return Definition{}, errors.New("agent MCP contract metadata does not match")
	}
	for provider, version := range definition.ProviderCLIVersions {
		expected, admitted := agentprofile.SupportedCLIVersion(provider)
		if !admitted || version != expected {
			return Definition{}, errors.New("agent MCP provider compatibility does not match")
		}
	}
	seenNames := map[string]struct{}{}
	seenCapabilities := map[domainconfig.MCPCapability]struct{}{}
	expectedRows := expectedTools()
	for _, tool := range definition.Tools {
		expected, known := expectedRows[tool.Capability]
		if !identityPattern.MatchString(tool.Name) || len(tool.Description) == 0 || len(tool.Description) > 512 ||
			len(tool.Roles) == 0 || !uniqueStrings(tool.Roles) || len(tool.InputSchema) == 0 || !known ||
			tool.Name != expected.name || tool.Mutating != expected.mutating || !slices.Equal(tool.Roles, expected.roles) {
			return Definition{}, errors.New("agent MCP tool definition is invalid")
		}
		if _, duplicate := seenNames[tool.Name]; duplicate {
			return Definition{}, errors.New("agent MCP tool name is duplicated")
		}
		if _, duplicate := seenCapabilities[tool.Capability]; duplicate {
			return Definition{}, errors.New("agent MCP capability mapping is duplicated")
		}
		for _, role := range tool.Roles {
			if !toolRoleAllowed(tool.Capability, role) {
				return Definition{}, errors.New("agent MCP role capability mapping is invalid")
			}
		}
		if _, err := jsondocument.Canonical(tool.InputSchema); err != nil {
			return Definition{}, errors.New("agent MCP tool input schema is invalid")
		}
		var schemaValue any
		if json.Unmarshal(tool.InputSchema, &schemaValue) != nil || !closedToolSchema(schemaValue) {
			return Definition{}, errors.New("agent MCP tool input schema is not closed")
		}
		seenNames[tool.Name] = struct{}{}
		seenCapabilities[tool.Capability] = struct{}{}
	}
	return cloneDefinition(definition), nil
}

func cloneDefinition(definition Definition) Definition {
	definition.Roles = slices.Clone(definition.Roles)
	definition.Capabilities = slices.Clone(definition.Capabilities)
	definition.ProviderCLIVersions = make(map[domainconfig.Provider]string, len(definition.ProviderCLIVersions))
	for provider, version := range mustProviderVersions() {
		definition.ProviderCLIVersions[provider] = version
	}
	definition.Tools = slices.Clone(definition.Tools)
	for index := range definition.Tools {
		definition.Tools[index].Roles = slices.Clone(definition.Tools[index].Roles)
		definition.Tools[index].InputSchema = slices.Clone(definition.Tools[index].InputSchema)
	}
	return definition
}

func mustProviderVersions() map[domainconfig.Provider]string {
	var raw struct {
		ProviderCLIVersions map[domainconfig.Provider]string `json:"providerCliVersions"`
	}
	_ = json.Unmarshal(embeddedSchema, &raw)
	return raw.ProviderCLIVersions
}

// EmbeddedDefinition returns an isolated exact contract.
func EmbeddedDefinition() (Definition, error) { return ParseDefinition(embeddedSchema) }

// Schema returns an isolated copy of the engine-owned contract.
func Schema() []byte { return slices.Clone(embeddedSchema) }

// SchemaSHA256 returns the canonical contract digest.
func SchemaSHA256() (string, error) { return CanonicalSHA256(embeddedSchema) }

// Catalog returns exactly the tools admitted by one immutable effective role.
func Catalog(role agentprofile.FrozenRole) ([]ToolDefinition, error) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		return nil, err
	}
	if role.Role != agentprofile.RoleOrganizer && role.Role != agentprofile.RoleWorker && role.Role != agentprofile.RoleHelper && role.Role != agentprofile.RoleReviewer {
		return nil, errors.New("agent MCP role is invalid")
	}
	capabilities := make(map[domainconfig.MCPCapability]struct{}, len(role.Selection.MCPCapabilities))
	for _, capability := range role.Selection.MCPCapabilities {
		if !toolRoleAllowed(capability, role.Role) {
			return nil, errors.New("effective profile contains an invalid MCP capability")
		}
		capabilities[capability] = struct{}{}
	}
	tools := make([]ToolDefinition, 0, len(capabilities))
	for _, tool := range definition.Tools {
		if _, admitted := capabilities[tool.Capability]; admitted && slices.Contains(tool.Roles, role.Role) {
			tools = append(tools, tool)
		}
	}
	if len(tools) != len(capabilities) {
		return nil, errors.New("effective profile contains an unknown MCP capability")
	}
	return tools, nil
}

func validIdentity(value string) bool {
	return identityPattern.MatchString(value) && utf8.ValidString(value) && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

// ValidateSessionBinding verifies the fixed server-created scope and every
// immutable version/revision binding without consulting caller tool input.
func ValidateSessionBinding(binding SessionBinding) error {
	if binding.SchemaVersion != SessionSchemaVersion || !validIdentity(binding.Audience) ||
		!validIdentity(binding.ProjectID) || !validIdentity(binding.WorkspaceID) || !validIdentity(binding.TaskID) ||
		!validIdentity(binding.RunID) || !validIdentity(binding.NativeAgentID) || !validIdentity(binding.TurnID) ||
		!sha256Pattern.MatchString(binding.ExpectedRunStateSHA256) ||
		!sha256Pattern.MatchString(binding.EffectiveProfilesSHA256) ||
		!gitOIDPattern.MatchString(binding.OrganizerRevision) ||
		!sha256Pattern.MatchString(binding.ConfigurationSHA256) ||
		!sha256Pattern.MatchString(binding.ProviderDiscoveryRevision) {
		return errors.New("agent MCP session binding is invalid")
	}
	switch binding.Role {
	case agentprofile.RoleOrganizer, agentprofile.RoleWorker:
		if binding.HelperID != "" || binding.CandidateID != "" || binding.CandidateSHA != "" {
			return errors.New("agent MCP session Candidate binding is invalid")
		}
	case agentprofile.RoleHelper:
		if !validIdentity(binding.HelperID) || binding.CandidateID != "" || binding.CandidateSHA != "" {
			return errors.New("agent MCP helper binding is invalid")
		}
	case agentprofile.RoleReviewer:
		if binding.HelperID != "" || !validIdentity(binding.CandidateID) || !gitOIDPattern.MatchString(binding.CandidateSHA) {
			return errors.New("agent MCP Reviewer Candidate binding is invalid")
		}
	default:
		return errors.New("agent MCP session role is invalid")
	}
	return nil
}

// SessionDigest identifies every fixed scope, version, expected-state, role,
// profile, and provider-fact binding. It never includes a credential.
func SessionDigest(binding SessionBinding) (string, error) {
	if err := ValidateSessionBinding(binding); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	canonical, err := jsondocument.Canonical(encoded)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func optionsKey(options []domainconfig.ProviderOption) string {
	cloned := slices.Clone(options)
	sort.Slice(cloned, func(left, right int) bool {
		if cloned[left].Name != cloned[right].Name {
			return cloned[left].Name < cloned[right].Name
		}
		return cloned[left].Value < cloned[right].Value
	})
	encoded, _ := json.Marshal(cloned)
	return string(encoded)
}

func capabilitiesContain(available, required []domainconfig.MCPCapability) bool {
	set := make(map[domainconfig.MCPCapability]struct{}, len(available))
	for _, capability := range available {
		set[capability] = struct{}{}
	}
	for _, capability := range required {
		if _, present := set[capability]; !present {
			return false
		}
	}
	return true
}

func preflightFailure(code PreflightCode, role agentprofile.FrozenRole) error {
	return &PreflightError{Code: code, Role: role.Role, Provider: role.Selection.Provider}
}

// VerifyProviderPreflight rechecks the exact selected tuple against fresh,
// normalized public Paseo/provider facts. It does not select a fallback or
// alter the immutable effective role.
func VerifyProviderPreflight(role agentprofile.FrozenRole, snapshot agentprofile.DiscoverySnapshot, expectedRevision string, nowMillis int64) error {
	if err := agentprofile.ValidateDiscoverySnapshot(snapshot); err != nil {
		return preflightFailure(PreflightDiscoveryInvalid, role)
	}
	if snapshot.Revision != expectedRevision {
		return preflightFailure(PreflightRevisionMismatch, role)
	}
	if nowMillis < snapshot.ObservedAtMillis || nowMillis-snapshot.ObservedAtMillis > snapshot.MaximumAgeMillis {
		return preflightFailure(PreflightDiscoveryStale, role)
	}
	if snapshot.PaseoVersion != agentprofile.SupportedPaseoVersion {
		return preflightFailure(PreflightPaseoUnsupported, role)
	}
	expectedCLI, admitted := agentprofile.SupportedCLIVersion(role.Selection.Provider)
	if !admitted {
		return preflightFailure(PreflightProfileMismatch, role)
	}
	var provider *agentprofile.ProviderFact
	for index := range snapshot.Providers {
		if snapshot.Providers[index].Provider == role.Selection.Provider {
			provider = &snapshot.Providers[index]
			break
		}
	}
	if provider == nil {
		return preflightFailure(PreflightProviderMissing, role)
	}
	if provider.CLIVersion != expectedCLI {
		return preflightFailure(PreflightCLIVersionUnsupported, role)
	}
	if provider.State != agentprofile.ProviderReady {
		return preflightFailure(PreflightProviderUnavailable, role)
	}
	var model *agentprofile.ModelFact
	for index := range provider.Models {
		if provider.Models[index].Model == role.Selection.Model {
			model = &provider.Models[index]
			break
		}
	}
	if model == nil {
		return preflightFailure(PreflightModelUnsupported, role)
	}
	var combination []agentprofile.VariantFact
	for _, variant := range model.Variants {
		if variant.Effort == role.Selection.Effort && variant.Mode == role.Selection.Mode &&
			variant.PermissionMode == role.Selection.PermissionMode {
			combination = append(combination, variant)
		}
	}
	if len(combination) == 0 {
		return preflightFailure(PreflightCombinationUnsupported, role)
	}
	var optionMatches []agentprofile.VariantFact
	for _, variant := range combination {
		if optionsKey(variant.ProviderOptions) == optionsKey(role.Selection.ProviderOptions) {
			optionMatches = append(optionMatches, variant)
		}
	}
	if len(optionMatches) == 0 {
		return preflightFailure(PreflightOptionsUnsupported, role)
	}
	for _, variant := range optionMatches {
		if !capabilitiesContain(variant.MCPCapabilities, role.Selection.MCPCapabilities) {
			continue
		}
		if !variant.SessionStdioMCP {
			return preflightFailure(PreflightSessionMCPUnavailable, role)
		}
		if !variant.ExactMCPToolPolicy {
			return preflightFailure(PreflightToolPolicyUnavailable, role)
		}
		if !variant.RuntimeProbePassed {
			return preflightFailure(PreflightRuntimeProbeUnavailable, role)
		}
		return nil
	}
	return preflightFailure(PreflightCapabilitiesUnsupported, role)
}
