// SPDX-License-Identifier: Apache-2.0

// Package agentprofile validates normalized provider discovery facts and
// freezes the exact Organizer, Worker, and Reviewer selections consumed by a
// Run. It contains policy; host connectors only report DiscoverySnapshot
// facts and never choose a provider or fallback.
package agentprofile

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/jsondocument"
)

const (
	DiscoverySchemaVersion = "director.provider-discovery/v1"
	FrozenSchemaVersion    = "director.effective-agent-profiles/v1"
	SupportedPaseoVersion  = "0.7.2"
	MaximumDocumentBytes   = 1 << 20
)

//go:embed schemas/provider-discovery.v1.json
var discoverySchema []byte

//go:embed schemas/effective-agent-profiles.v1.json
var frozenSchema []byte

var (
	gitObjectPattern = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)
	sha256Pattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	tokenPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	admittedCLI      = map[domainconfig.Provider]string{
		domainconfig.ProviderCodex:      "0.147.0",
		domainconfig.ProviderClaudeCode: "2.1.258",
		domainconfig.ProviderOpenCode:   "1.18.18",
	}
)

// Role is the closed profile role vocabulary. Organizer identifies an
// optional planning context, not a standing agent or lifecycle authority.
type Role string

const (
	RoleOrganizer Role = "organizer"
	RoleWorker    Role = "worker"
	RoleReviewer  Role = "reviewer"
)

// ProviderState is a normalized availability fact. Unsupported tuples remain
// representable so the engine can explain their exclusion deterministically.
type ProviderState string

const (
	ProviderReady       ProviderState = "ready"
	ProviderUnavailable ProviderState = "unavailable"
)

// DiagnosticCode is the complete redacted diagnostic vocabulary accepted
// from a provider adapter. Raw stderr, credential material, and provider error
// messages have no field in this contract.
type DiagnosticCode string

const (
	DiagnosticAuthenticationRequired DiagnosticCode = "authentication_required"
	DiagnosticServiceUnavailable     DiagnosticCode = "service_unavailable"
	DiagnosticCapabilityLoss         DiagnosticCode = "capability_loss"
	DiagnosticVersionMismatch        DiagnosticCode = "version_mismatch"
)

// VariantFact is one exact discovered combination. Director never computes a
// Cartesian product from independently discovered model, effort, mode,
// permission, option, or MCP lists.
type VariantFact struct {
	Effort             string                        `json:"effort"`
	Mode               string                        `json:"mode"`
	PermissionMode     string                        `json:"permissionMode"`
	ProviderOptions    []domainconfig.ProviderOption `json:"providerOptions"`
	MCPCapabilities    []domainconfig.MCPCapability  `json:"mcpCapabilities"`
	SessionStdioMCP    bool                          `json:"sessionStdioMcp"`
	ExactMCPToolPolicy bool                          `json:"exactMcpToolPolicy"`
	RuntimeProbePassed bool                          `json:"runtimeProbePassed"`
}

// ModelFact groups exact variants under one discovered model ID.
type ModelFact struct {
	Model    string        `json:"model"`
	Variants []VariantFact `json:"variants"`
}

// ProviderFact is a normalized public-host observation. DiagnosticCodes are
// bounded classifications only; provider output is deliberately absent.
type ProviderFact struct {
	Provider        domainconfig.Provider `json:"provider"`
	CLIVersion      string                `json:"cliVersion"`
	State           ProviderState         `json:"state"`
	DiagnosticCodes []DiagnosticCode      `json:"diagnosticCodes"`
	Models          []ModelFact           `json:"models"`
}

// DiscoverySnapshot is the immutable fact supplied by a provider adapter.
type DiscoverySnapshot struct {
	SchemaVersion    string         `json:"schemaVersion"`
	Revision         string         `json:"revision"`
	PaseoVersion     string         `json:"paseoVersion"`
	ObservedAtMillis int64          `json:"observedAtMillis"`
	MaximumAgeMillis int64          `json:"maximumAgeMillis"`
	Providers        []ProviderFact `json:"providers"`
}

// ResolutionCode is a stable explanation intended for UI/Doctor output.
type ResolutionCode string

