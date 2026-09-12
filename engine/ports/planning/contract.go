// SPDX-License-Identifier: Apache-2.0

// Package planning owns the transport-neutral M2 planning presentation contract.
package planning

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/mcuadros/director-engine/domain/jsondocument"
)

//go:embed planning-surface.v1.json
var embeddedSchema []byte

// Definition is the generator-facing metadata in the engine-owned contract.
type Definition struct {
	SchemaVersion          int                        `json:"schemaVersion"`
	ContractVersion        string                     `json:"contractVersion"`
	QueryNames             []string                   `json:"queryNames"`
	MutationName           string                     `json:"mutationName"`
	MutationPath           string                     `json:"mutationPath"`
	OrganizerMutationPath  string                     `json:"organizerMutationPath"`
	DoctorQueryPath        string                     `json:"doctorQueryPath"`
	OperationsQueryPath    string                     `json:"operationsQueryPath"`
	OperationsMutationPath string                     `json:"operationsMutationPath"`
	RepairMutationPath     string                     `json:"repairMutationPath"`
	MutationActorHeaders   MutationActorHeaders       `json:"mutationActorHeaders"`
	QueryPath              string                     `json:"queryPath"`
	TaskDetailQueryPath    string                     `json:"taskDetailQueryPath"`
	HomeQueryPath          string                     `json:"homeQueryPath"`
	MaximumRequestBytes    int                        `json:"maximumRequestBytes"`
	MaximumResponseBytes   int                        `json:"maximumResponseBytes"`
	MaximumPageSize        int                        `json:"maximumPageSize"`
	MaximumProjects        int                        `json:"maximumProjects"`
	MaximumWorkspaces      int                        `json:"maximumWorkspaces"`
	MaximumEpics           int                        `json:"maximumEpics"`
	MaximumHomePageSize    int                        `json:"maximumHomePageSize"`
	HomeHealthStates       []string                   `json:"homeHealthStates"`
	HomeActionKinds        []string                   `json:"homeActionKinds"`
	DerivedStates          []string                   `json:"derivedStates"`
	Priorities             []string                   `json:"priorities"`
	StableSorts            []string                   `json:"stableSorts"`
	AttentionCodes         []string                   `json:"attentionCodes"`
	AllowedActions         []string                   `json:"allowedActions"`
	ConfigurationKeys      []string                   `json:"configurationKeys"`
	Definitions            map[string]json.RawMessage `json:"$defs"`
}

type MutationActorHeaders struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Session string `json:"session"`
}

var requiredDefinitions = []string{
	"allowedAction",
	"attentionCode",
	"budgetCountSummary",
	"budgetDimensionSummary",
	"capacityFacts",
	"configurationEntry",
	"configurationApplyAction",
	"configurationPreview",
	"epicSummary",
	"explanation",
	"planningMutationInput",
	"planningMutationResult",
	"planningPage",
	"pageCursor",
	"planningQueryInput",
	"planningSnapshot",
	"homeAction",
	"homeHost",
	"homePage",
	"homeProject",
	"homeQueryInput",
	"homeSnapshot",
	"homeTotals",
	"doctorCheck",
	"doctorQueryInput",
	"doctorRepairAvailability",
	"doctorReport",
	"operationsQueryInput",
	"operationsReport",
	"operationsControlAvailability",
	"operationsMutationInput",
	"operationsMutationResult",
	"repairInput",
	"repairOperation",
	"repairPreview",
	"repairResult",
	"organizerBootstrapInput",
	"organizerBootstrapPreview",
	"organizerBootstrapResult",
	"projectSummary",
	"runtimeBudgetSummary",
	"schedulerFacts",
	"taskDetail",
	"taskDetailBinding",
	"taskDetailQueryInput",
	"taskDetailSnapshot",
	"taskSummary",
	"workspaceSummary",
}

// CanonicalJSON emits the duplicate-safe canonical representation used for hashing.
func CanonicalJSON(schema []byte) ([]byte, error) {
	canonical, err := jsondocument.Canonical(schema)
	if err != nil {
		return nil, fmt.Errorf("canonicalize planning contract: %w", err)
	}
	return canonical, nil
}

