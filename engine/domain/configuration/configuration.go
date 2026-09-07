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
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	tokenPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
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

// Provider is an admitted provider-family identity. Exact provider tuples are
// reconciled outside this schema before launch.
type Provider string

const (
	ProviderCodex      Provider = "codex"
	ProviderClaudeCode Provider = "claude-code"
	ProviderOpenCode   Provider = "opencode"
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
	Remote            string `json:"remote"`
	SourcePath        string `json:"sourcePath"`
	DefaultBaseBranch string `json:"defaultBaseBranch"`
}

// AgentProfile records the provider inputs that later execution Tasks may
// reconcile. Parsing a profile does not admit or launch an agent.
type AgentProfile struct {
	Provider       Provider `json:"provider"`
	Model          string   `json:"model"`
	Effort         string   `json:"effort"`
	PermissionMode string   `json:"permissionMode"`
}

// AgentProfiles keeps Task and Reviewer profiles distinct.
type AgentProfiles struct {
	TaskAgent     AgentProfile `json:"taskAgent"`
	ReviewerAgent AgentProfile `json:"reviewerAgent"`
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
}

// Defaults is the Project-level configuration inherited by Workspaces and
// Tasks. This Task does not implement launch, delivery, or inheritance logic.
type Defaults struct {
	LaunchPolicy LaunchPolicy `json:"launchPolicy"`
	DeliveryMode DeliveryMode `json:"deliveryMode"`
	Limits       Limits       `json:"limits"`
	RunBudget    RunBudget    `json:"runBudget"`
}

// WorkspaceOverride records explicit Inherit/concrete selections. Effective
// policy reduction remains outside this configuration skeleton.
type WorkspaceOverride struct {
	WorkspaceID  string       `json:"workspaceId"`
	LaunchPolicy LaunchPolicy `json:"launchPolicy"`
	DeliveryMode DeliveryMode `json:"deliveryMode"`
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
	value.WorkspaceOverrides = slices.Clone(value.WorkspaceOverrides)
	value.Skills = slices.Clone(value.Skills)
	value.Templates = slices.Clone(value.Templates)
	return value
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

func validRemoteUsername(value string) bool {
	if value == "" {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("._-", r))
	}) < 0
}

func validRemoteHost(value string) bool {
	if value == "" || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") ||
		strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") || strings.Contains(value, "..") {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-')
	}) < 0
}

func validRemoteHostPort(value string) bool {
	if strings.HasPrefix(value, "[") {
		closing := strings.IndexByte(value, ']')
		if closing < 2 {
			return false
		}
		address := value[1:closing]
		if strings.IndexFunc(address, func(r rune) bool {
			return !((r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') ||
				(r >= '0' && r <= '9') || r == ':' || r == '.')
		}) >= 0 {
			return false
		}
		value = value[closing+1:]
		if value == "" {
			return true
		}
		if !strings.HasPrefix(value, ":") {
			return false
		}
		value = value[1:]
		return value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) < 0
	}
	if strings.Count(value, ":") > 1 {
		return false
	}
	host := value
	if colon := strings.LastIndexByte(value, ':'); colon >= 0 {
		host = value[:colon]
		port := value[colon+1:]
		if port == "" || strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return false
		}
	}
	return validRemoteHost(host)
}

func validURLRemote(value string) bool {
	separator := strings.Index(value, "://")
	if separator <= 0 {
		return false
	}
	rest := value[separator+3:]
	authority := rest
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		authority = rest[:slash]
	}
	if authority == "" || strings.ContainsAny(authority, "?#") || strings.Count(authority, "@") > 1 {
		return false
	}
	hostPort := authority
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		if !validRemoteUsername(authority[:at]) {
			return false
		}
		hostPort = authority[at+1:]
	}
	return validRemoteHostPort(hostPort) && !strings.ContainsAny(rest, "?#")
}