const (
	CodeDiscoveryInvalid           ResolutionCode = "discovery_invalid"
	CodeOrganizerRevisionStale     ResolutionCode = "organizer_revision_stale"
	CodeDiscoveryRevisionStale     ResolutionCode = "discovery_revision_stale"
	CodeDiscoveryFactsStale        ResolutionCode = "discovery_facts_stale"
	CodeProviderNotDiscovered      ResolutionCode = "provider_not_discovered"
	CodeProviderTupleUnsupported   ResolutionCode = "provider_tuple_unsupported"
	CodeProviderUnavailable        ResolutionCode = "provider_unavailable"
	CodeModelUnsupported           ResolutionCode = "model_unsupported"
	CodeCombinationUnsupported     ResolutionCode = "combination_unsupported"
	CodeProviderOptionsUnsupported ResolutionCode = "provider_options_unsupported"
	CodeMCPCapabilityUnsupported   ResolutionCode = "mcp_capability_unsupported"
	CodeMCPProofUnavailable        ResolutionCode = "mcp_proof_unavailable"
)

// Explanation identifies one rejected configured selection without retaining
// raw provider diagnostics or credentials.
type Explanation struct {
	Code           ResolutionCode        `json:"code"`
	Role           Role                  `json:"role"`
	SelectionIndex int                   `json:"selectionIndex"`
	Provider       domainconfig.Provider `json:"provider"`
	DiagnosticCode DiagnosticCode        `json:"diagnosticCode,omitempty"`
}

// ResolutionError reports every role that could not select an explicitly
// configured and currently supported row.
type ResolutionError struct {
	Explanations []Explanation
}

func (err *ResolutionError) Error() string {
	if len(err.Explanations) == 0 {
		return "agent profile resolution failed"
	}
	first := err.Explanations[0]
	return fmt.Sprintf("agent profile resolution failed: %s for %s selection %d", first.Code, first.Role, first.SelectionIndex)
}

// ResolutionExplanations returns a defensive copy for a typed resolution
// failure.
func ResolutionExplanations(err error) ([]Explanation, bool) {
	var resolution *ResolutionError
	if !errors.As(err, &resolution) {
		return nil, false
	}
	return slices.Clone(resolution.Explanations), true
}

// FrozenRole is one effective, explicitly configured selection plus bounded
// explanations for earlier configured entries skipped in fallback order.
type FrozenRole struct {
	Role                  Role                        `json:"role"`
	SelectedIndex         int                         `json:"selectedIndex"`
	ConfiguredChainSHA256 string                      `json:"configuredChainSha256"`
	Selection             domainconfig.AgentSelection `json:"selection"`
	PriorExplanations     []Explanation               `json:"priorExplanations"`
}

type frozenWire struct {
	SchemaVersion       string       `json:"schemaVersion"`
	OrganizerRevision   string       `json:"organizerRevision"`
	ConfigurationSHA256 string       `json:"configurationSha256"`
	DiscoveryRevision   string       `json:"discoveryRevision"`
	DiscoverySHA256     string       `json:"discoverySha256"`
	Roles               []FrozenRole `json:"roles"`
}

// FrozenSet keeps only canonical immutable bytes. Accessors decode copies so a
// Run cannot be changed by mutating the source configuration or discovery.
type FrozenSet struct {
	canonical []byte
	sha256    string
}

// FreezeRequest binds selection to the exact active Organizer and discovery
// revisions seen by the launch reducer.
type FreezeRequest struct {
	Profiles                  domainconfig.AgentProfiles
	OrganizerRevision         string
	ExpectedOrganizerRevision string
	ConfigurationSHA256       string
	Discovery                 DiscoverySnapshot
	ExpectedDiscoveryRevision string
	NowMillis                 int64
}