// CanonicalSHA256 returns the lowercase canonical schema hash.
func CanonicalSHA256(schema []byte) (string, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func validateUnique(name string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("planning %s cannot be empty", name)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("planning %s contains an empty value", name)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("planning %s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateClosedObjects(value any, path string) error {
	switch current := value.(type) {
	case []any:
		for index, child := range current {
			if err := validateClosedObjects(child, fmt.Sprintf("%s/%d", path, index)); err != nil {
				return err
			}
		}
	case map[string]any:
		if kind, ok := current["type"].(string); ok && kind == "object" {
			closed, exists := current["additionalProperties"].(bool)
			if !exists || closed {
				return fmt.Errorf("planning object %s must set additionalProperties=false", path)
			}
		}
		for key, child := range current {
			if err := validateClosedObjects(child, path+"/"+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func definitionEnum(definitions map[string]json.RawMessage, name string) ([]string, error) {
	raw, ok := definitions[name]
	if !ok {
		return nil, fmt.Errorf("planning definition %q is required", name)
	}
	var value struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode planning definition %q: %w", name, err)
	}
	return value.Enum, nil
}

// ParseDefinition validates the closed schema metadata consumed by generators.
func ParseDefinition(schema []byte) (Definition, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return Definition{}, err
	}
	var raw any
	if err := json.Unmarshal(canonical, &raw); err != nil {
		return Definition{}, fmt.Errorf("decode planning contract: %w", err)
	}
	if err := validateClosedObjects(raw, "$"); err != nil {
		return Definition{}, err
	}
	var definition Definition
	if err := json.Unmarshal(canonical, &definition); err != nil {
		return Definition{}, fmt.Errorf("decode planning definition: %w", err)
	}
	if definition.SchemaVersion != 1 {
		return Definition{}, fmt.Errorf("unsupported planning schema version %d", definition.SchemaVersion)
	}
	if definition.ContractVersion != "director-planning/v1" {
		return Definition{}, errors.New("planning contract version does not match")
	}
	if !slices.Equal(definition.QueryNames, []string{"planning.query", "planning.task-detail", "planning.home", "planning.doctor", "planning.operations"}) || definition.MutationName != "planning.mutate" {
		return Definition{}, errors.New("planning operation names do not match")
	}
	if definition.MutationPath != MutationPath {
		return Definition{}, errors.New("planning mutation path does not match")
	}
	if definition.OrganizerMutationPath != OrganizerMutationPath {
		return Definition{}, errors.New("Organizer bootstrap mutation path does not match")
	}
	if definition.DoctorQueryPath != DoctorQueryPath || definition.OperationsQueryPath != OperationsQueryPath ||
		definition.OperationsMutationPath != OperationsMutationPath || definition.RepairMutationPath != RepairMutationPath {
		return Definition{}, errors.New("Doctor/Operations/Repair operation paths do not match")
	}
	if definition.MutationActorHeaders != (MutationActorHeaders{
		Kind: "x-director-actor-kind", ID: "x-director-actor-id", Session: "x-director-actor-session",
	}) {
		return Definition{}, errors.New("planning mutation actor headers do not match")
	}
	if definition.QueryPath != QueryPath || definition.TaskDetailQueryPath != TaskDetailQueryPath || definition.HomeQueryPath != HomeQueryPath || definition.MaximumRequestBytes != MaximumRequestBytes ||
		definition.MaximumResponseBytes != MaximumResponseBytes ||
		definition.MaximumPageSize != MaximumPageSize || definition.MaximumProjects != MaximumProjects ||
		definition.MaximumWorkspaces != MaximumWorkspaces || definition.MaximumEpics != MaximumEpics ||
		definition.MaximumHomePageSize != MaximumHomePageSize {
		return Definition{}, errors.New("planning bounds do not match")
	}
	for name, values := range map[string][]string{
		"derived states":     definition.DerivedStates,
		"priorities":         definition.Priorities,
		"stable sorts":       definition.StableSorts,
		"attention codes":    definition.AttentionCodes,
		"allowed actions":    definition.AllowedActions,
		"home health states": definition.HomeHealthStates,
		"home action kinds":  definition.HomeActionKinds,
		"configuration keys": definition.ConfigurationKeys,
	} {
		if err := validateUnique(name, values); err != nil {
			return Definition{}, err
		}
	}
	for _, name := range requiredDefinitions {
		if _, ok := definition.Definitions[name]; !ok {
			return Definition{}, fmt.Errorf("planning definition %q is required", name)
		}
	}
	for definitionName, expected := range map[string][]string{
		"derivedState":      definition.DerivedStates,
		"priority":          definition.Priorities,
		"stableSort":        definition.StableSorts,
		"attentionCode":     definition.AttentionCodes,
		"allowedActionKind": definition.AllowedActions,
		"homeHealthState":   definition.HomeHealthStates,
		"homeActionKind":    definition.HomeActionKinds,
	} {
		actual, err := definitionEnum(definition.Definitions, definitionName)
		if err != nil {
			return Definition{}, err
		}
		if !slices.Equal(actual, expected) {
			return Definition{}, fmt.Errorf("planning %s enum does not match metadata", definitionName)
		}
	}
	return definition, nil
}

// EmbeddedDefinition returns an isolated copy of the embedded metadata.
func EmbeddedDefinition() (Definition, error) {
	definition, err := ParseDefinition(embeddedSchema)
	if err != nil {
		return Definition{}, err
	}
	definition.QueryNames = slices.Clone(definition.QueryNames)
	definition.DerivedStates = slices.Clone(definition.DerivedStates)
	definition.Priorities = slices.Clone(definition.Priorities)
	definition.StableSorts = slices.Clone(definition.StableSorts)
	definition.AttentionCodes = slices.Clone(definition.AttentionCodes)
	definition.AllowedActions = slices.Clone(definition.AllowedActions)
	definition.HomeHealthStates = slices.Clone(definition.HomeHealthStates)
	definition.HomeActionKinds = slices.Clone(definition.HomeActionKinds)
	definition.ConfigurationKeys = slices.Clone(definition.ConfigurationKeys)
	definition.Definitions = nil
	return definition, nil
}

// SchemaSHA256 returns the canonical hash of the embedded planning schema.
func SchemaSHA256() (string, error) {
	return CanonicalSHA256(embeddedSchema)
}
