// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

const PrimarySessionSchemaVersion = "director.primary-session/v1"

var (
	primaryIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	primarySHA256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	primaryGitOIDPattern   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	primarySecretPattern   = regexp.MustCompile(`(?i)^--?(?:token|secret|password|credential|authorization|github|paseo|socket)(?:=|$)|(?:gh[pousr]_|sk-)[A-Za-z0-9_-]{16,}`)
)

// LeaseBinding freezes the exact Director Engine owner which admitted this
// Run. A renewed lease keeps the binding; a different holder or epoch must be
// reconciled before another lifecycle side effect can be dispatched.
type LeaseBinding struct {
	HolderInstance        string `json:"holderInstance"`
	HolderProcessIdentity string `json:"holderProcessIdentity"`
	Epoch                 uint64 `json:"epoch"`
}

// RepositoryBinding is the complete immutable source and Task-worktree
// identity. Hash-only storage is insufficient: recovery must be able to
// re-observe every named repository fact before it mutates anything.
type RepositoryBinding struct {
	RepositoryID       string `json:"repositoryId"`
	RepositoryKey      string `json:"repositoryKey"`
	CanonicalRemote    string `json:"canonicalRemote"`
	SourcePath         string `json:"sourcePath"`
	SourceDevice       uint64 `json:"sourceDevice"`
	SourceInode        uint64 `json:"sourceInode"`
	GitCommonDirectory string `json:"gitCommonDirectory"`
	GitCommonDevice    uint64 `json:"gitCommonDevice"`
	GitCommonInode     uint64 `json:"gitCommonInode"`
	WorktreePath       string `json:"worktreePath"`
	Branch             string `json:"branch"`
	BaseSHA            string `json:"baseSha"`
}

// MCPServerLaunch is a credential-free stdio server descriptor. Provider and
// connector credentials, raw control sockets, and arbitrary environment
// variables have no representation in the production primary launch record.
type MCPServerLaunch struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// PrimarySession is the immutable m3.2 Worker profile and m3.3 scoped-MCP
// reservation supplied to the zero-work bootstrap. NativeAgentID and
// BindingSHA256 are filled exactly once after Paseo reports bootstrap
// completion and before the real prompt intent is recorded.
type PrimarySession struct {
	SchemaVersion             string                        `json:"schemaVersion"`
	Scope                     Scope                         `json:"scope"`
	Role                      agentprofile.Role             `json:"role"`
	AgentIntentID             string                        `json:"agentIntentId"`
	EffectiveProfilesSHA256   string                        `json:"effectiveProfilesSha256"`
	OrganizerRevision         string                        `json:"organizerRevision"`
	ConfigurationSHA256       string                        `json:"configurationSha256"`
	ProviderDiscoveryRevision string                        `json:"providerDiscoveryRevision"`
	Provider                  domainconfig.Provider         `json:"provider"`
	Model                     string                        `json:"model"`
	Effort                    string                        `json:"effort"`
	Mode                      string                        `json:"mode"`
	PermissionMode            string                        `json:"permissionMode"`
	ProviderOptions           []domainconfig.ProviderOption `json:"providerOptions"`
	MCPContractVersion        string                        `json:"mcpContractVersion"`
	MCPContractSHA256         string                        `json:"mcpContractSha256"`
	MCPTools                  []string                      `json:"mcpTools"`
	MCPServer                 MCPServerLaunch               `json:"mcpServer"`
	ReservationSHA256         string                        `json:"reservationSha256"`
	NativeAgentID             string                        `json:"nativeAgentId,omitempty"`
	BindingSHA256             string                        `json:"bindingSha256,omitempty"`
}

func primaryDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed primary execution value: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func boundedPrimaryValue(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validScope(scope Scope) bool {
	return primaryIdentityPattern.MatchString(scope.ProjectID) &&
		primaryIdentityPattern.MatchString(scope.WorkspaceID) &&
		primaryIdentityPattern.MatchString(scope.TaskID) &&
		primaryIdentityPattern.MatchString(scope.RunID)
}

func validPrimaryBranch(value string) bool {
	if len(value) == 0 || len(value) > 255 || value == "@" || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") ||
		strings.HasSuffix(value, ".lock") || strings.Contains(value, "..") || strings.Contains(value, "@{") ||
		strings.Contains(value, "//") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return character <= ' ' || character == 0x7f || strings.ContainsRune(`~^:?*[\`, character)
	}) < 0
}

// ValidLeaseBinding rejects a missing or caller-shaped lease identity.
func ValidLeaseBinding(binding LeaseBinding) bool {
	return binding.Epoch > 0 && primaryIdentityPattern.MatchString(binding.HolderInstance) &&
		primaryIdentityPattern.MatchString(binding.HolderProcessIdentity)
}

// ValidPrimaryDigest validates a lowercase SHA-256 identity used by the
// primary lifecycle evidence records.
func ValidPrimaryDigest(value string) bool { return primarySHA256Pattern.MatchString(value) }

// RepositoryBindingSHA256 returns an identity only for a complete canonical
// binding. It performs no I/O; adapters must re-observe the stored facts.
func RepositoryBindingSHA256(binding RepositoryBinding) string {
	if !primaryIdentityPattern.MatchString(binding.RepositoryID) ||
		!boundedPrimaryValue(binding.RepositoryKey, 512) ||
		!boundedPrimaryValue(binding.CanonicalRemote, 4_096) ||
		!filepath.IsAbs(binding.SourcePath) || filepath.Clean(binding.SourcePath) != binding.SourcePath ||
		binding.SourceDevice == 0 || binding.SourceInode == 0 ||
		!filepath.IsAbs(binding.GitCommonDirectory) || filepath.Clean(binding.GitCommonDirectory) != binding.GitCommonDirectory ||
		binding.GitCommonDevice == 0 || binding.GitCommonInode == 0 ||
		!filepath.IsAbs(binding.WorktreePath) || filepath.Clean(binding.WorktreePath) != binding.WorktreePath ||
		binding.SourcePath == binding.WorktreePath || !validPrimaryBranch(binding.Branch) ||
		!primaryGitOIDPattern.MatchString(binding.BaseSHA) {
		return ""
	}
	return primaryDigest(binding)
}

func validMCPServer(server MCPServerLaunch) bool {
	if !primaryIdentityPattern.MatchString(server.Name) || len(server.Name) > 128 ||
		!filepath.IsAbs(server.Command) || filepath.Clean(server.Command) != server.Command ||
		len(server.Command) > 4_096 || len(server.Args) > 64 || len(server.Env) != 0 {
		return false
	}
	for _, argument := range server.Args {
		if !boundedPrimaryValue(argument, 256) || primarySecretPattern.MatchString(argument) {
			return false
		}
	}
	return true
}

func normalizedSession(session PrimarySession) PrimarySession {
	session.ProviderOptions = slices.Clone(session.ProviderOptions)
	sort.Slice(session.ProviderOptions, func(left, right int) bool {
		if session.ProviderOptions[left].Name != session.ProviderOptions[right].Name {
			return session.ProviderOptions[left].Name < session.ProviderOptions[right].Name
		}
		return session.ProviderOptions[left].Value < session.ProviderOptions[right].Value
	})
	session.MCPTools = slices.Clone(session.MCPTools)
	sort.Strings(session.MCPTools)
	session.MCPServer.Args = slices.Clone(session.MCPServer.Args)
	environment := session.MCPServer.Env
	session.MCPServer.Env = make(map[string]string, len(environment))
	for name, value := range environment {
		session.MCPServer.Env[name] = value
	}
	return session
}

func reservationValue(session PrimarySession) PrimarySession {
	session.NativeAgentID = ""
	session.BindingSHA256 = ""
	session.ReservationSHA256 = ""
	return session
}

func bindingValue(session PrimarySession) PrimarySession {
	session.BindingSHA256 = ""
	return session
}

// NewPrimarySession freezes exactly one Worker profile and its role-filtered
// scoped MCP catalog. It does not choose a fallback or provider setting.
func NewPrimarySession(
	scope Scope,
	agentIntentID string,
	profiles agentprofile.FrozenSet,
	server MCPServerLaunch,
) (PrimarySession, error) {
	if !validScope(scope) || !primaryIdentityPattern.MatchString(agentIntentID) ||
		!profiles.Valid() || !validMCPServer(server) {
		return PrimarySession{}, errors.New("primary session inputs are invalid")
	}
	worker, ok := profiles.Role(agentprofile.RoleWorker)
	if !ok {
		return PrimarySession{}, errors.New("frozen Worker profile is absent")
	}
	tools, err := agentbridge.Catalog(worker)
	if err != nil {
		return PrimarySession{}, err
	}
	contractHash, err := agentbridge.SchemaSHA256()
	if err != nil {
		return PrimarySession{}, err
	}
	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Name)
	}
	session := normalizedSession(PrimarySession{
		SchemaVersion: PrimarySessionSchemaVersion, Scope: scope,
		Role: agentprofile.RoleWorker, AgentIntentID: agentIntentID,
		EffectiveProfilesSHA256: profiles.SHA256(),
		OrganizerRevision:       profiles.OrganizerRevision(), ConfigurationSHA256: profiles.ConfigurationSHA256(),
		ProviderDiscoveryRevision: profiles.DiscoveryRevision(), Provider: worker.Selection.Provider,
		Model: worker.Selection.Model, Effort: worker.Selection.Effort, Mode: worker.Selection.Mode,
		PermissionMode: worker.Selection.PermissionMode, ProviderOptions: worker.Selection.ProviderOptions,
		MCPContractVersion: agentbridge.ContractVersion, MCPContractSHA256: contractHash,
		MCPTools: toolNames, MCPServer: server,
	})
	session.ReservationSHA256 = primaryDigest(reservationValue(session))
	if !ValidPrimarySession(session) {
		return PrimarySession{}, errors.New("primary session reservation is invalid")
	}
	return session, nil
}

// BindPrimarySession records the exact native Paseo identity before any real
// prompt. Rebinding, clearing, or changing the immutable reservation fails.
func BindPrimarySession(session PrimarySession, nativeAgentID string) (PrimarySession, error) {
	if !ValidPrimarySession(session) || session.NativeAgentID != "" || session.BindingSHA256 != "" ||
		!primaryIdentityPattern.MatchString(nativeAgentID) {
		return PrimarySession{}, errors.New("primary session cannot bind the native agent")
	}
	next := normalizedSession(session)
	next.NativeAgentID = nativeAgentID
	next.BindingSHA256 = primaryDigest(bindingValue(next))
	if !ValidPrimarySession(next) {
		return PrimarySession{}, errors.New("bound primary session is invalid")
	}
	return next, nil
}

// ValidPrimarySession validates both pre-create reservations and exactly-once
// post-bootstrap bindings without accepting a partial native identity.
func ValidPrimarySession(session PrimarySession) bool {
	expectedContractHash, contractErr := agentbridge.SchemaSHA256()
	if session.SchemaVersion != PrimarySessionSchemaVersion || !validScope(session.Scope) ||
		session.Role != agentprofile.RoleWorker || !primaryIdentityPattern.MatchString(session.AgentIntentID) ||
		!primarySHA256Pattern.MatchString(session.EffectiveProfilesSHA256) ||
		!primaryGitOIDPattern.MatchString(session.OrganizerRevision) ||
		!primarySHA256Pattern.MatchString(session.ConfigurationSHA256) ||
		!primarySHA256Pattern.MatchString(session.ProviderDiscoveryRevision) ||
		!boundedPrimaryValue(string(session.Provider), 128) || !boundedPrimaryValue(session.Model, 128) ||
		!boundedPrimaryValue(session.Effort, 128) || !boundedPrimaryValue(session.Mode, 128) ||
		!boundedPrimaryValue(session.PermissionMode, 128) || len(session.ProviderOptions) > 16 ||
		session.MCPContractVersion != agentbridge.ContractVersion || contractErr != nil ||
		session.MCPContractSHA256 != expectedContractHash || len(session.MCPTools) == 0 ||
		len(session.MCPTools) > 16 || !validMCPServer(session.MCPServer) {
		return false
	}
	if session.Provider != domainconfig.ProviderCodex && session.Provider != domainconfig.ProviderClaudeCode &&
		session.Provider != domainconfig.ProviderOpenCode {
		return false
	}
	if session.PermissionMode != "read-only" && session.PermissionMode != "workspace-write" {
		return false
	}
	normalized := normalizedSession(session)
	if !slices.Equal(session.ProviderOptions, normalized.ProviderOptions) || !slices.Equal(session.MCPTools, normalized.MCPTools) {
		return false
	}
	seenOptions := make(map[domainconfig.ProviderOptionName]struct{}, len(session.ProviderOptions))
	for _, option := range session.ProviderOptions {
		if (option.Name != domainconfig.ProviderOptionNetworkAccess &&
			option.Name != domainconfig.ProviderOptionNativeWebSearch &&
			option.Name != domainconfig.ProviderOptionReasoningSummary) ||
			!boundedPrimaryValue(option.Value, 128) {
			return false
		}
		if _, exists := seenOptions[option.Name]; exists {
			return false
		}
		seenOptions[option.Name] = struct{}{}
	}
	seenTools := make(map[string]struct{}, len(session.MCPTools))
	for _, tool := range session.MCPTools {
		if tool != "director_project_read" && tool != "director_task_read" &&
			tool != "director_task_outcome_submit" && tool != "director_task_helper_request" {
			return false
		}
		if _, exists := seenTools[tool]; exists {
			return false
		}
		seenTools[tool] = struct{}{}
	}
	if _, present := seenTools["director_task_read"]; !present {
		return false
	}
	if _, present := seenTools["director_task_outcome_submit"]; !present {
		return false
	}
	if session.ReservationSHA256 != primaryDigest(reservationValue(session)) {
		return false
	}
	if session.NativeAgentID == "" || session.BindingSHA256 == "" {
		return session.NativeAgentID == "" && session.BindingSHA256 == ""
	}
	return primaryIdentityPattern.MatchString(session.NativeAgentID) &&
		session.BindingSHA256 == primaryDigest(bindingValue(session))
}

// PrimarySessionMatchesProfiles re-derives the reservation from the immutable
// m3.2 profile set and m3.3 server descriptor. This prevents a self-consistent
// but different provider, permission, option, or tool catalog from being
// substituted in durable storage.
func PrimarySessionMatchesProfiles(session PrimarySession, profiles agentprofile.FrozenSet) bool {
	if !ValidPrimarySession(session) || !profiles.Valid() || session.EffectiveProfilesSHA256 != profiles.SHA256() {
		return false
	}
	expected, err := NewPrimarySession(session.Scope, session.AgentIntentID, profiles, session.MCPServer)
	return err == nil && expected.ReservationSHA256 == session.ReservationSHA256
}