func schemaHash(schema []byte) (string, error) {
	canonical, err := jsondocument.Canonical(schema)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// DiscoverySchema returns a defensive copy of the closed discovery schema.
func DiscoverySchema() []byte { return slices.Clone(discoverySchema) }

// FrozenSchema returns a defensive copy of the closed frozen-profile schema.
func FrozenSchema() []byte { return slices.Clone(frozenSchema) }

// DiscoverySchemaSHA256 identifies the exact closed discovery contract.
func DiscoverySchemaSHA256() (string, error) { return schemaHash(discoverySchema) }

// FrozenSchemaSHA256 identifies the exact closed Run profile contract.
func FrozenSchemaSHA256() (string, error) { return schemaHash(frozenSchema) }

// ValidateDiscoverySnapshot exposes the same closed structural and revision
// checks used by profile freezing so later engine-owned launch preflight never
// has to trust a connector assertion that provider facts are well formed.
func ValidateDiscoverySnapshot(snapshot DiscoverySnapshot) error {
	return validateDiscovery(snapshot)
}

// SupportedCLIVersion returns the exact ADR-0005 compatibility point for one
// admitted provider. Unknown providers have no implicit fallback.
func SupportedCLIVersion(provider domainconfig.Provider) (string, bool) {
	version, ok := admittedCLI[provider]
	return version, ok
}

func validToken(value string) bool { return tokenPattern.MatchString(value) }

func validDiagnostic(code DiagnosticCode) bool {
	switch code {
	case DiagnosticAuthenticationRequired, DiagnosticServiceUnavailable, DiagnosticCapabilityLoss, DiagnosticVersionMismatch:
		return true
	default:
		return false
	}
}

func validOption(option domainconfig.ProviderOption) bool {
	switch option.Name {
	case domainconfig.ProviderOptionNetworkAccess, domainconfig.ProviderOptionNativeWebSearch:
		return option.Value == "disabled" || option.Value == "enabled"
	case domainconfig.ProviderOptionReasoningSummary:
		return option.Value == "disabled" || option.Value == "concise" || option.Value == "detailed"
	default:
		return false
	}
}

func validCapability(capability domainconfig.MCPCapability) bool {
	switch capability {
	case domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit,
		domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit,
		domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit:
		return true
	default:
		return false
	}
}

func capabilityAllowed(role Role, capability domainconfig.MCPCapability) bool {
	switch role {
	case RoleOrganizer:
		return capability == domainconfig.MCPProjectRead || capability == domainconfig.MCPPlanningCommandSubmit
	case RoleWorker:
		return capability == domainconfig.MCPProjectRead || capability == domainconfig.MCPTaskRead || capability == domainconfig.MCPTaskOutcomeSubmit
	case RoleReviewer:
		return capability == domainconfig.MCPCandidateRead || capability == domainconfig.MCPReviewVerdictSubmit
	default:
		return false
	}
}

func validSelectionForRole(role Role, selection domainconfig.AgentSelection) bool {
	if admittedCLI[selection.Provider] == "" || !validToken(selection.Model) || !validToken(selection.Effort) ||
		!validToken(selection.Mode) || (selection.PermissionMode != "read-only" && selection.PermissionMode != "workspace-write") ||
		selection.ProviderOptions == nil || len(selection.ProviderOptions) > 16 ||
		selection.MCPCapabilities == nil || len(selection.MCPCapabilities) == 0 || len(selection.MCPCapabilities) > 16 {
		return false
	}
	if (role == RoleOrganizer || role == RoleReviewer) && selection.PermissionMode != "read-only" {
		return false
	}
	optionNames := make(map[domainconfig.ProviderOptionName]struct{}, len(selection.ProviderOptions))
	for _, option := range selection.ProviderOptions {
		if !validOption(option) {
			return false
		}
		if _, duplicate := optionNames[option.Name]; duplicate {
			return false
		}
		optionNames[option.Name] = struct{}{}
	}
	capabilities := make(map[domainconfig.MCPCapability]struct{}, len(selection.MCPCapabilities))
	for _, capability := range selection.MCPCapabilities {
		if !validCapability(capability) || !capabilityAllowed(role, capability) {
			return false
		}
		if _, duplicate := capabilities[capability]; duplicate {
			return false
		}
		capabilities[capability] = struct{}{}
	}
	required := map[Role][]domainconfig.MCPCapability{
		RoleOrganizer: {domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit},
		RoleWorker:    {domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit},
		RoleReviewer:  {domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit},
	}
	for _, capability := range required[role] {
		if _, present := capabilities[capability]; !present {
			return false
		}
	}
	return true
}

func validResolutionCode(code ResolutionCode) bool {
	switch code {
	case CodeProviderNotDiscovered, CodeProviderTupleUnsupported, CodeProviderUnavailable,
		CodeModelUnsupported, CodeCombinationUnsupported, CodeProviderOptionsUnsupported,
		CodeMCPCapabilityUnsupported, CodeMCPProofUnavailable:
		return true
	default:
		return false
	}
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

func capabilitiesKey(capabilities []domainconfig.MCPCapability) string {
	cloned := slices.Clone(capabilities)
	sort.Slice(cloned, func(left, right int) bool { return cloned[left] < cloned[right] })
	encoded, _ := json.Marshal(cloned)
	return string(encoded)
}

func variantKey(variant VariantFact) string {
	return strings.Join([]string{
		variant.Effort, variant.Mode, variant.PermissionMode,
		optionsKey(variant.ProviderOptions), capabilitiesKey(variant.MCPCapabilities),
		fmt.Sprint(variant.SessionStdioMCP, variant.ExactMCPToolPolicy, variant.RuntimeProbePassed),
	}, "\x1f")
}

type discoveryRevisionPayload struct {
	SchemaVersion    string         `json:"schemaVersion"`
	PaseoVersion     string         `json:"paseoVersion"`
	MaximumAgeMillis int64          `json:"maximumAgeMillis"`
	Providers        []ProviderFact `json:"providers"`
}

func computedDiscoveryRevision(snapshot DiscoverySnapshot) string {
	snapshot = normalizeDiscovery(snapshot)
	encoded, _ := json.Marshal(discoveryRevisionPayload{
		SchemaVersion: snapshot.SchemaVersion, PaseoVersion: snapshot.PaseoVersion,
		MaximumAgeMillis: snapshot.MaximumAgeMillis, Providers: snapshot.Providers,
	})
	canonical, _ := jsondocument.Canonical(encoded)
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func validateDiscoveryStructure(snapshot DiscoverySnapshot) error {
	if snapshot.SchemaVersion != DiscoverySchemaVersion || !sha256Pattern.MatchString(snapshot.Revision) ||
		!validToken(snapshot.PaseoVersion) || snapshot.ObservedAtMillis < 0 ||
		snapshot.MaximumAgeMillis < 1 || snapshot.MaximumAgeMillis > 60_000 ||
		snapshot.Providers == nil || len(snapshot.Providers) > 16 {
		return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid}}}
	}
	providers := make(map[domainconfig.Provider]struct{}, len(snapshot.Providers))
	for _, provider := range snapshot.Providers {
		if !validToken(string(provider.Provider)) || !validToken(provider.CLIVersion) ||
			(provider.State != ProviderReady && provider.State != ProviderUnavailable) ||
			provider.DiagnosticCodes == nil || len(provider.DiagnosticCodes) > 4 || provider.Models == nil || len(provider.Models) > 256 {
			return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
		}
		if _, duplicate := providers[provider.Provider]; duplicate {
			return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
		}
		providers[provider.Provider] = struct{}{}
		diagnostics := make(map[DiagnosticCode]struct{}, len(provider.DiagnosticCodes))
		for _, code := range provider.DiagnosticCodes {
			if !validDiagnostic(code) {
				return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
			}
			if _, duplicate := diagnostics[code]; duplicate {
				return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
			}
			diagnostics[code] = struct{}{}
		}
		models := make(map[string]struct{}, len(provider.Models))
		for _, model := range provider.Models {
			if !validToken(model.Model) || model.Variants == nil || len(model.Variants) == 0 || len(model.Variants) > 128 {
				return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
			}
			if _, duplicate := models[model.Model]; duplicate {
				return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
			}
			models[model.Model] = struct{}{}
			variants := make(map[string]struct{}, len(model.Variants))
			for _, variant := range model.Variants {
				if !validToken(variant.Effort) || !validToken(variant.Mode) ||
					(variant.PermissionMode != "read-only" && variant.PermissionMode != "workspace-write") ||
					variant.ProviderOptions == nil || len(variant.ProviderOptions) > 16 ||
					variant.MCPCapabilities == nil || len(variant.MCPCapabilities) > 16 {
					return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
				}
				optionNames := make(map[domainconfig.ProviderOptionName]struct{}, len(variant.ProviderOptions))
				for _, option := range variant.ProviderOptions {
					if !validOption(option) {
						return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
					}
					if _, duplicate := optionNames[option.Name]; duplicate {
						return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
					}
					optionNames[option.Name] = struct{}{}
				}
				capabilities := make(map[domainconfig.MCPCapability]struct{}, len(variant.MCPCapabilities))
				for _, capability := range variant.MCPCapabilities {
					if !validCapability(capability) {
						return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
					}
					if _, duplicate := capabilities[capability]; duplicate {
						return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
					}
					capabilities[capability] = struct{}{}
				}
				key := variantKey(variant)
				if _, duplicate := variants[key]; duplicate {
					return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid, Provider: provider.Provider}}}
				}
				variants[key] = struct{}{}
			}
		}
	}
	return nil
}