func validSCPLikeRemote(value string) bool {
	if strings.Contains(value, "://") || strings.Count(value, "@") > 1 {
		return false
	}
	hostStart := 0
	hasUsername := false
	if at := strings.LastIndexByte(value, '@'); at >= 0 {
		if !validRemoteUsername(value[:at]) {
			return false
		}
		hasUsername = true
		hostStart = at + 1
	}
	separatorOffset := strings.IndexByte(value[hostStart:], ':')
	if separatorOffset <= 0 {
		return false
	}
	separator := hostStart + separatorOffset
	host := value[hostStart:separator]
	remotePath := value[separator+1:]
	if hostStart == 0 {
		switch strings.ToLower(host) {
		case "ext", "file", "ftp", "git", "git+ssh", "http", "https", "rsync", "ssh":
			return false
		}
	}
	hostIsExplicit := hasUsername || strings.EqualFold(host, "localhost") || strings.ContainsRune(host, '.')
	return hostIsExplicit && validRemoteHost(host) && remotePath != "" && !strings.HasPrefix(remotePath, "-") &&
		!strings.Contains(remotePath, "::")
}

func validateRemote(value string) (string, string) {
	if len(value) == 0 || len(value) > 2048 || strings.HasPrefix(value, "-") {
		return "remote_invalid", "remote must be bounded and cannot begin with an option prefix"
	}
	decoded, ok := decodeRemoteEscapes(value)
	if !ok {
		return "remote_format_invalid", "remote contains invalid percent-encoding or Unicode"
	}
	if hasUnsafeRemoteWhitespace(decoded) {
		return "remote_whitespace_unsafe", "remote cannot contain whitespace or control characters"
	}
	if hasPasswordUserinfo(decoded) {
		return "remote_userinfo_password", "remote userinfo cannot contain a password"
	}
	if hasUnsafeRemoteCommandSyntax(decoded) {
		return "remote_command_unsafe", "remote cannot contain command-bearing syntax"
	}
	if separator := strings.Index(decoded, "://"); separator >= 0 {
		scheme := strings.ToLower(decoded[:separator])
		if scheme != "https" && scheme != "ssh" && scheme != "git" {
			return "remote_scheme_unsupported", "remote scheme must be https, ssh, or git"
		}
		if !validURLRemote(decoded) {
			return "remote_format_invalid", "remote URL structure is invalid"
		}
		return "", ""
	}
	if !validSCPLikeRemote(decoded) {
		return "remote_format_invalid", "remote must use https, ssh, git, or safe scp-like syntax"
	}
	return "", ""
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

func validateProfile(path string, profile AgentProfile, issues *[]Issue) {
	if profile.Provider != ProviderCodex && profile.Provider != ProviderClaudeCode && profile.Provider != ProviderOpenCode {
		*issues = append(*issues, issue("provider_unsupported", path+".provider", "provider must be codex, claude-code, or opencode"))
	}
	for field, value := range map[string]string{
		"model":          profile.Model,
		"effort":         profile.Effort,
		"permissionMode": profile.PermissionMode,
	} {
		if !validToken(value) {
			*issues = append(*issues, issue("token_invalid", path+"."+field, "value must be a bounded provider token"))
		}
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
	for index, workspace := range value.Workspaces {
		base := fmt.Sprintf("$.workspaces[%d]", index)
		if !validIdentifier(workspace.ID) {
			issues = append(issues, issue("identifier_invalid", base+".id", "workspace id must be a lowercase Director identifier"))
		} else if _, duplicate := workspaceIDs[workspace.ID]; duplicate {
			issues = append(issues, issue("workspace_duplicate", base+".id", "workspace id must be unique"))
		}
		workspaceIDs[workspace.ID] = struct{}{}
		if code, message := validateRemote(workspace.Remote); code != "" {
			issues = append(issues, issue(code, base+".remote", message))
		}
		if !validSourcePath(workspace.SourcePath) {
			issues = append(issues, issue("source_path_invalid", base+".sourcePath", "source path must be a clean absolute Linux path"))
		}
		if !validGitBranch(workspace.DefaultBaseBranch) {
			issues = append(issues, issue("base_branch_invalid", base+".defaultBaseBranch", "default base branch is not a safe Git branch name"))
		}
	}
	validateProfile("$.agentProfiles.taskAgent", value.AgentProfiles.TaskAgent, &issues)
	validateProfile("$.agentProfiles.reviewerAgent", value.AgentProfiles.ReviewerAgent, &issues)
	if value.Defaults.LaunchPolicy != LaunchManual && value.Defaults.LaunchPolicy != LaunchAutomatic {
		issues = append(issues, issue("launch_policy_invalid", "$.defaults.launchPolicy", "Project launch policy must be manual or automatic"))
	}
	if value.Defaults.DeliveryMode != DeliveryPullRequest && value.Defaults.DeliveryMode != DeliveryDirect {
		issues = append(issues, issue("delivery_mode_invalid", "$.defaults.deliveryMode", "Project delivery mode must be pull_request or direct"))
	}
	limits := value.Defaults.Limits
	for field, current := range map[string]int{
		"maxActiveTasks":             limits.MaxActiveTasks,
		"maxActiveTasksPerWorkspace": limits.MaxActiveTasksPerWorkspace,
		"maxConcurrentAgents":        limits.MaxConcurrentAgents,
	} {
		if current < 1 {
			issues = append(issues, issue("limit_invalid", "$.defaults.limits."+field, "limit must be a positive integer"))
		}
	}
	if limits.MaxSubagentsPerTask < 0 {
		issues = append(issues, issue("limit_invalid", "$.defaults.limits.maxSubagentsPerTask", "subagent limit cannot be negative"))
	}
	if limits.MaxActiveTasksPerWorkspace > limits.MaxActiveTasks {
		issues = append(issues, issue("limit_inconsistent", "$.defaults.limits.maxActiveTasksPerWorkspace", "per-Workspace active Task limit cannot exceed the Project limit"))
	}
	if limits.MaxActiveTasks > limits.MaxConcurrentAgents {
		issues = append(issues, issue("limit_inconsistent", "$.defaults.limits.maxConcurrentAgents", "agent capacity cannot be lower than active Task capacity"))
	}
	for field, current := range map[string]int64{
		"elapsedSeconds": value.Defaults.RunBudget.ElapsedSeconds,
		"tokens":         value.Defaults.RunBudget.Tokens,
		"turns":          value.Defaults.RunBudget.Turns,
		"ciCycles":       value.Defaults.RunBudget.CICycles,
	} {
		if current < 1 {
			issues = append(issues, issue("budget_invalid", "$.defaults.runBudget."+field, "Run budget must be a positive integer"))
		}
	}
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
		if override.LaunchPolicy == LaunchInherit && override.DeliveryMode == DeliveryInherit {
			issues = append(issues, issue("override_empty", base, "an override must contain at least one concrete value"))
		}
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
	canonical, err := jsondocument.Canonical(input)
	if err != nil {
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
			Limits struct {
				MaxSubagentsPerTask *int `json:"maxSubagentsPerTask"`
			} `json:"limits"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(canonical, &presence); err != nil || presence.Defaults.Limits.MaxSubagentsPerTask == nil {
		return Document{}, invalidDocument("schema_mismatch", "configuration does not match the closed version 1 schema")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Document{}, invalidDocument("json_invalid", "configuration must contain exactly one JSON value")
	}
	if err := validate(value); err != nil {
		return Document{}, err
	}
	digest := sha256.Sum256(canonical)
	return Document{
		configuration: cloneConfiguration(value),
		canonical:     slices.Clone(canonical),
		sha256:        hex.EncodeToString(digest[:]),
	}, nil
}