func validateDiscovery(snapshot DiscoverySnapshot) error {
	if err := validateDiscoveryStructure(snapshot); err != nil {
		return err
	}
	if snapshot.Revision != computedDiscoveryRevision(snapshot) {
		return &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid}}}
	}
	return nil
}

// SealDiscovery derives the immutable revision from normalized non-temporal
// capability facts. Adapters may use it before persistence; the engine still
// revalidates the revision on every read.
func SealDiscovery(snapshot DiscoverySnapshot) (DiscoverySnapshot, error) {
	snapshot = normalizeDiscovery(snapshot)
	snapshot.Revision = strings.Repeat("0", 64)
	if err := validateDiscoveryStructure(snapshot); err != nil {
		return DiscoverySnapshot{}, err
	}
	snapshot.Revision = computedDiscoveryRevision(snapshot)
	return snapshot, nil
}

func canonicalDiscovery(snapshot DiscoverySnapshot) ([]byte, string, error) {
	snapshot = normalizeDiscovery(snapshot)
	if err := validateDiscovery(snapshot); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	canonical, err := jsondocument.Canonical(encoded)
	if err != nil || len(canonical) > MaximumDocumentBytes {
		return nil, "", &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid}}}
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}

// ParseDiscovery strictly rejects unknown fields, duplicate keys, invalid
// encodings, and malformed normalized facts.
func ParseDiscovery(input []byte) (DiscoverySnapshot, error) {
	if len(input) == 0 || len(input) > MaximumDocumentBytes {
		return DiscoverySnapshot{}, &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid}}}
	}
	canonical, err := jsondocument.Canonical(input)
	if err != nil {
		return DiscoverySnapshot{}, &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid}}}
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var snapshot DiscoverySnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return DiscoverySnapshot{}, &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid}}}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return DiscoverySnapshot{}, &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryInvalid}}}
	}
	if err := validateDiscovery(snapshot); err != nil {
		return DiscoverySnapshot{}, err
	}
	return normalizeDiscovery(snapshot), nil
}

func cloneSelection(selection domainconfig.AgentSelection) domainconfig.AgentSelection {
	selection.ProviderOptions = slices.Clone(selection.ProviderOptions)
	selection.MCPCapabilities = slices.Clone(selection.MCPCapabilities)
	return selection
}

func cloneDiscovery(snapshot DiscoverySnapshot) DiscoverySnapshot {
	snapshot.Providers = slices.Clone(snapshot.Providers)
	for providerIndex := range snapshot.Providers {
		provider := &snapshot.Providers[providerIndex]
		provider.DiagnosticCodes = slices.Clone(provider.DiagnosticCodes)
		provider.Models = slices.Clone(provider.Models)
		for modelIndex := range provider.Models {
			model := &provider.Models[modelIndex]
			model.Variants = slices.Clone(model.Variants)
			for variantIndex := range model.Variants {
				variant := &model.Variants[variantIndex]
				variant.ProviderOptions = slices.Clone(variant.ProviderOptions)
				variant.MCPCapabilities = slices.Clone(variant.MCPCapabilities)
			}
		}
	}
	return snapshot
}

func normalizeDiscovery(snapshot DiscoverySnapshot) DiscoverySnapshot {
	snapshot = cloneDiscovery(snapshot)
	sort.Slice(snapshot.Providers, func(left, right int) bool {
		return snapshot.Providers[left].Provider < snapshot.Providers[right].Provider
	})
	for providerIndex := range snapshot.Providers {
		provider := &snapshot.Providers[providerIndex]
		sort.Slice(provider.DiagnosticCodes, func(left, right int) bool {
			return provider.DiagnosticCodes[left] < provider.DiagnosticCodes[right]
		})
		sort.Slice(provider.Models, func(left, right int) bool { return provider.Models[left].Model < provider.Models[right].Model })
		for modelIndex := range provider.Models {
			model := &provider.Models[modelIndex]
			for variantIndex := range model.Variants {
				variant := &model.Variants[variantIndex]
				sort.Slice(variant.ProviderOptions, func(left, right int) bool {
					if variant.ProviderOptions[left].Name != variant.ProviderOptions[right].Name {
						return variant.ProviderOptions[left].Name < variant.ProviderOptions[right].Name
					}
					return variant.ProviderOptions[left].Value < variant.ProviderOptions[right].Value
				})
				sort.Slice(variant.MCPCapabilities, func(left, right int) bool {
					return variant.MCPCapabilities[left] < variant.MCPCapabilities[right]
				})
			}
			sort.Slice(model.Variants, func(left, right int) bool {
				return variantKey(model.Variants[left]) < variantKey(model.Variants[right])
			})
		}
	}
	return snapshot
}

func firstDiagnostic(provider ProviderFact) DiagnosticCode {
	if len(provider.DiagnosticCodes) == 0 {
		return ""
	}
	cloned := slices.Clone(provider.DiagnosticCodes)
	sort.Slice(cloned, func(left, right int) bool { return cloned[left] < cloned[right] })
	return cloned[0]
}

func supportsCapabilities(available, required []domainconfig.MCPCapability) bool {
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

func evaluate(role Role, index int, selection domainconfig.AgentSelection, snapshot DiscoverySnapshot) *Explanation {
	explanation := func(code ResolutionCode, diagnostic DiagnosticCode) *Explanation {
		return &Explanation{Code: code, Role: role, SelectionIndex: index, Provider: selection.Provider, DiagnosticCode: diagnostic}
	}
	var provider *ProviderFact
	for providerIndex := range snapshot.Providers {
		if snapshot.Providers[providerIndex].Provider == selection.Provider {
			provider = &snapshot.Providers[providerIndex]
			break
		}
	}
	if provider == nil {
		return explanation(CodeProviderNotDiscovered, "")
	}
	if snapshot.PaseoVersion != SupportedPaseoVersion || admittedCLI[selection.Provider] == "" ||
		provider.CLIVersion != admittedCLI[selection.Provider] {
		return explanation(CodeProviderTupleUnsupported, DiagnosticVersionMismatch)
	}
	if provider.State != ProviderReady {
		return explanation(CodeProviderUnavailable, firstDiagnostic(*provider))
	}
	var model *ModelFact
	for modelIndex := range provider.Models {
		if provider.Models[modelIndex].Model == selection.Model {
			model = &provider.Models[modelIndex]
			break
		}
	}
	if model == nil {
		return explanation(CodeModelUnsupported, "")
	}
	var combination []VariantFact
	for _, variant := range model.Variants {
		if variant.Effort == selection.Effort && variant.Mode == selection.Mode &&
			variant.PermissionMode == selection.PermissionMode {
			combination = append(combination, variant)
		}
	}
	if len(combination) == 0 {
		return explanation(CodeCombinationUnsupported, "")
	}
	var optionMatches []VariantFact
	for _, variant := range combination {
		if optionsKey(variant.ProviderOptions) == optionsKey(selection.ProviderOptions) {
			optionMatches = append(optionMatches, variant)
		}
	}
	if len(optionMatches) == 0 {
		return explanation(CodeProviderOptionsUnsupported, "")
	}
	for _, variant := range optionMatches {
		if !supportsCapabilities(variant.MCPCapabilities, selection.MCPCapabilities) {
			continue
		}
		if !variant.SessionStdioMCP || !variant.ExactMCPToolPolicy || !variant.RuntimeProbePassed {
			return explanation(CodeMCPProofUnavailable, DiagnosticCapabilityLoss)
		}
		return nil
	}
	return explanation(CodeMCPCapabilityUnsupported, DiagnosticCapabilityLoss)
}

func roleProfile(profiles domainconfig.AgentProfiles, role Role) domainconfig.AgentProfile {
	switch role {
	case RoleOrganizer:
		return profiles.Organizer
	case RoleWorker:
		return profiles.Worker
	case RoleReviewer:
		return profiles.Reviewer
	default:
		return domainconfig.AgentProfile{}
	}
}

func configuredChain(profile domainconfig.AgentProfile) []domainconfig.AgentSelection {
	result := make([]domainconfig.AgentSelection, 0, len(profile.FallbackChain)+1)
	result = append(result, cloneSelection(profile.AgentSelection))
	for _, fallback := range profile.FallbackChain {
		result = append(result, cloneSelection(fallback))
	}
	return result
}

func chainHash(chain []domainconfig.AgentSelection) string {
	encoded, _ := json.Marshal(chain)
	canonical, _ := jsondocument.Canonical(encoded)
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func resolveRole(role Role, profile domainconfig.AgentProfile, snapshot DiscoverySnapshot) (FrozenRole, []Explanation) {
	chain := configuredChain(profile)
	prior := make([]Explanation, 0, len(chain))
	for index, selection := range chain {
		if refusal := evaluate(role, index, selection, snapshot); refusal != nil {
			prior = append(prior, *refusal)
			continue
		}
		return FrozenRole{
			Role: role, SelectedIndex: index, ConfiguredChainSHA256: chainHash(chain),
			Selection: cloneSelection(selection), PriorExplanations: slices.Clone(prior),
		}, nil
	}
	return FrozenRole{}, prior
}

func canonicalFrozen(wire frozenWire) ([]byte, error) {
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	canonical, err := jsondocument.Canonical(encoded)
	if err != nil || len(canonical) > MaximumDocumentBytes {
		return nil, errors.New("frozen agent profile document is invalid")
	}
	return canonical, nil
}

// Freeze selects the first supported and available explicitly configured row
// for each role and returns immutable canonical Run input. No fallback entry is
// inferred, and a skipped primary can only reach a configured later index.
func Freeze(request FreezeRequest) (FrozenSet, error) {
	if !gitObjectPattern.MatchString(request.OrganizerRevision) || request.ExpectedOrganizerRevision != request.OrganizerRevision ||
		!sha256Pattern.MatchString(request.ConfigurationSHA256) {
		return FrozenSet{}, &ResolutionError{Explanations: []Explanation{{Code: CodeOrganizerRevisionStale}}}
	}
	if request.ExpectedDiscoveryRevision != request.Discovery.Revision {
		return FrozenSet{}, &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryRevisionStale}}}
	}
	_, discoveryHash, err := canonicalDiscovery(request.Discovery)
	if err != nil {
		return FrozenSet{}, err
	}
	if request.NowMillis < request.Discovery.ObservedAtMillis ||
		request.NowMillis-request.Discovery.ObservedAtMillis > request.Discovery.MaximumAgeMillis {
		return FrozenSet{}, &ResolutionError{Explanations: []Explanation{{Code: CodeDiscoveryFactsStale}}}
	}
	roles := []Role{RoleOrganizer, RoleWorker, RoleReviewer}
	frozenRoles := make([]FrozenRole, 0, len(roles))
	var refusals []Explanation
	for _, role := range roles {
		profile := roleProfile(request.Profiles, role)
		chain := configuredChain(profile)
		invalidProfile := false
		for index, selection := range chain {
			if !validSelectionForRole(role, selection) {
				refusals = append(refusals, Explanation{Code: CodeCombinationUnsupported, Role: role, SelectionIndex: index, Provider: selection.Provider})
				invalidProfile = true
			}
		}
		if invalidProfile {
			continue
		}
		resolved, roleRefusals := resolveRole(role, profile, request.Discovery)
		if len(roleRefusals) > 0 {
			refusals = append(refusals, roleRefusals...)
			continue
		}
		frozenRoles = append(frozenRoles, resolved)
	}
	if len(refusals) > 0 {
		return FrozenSet{}, &ResolutionError{Explanations: slices.Clone(refusals)}
	}
	wire := frozenWire{
		SchemaVersion: FrozenSchemaVersion, OrganizerRevision: request.OrganizerRevision,
		ConfigurationSHA256: request.ConfigurationSHA256, DiscoveryRevision: request.Discovery.Revision,
		DiscoverySHA256: discoveryHash, Roles: frozenRoles,
	}
	canonical, err := canonicalFrozen(wire)
	if err != nil {
		return FrozenSet{}, err
	}
	digest := sha256.Sum256(canonical)
	return FrozenSet{canonical: canonical, sha256: hex.EncodeToString(digest[:])}, nil
}

func decodeFrozen(input []byte) (frozenWire, []byte, error) {
	if len(input) == 0 || len(input) > MaximumDocumentBytes {
		return frozenWire{}, nil, errors.New("frozen agent profile document is invalid")
	}
	canonical, err := jsondocument.Canonical(input)
	if err != nil {
		return frozenWire{}, nil, errors.New("frozen agent profile document is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var wire frozenWire
	if err := decoder.Decode(&wire); err != nil {
		return frozenWire{}, nil, errors.New("frozen agent profile document is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return frozenWire{}, nil, errors.New("frozen agent profile document is invalid")
	}
	if wire.SchemaVersion != FrozenSchemaVersion || !gitObjectPattern.MatchString(wire.OrganizerRevision) ||
		!sha256Pattern.MatchString(wire.ConfigurationSHA256) || !sha256Pattern.MatchString(wire.DiscoveryRevision) ||
		!sha256Pattern.MatchString(wire.DiscoverySHA256) || len(wire.Roles) != 3 {
		return frozenWire{}, nil, errors.New("frozen agent profile document is invalid")
	}
	wantRoles := []Role{RoleOrganizer, RoleWorker, RoleReviewer}
	for index, role := range wire.Roles {
		if role.Role != wantRoles[index] || role.SelectedIndex < 0 || role.SelectedIndex > 8 || !sha256Pattern.MatchString(role.ConfiguredChainSHA256) ||
			!validSelectionForRole(role.Role, role.Selection) || len(role.PriorExplanations) != role.SelectedIndex {
			return frozenWire{}, nil, errors.New("frozen agent profile document is invalid")
		}
		for explanationIndex, explanation := range role.PriorExplanations {
			if !validResolutionCode(explanation.Code) || explanation.Role != role.Role || explanation.SelectionIndex < 0 ||
				explanation.SelectionIndex != explanationIndex || explanation.SelectionIndex >= role.SelectedIndex || admittedCLI[explanation.Provider] == "" ||
				(explanation.DiagnosticCode != "" && !validDiagnostic(explanation.DiagnosticCode)) {
				return frozenWire{}, nil, errors.New("frozen agent profile document is invalid")
			}
		}
	}
	return wire, canonical, nil
}

// ParseFrozenSet reconstructs and validates immutable Run profile bytes.
func ParseFrozenSet(input []byte) (FrozenSet, error) {
	_, canonical, err := decodeFrozen(input)
	if err != nil {
		return FrozenSet{}, err
	}
	digest := sha256.Sum256(canonical)
	return FrozenSet{canonical: canonical, sha256: hex.EncodeToString(digest[:])}, nil
}

// MarshalJSON persists the verified canonical profile document.
func (set FrozenSet) MarshalJSON() ([]byte, error) {
	if !set.Valid() {
		return nil, errors.New("frozen agent profile set is invalid")
	}
	return slices.Clone(set.canonical), nil
}

// UnmarshalJSON strictly restores a FrozenSet for TaskStore restart.
func (set *FrozenSet) UnmarshalJSON(input []byte) error {
	parsed, err := ParseFrozenSet(input)
	if err != nil {
		return err
	}
	*set = parsed
	return nil
}

// Valid proves the canonical bytes and their stored digest still agree.
func (set FrozenSet) Valid() bool {
	if set.sha256 == "" {
		return false
	}
	digest := sha256.Sum256(set.canonical)
	if hex.EncodeToString(digest[:]) != set.sha256 {
		return false
	}
	_, canonical, err := decodeFrozen(set.canonical)
	return err == nil && bytes.Equal(canonical, set.canonical)
}

// SHA256 identifies the complete immutable effective profile set.
func (set FrozenSet) SHA256() string {
	if !set.Valid() {
		return ""
	}
	return set.sha256
}

// CanonicalJSON returns defensive persisted bytes.
func (set FrozenSet) CanonicalJSON() []byte {
	if !set.Valid() {
		return nil
	}
	return slices.Clone(set.canonical)
}

// Role returns a defensive copy of one frozen role.
func (set FrozenSet) Role(role Role) (FrozenRole, bool) {
	wire, _, err := decodeFrozen(set.canonical)
	if err != nil {
		return FrozenRole{}, false
	}
	for _, current := range wire.Roles {
		if current.Role == role {
			current.Selection = cloneSelection(current.Selection)
			current.PriorExplanations = slices.Clone(current.PriorExplanations)
			return current, true
		}
	}
	return FrozenRole{}, false
}

// OrganizerRevision returns the active Organizer revision frozen into the Run.
func (set FrozenSet) OrganizerRevision() string {
	wire, _, err := decodeFrozen(set.canonical)
	if err != nil {
		return ""
	}
	return wire.OrganizerRevision
}

// ConfigurationSHA256 returns the exact active configuration hash.
func (set FrozenSet) ConfigurationSHA256() string {
	wire, _, err := decodeFrozen(set.canonical)
	if err != nil {
		return ""
	}
	return wire.ConfigurationSHA256
}

// DiscoveryRevision returns the exact normalized provider fact revision.
func (set FrozenSet) DiscoveryRevision() string {
	wire, _, err := decodeFrozen(set.canonical)
	if err != nil {
		return ""
	}
	return wire.DiscoveryRevision
}
