// SPDX-License-Identifier: Apache-2.0

package planning

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/safedata"
)

const MutationPath = "/v1/planning/mutate"

type MutationIntent struct {
	Type               string                  `json:"type"`
	ProjectID          string                  `json:"projectId,omitempty"`
	Name               string                  `json:"name,omitempty"`
	EpicID             string                  `json:"epicId,omitempty"`
	WorkspaceID        string                  `json:"workspaceId,omitempty"`
	TaskID             string                  `json:"taskId,omitempty"`
	RunID              string                  `json:"runId,omitempty"`
	Key                string                  `json:"key,omitempty"`
	Title              string                  `json:"title,omitempty"`
	Description        string                  `json:"description,omitempty"`
	Objective          string                  `json:"objective,omitempty"`
	AcceptanceCriteria []string                `json:"acceptanceCriteria,omitempty"`
	Priority           *string                 `json:"priority,omitempty"`
	Labels             []string                `json:"labels,omitempty"`
	DependencyKind     string                  `json:"dependencyKind,omitempty"`
	DependencyID       string                  `json:"dependencyId,omitempty"`
	Target             *ConfigurationTarget    `json:"target,omitempty"`
	Overrides          []ConfigurationOverride `json:"overrides,omitempty"`
	PreviewID          string                  `json:"previewId,omitempty"`
	Body               string                  `json:"body,omitempty"`
	Severity           string                  `json:"severity,omitempty"`
}

// MarshalJSON emits the exact discriminated-union member rather than the Go
// superset used for strict decoding. Required empty arrays and nullable fields
// therefore stay present on engine-authored tests and internal retries.
func (intent MutationIntent) MarshalJSON() ([]byte, error) {
	value := map[string]any{"type": intent.Type}
	switch intent.Type {
	case "project.create":
		value["name"] = intent.Name
	case "project.update":
		value["projectId"], value["name"] = intent.ProjectID, intent.Name
	case "epic.create":
		value["projectId"], value["key"], value["title"], value["description"] = intent.ProjectID, intent.Key, intent.Title, intent.Description
		value["priority"], value["labels"] = intent.Priority, nonNilStrings(intent.Labels)
	case "epic.update":
		value["epicId"], value["title"], value["description"] = intent.EpicID, intent.Title, intent.Description
		value["priority"], value["labels"] = intent.Priority, nonNilStrings(intent.Labels)
	case "task.create":
		value["projectId"], value["workspaceId"], value["key"] = intent.ProjectID, intent.WorkspaceID, intent.Key
		value["epicId"] = nullableString(intent.EpicID)
		value["title"], value["objective"] = intent.Title, intent.Objective
		value["acceptanceCriteria"], value["priority"], value["labels"] = nonNilStrings(intent.AcceptanceCriteria), intent.Priority, nonNilStrings(intent.Labels)
	case "task.update":
		value["taskId"], value["epicId"] = intent.TaskID, nullableString(intent.EpicID)
		value["title"], value["objective"] = intent.Title, intent.Objective
		value["acceptanceCriteria"], value["priority"], value["labels"] = nonNilStrings(intent.AcceptanceCriteria), intent.Priority, nonNilStrings(intent.Labels)
	case "dependency.add", "dependency.remove", "dependency.override":
		value["taskId"], value["dependencyKind"], value["dependencyId"] = intent.TaskID, intent.DependencyKind, intent.DependencyID
	case "configuration.preview":
		value["target"], value["overrides"] = intent.Target, nonNilOverrides(intent.Overrides)
	case "configuration.apply":
		value["target"], value["previewId"] = intent.Target, intent.PreviewID
	case "task.launch-now":
		value["taskId"] = intent.TaskID
	case "project.pause", "project.resume", "project.emergency-stop.prepare", "project.emergency-stop.confirm":
		value["projectId"] = intent.ProjectID
	case "task.cancel", "task.integrate":
		value["projectId"], value["taskId"], value["runId"] = intent.ProjectID, intent.TaskID, intent.RunID
	case "task.feedback":
		value["projectId"], value["taskId"], value["runId"] = intent.ProjectID, intent.TaskID, intent.RunID
		value["body"], value["severity"] = intent.Body, intent.Severity
	}
	return json.Marshal(value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilOverrides(values []ConfigurationOverride) []ConfigurationOverride {
	if values == nil {
		return []ConfigurationOverride{}
	}
	return values
}

type MutationInput struct {
	SchemaVersion           int            `json:"schemaVersion"`
	ContractVersion         string         `json:"contractVersion"`
	ContractHash            string         `json:"contractHash"`
	RequestID               string         `json:"requestId"`
	IdempotencyKey          string         `json:"idempotencyKey"`
	ExpectedVersion         string         `json:"expectedVersion"`
	HumanApprovalRef        *string        `json:"humanApprovalRef"`
	AcknowledgementRevision *string        `json:"acknowledgementRevision"`
	Intent                  MutationIntent `json:"intent"`
}

type MutationResult struct {
	SchemaVersion   int     `json:"schemaVersion"`
	ContractVersion string  `json:"contractVersion"`
	ContractHash    string  `json:"contractHash"`
	Cursor          string  `json:"cursor"`
	RequestID       string  `json:"requestId"`
	Status          string  `json:"status"`
	Message         string  `json:"message"`
	UpdatedVersion  *string `json:"updatedVersion"`
	Preview         any     `json:"preview"`
	ConfirmationRef *string `json:"confirmationRef"`
}

func ParseExpectedVersion(value string) (uint64, error) {
	result, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(result, 10) != value {
		return 0, ErrQueryInvalid
	}
	return result, nil
}

func boundedMutationText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0 &&
		safedata.ClassifyText(value, true) == safedata.Safe
}

func validMutationPriority(value *string, nullable bool) bool {
	if value == nil {
		return nullable
	}
	return *value == "urgent" || *value == "high" || *value == "normal" || *value == "low"
}

func validMutationLabels(values []string) bool {
	return validUnique(values, 64, nil)
}

func validConfigurationTarget(value *ConfigurationTarget) bool {
	return value != nil && validOpaque(value.ID) &&
		(value.Scope == "project" || value.Scope == "workspace" || value.Scope == "task")
}

func validConfigurationOverride(value ConfigurationOverride) bool {
	keys := allowed("launchPolicy", "maxActiveTasks", "maxActiveTasksPerWorkspace", "maxConcurrentAgents",
		"maxSubagentsPerTask", "autoFixCiFailures", "autoFixReviewFeedback", "requireDifferentReviewerModel")
	if _, ok := keys[value.Key]; !ok || value.Mode != "inherit" && value.Mode != "value" {
		return false
	}
	if value.Mode == "inherit" {
		return value.Value == nil
	}
	switch current := value.Value.(type) {
	case bool:
		return true
	case string:
		if current == "manual" || current == "automatic" {
			return true
		}
		_, err := ParseExpectedVersion(current)
		return err == nil
	default:
		return false
	}
}

// ValidateMutation closes the Go mutation boundary over every intent emitted
// by the generated planning contract. It validates shape only; an engine-owned
// application service still decides whether current policy and facts permit
// the requested action.
func ValidateMutation(input MutationInput) error {
	hash, err := SchemaSHA256()
	if err != nil || input.SchemaVersion != 1 || input.ContractVersion != "director-planning/v1" ||
		input.ContractHash != hash || !validOpaque(input.RequestID) || input.IdempotencyKey != input.RequestID {
		return ErrQueryInvalid
	}
	if _, err := ParseExpectedVersion(input.ExpectedVersion); err != nil {
		return err
	}
	intent := input.Intent
	switch input.Intent.Type {
	case "project.create":
		if !boundedMutationText(intent.Name, 2048) || intent.ProjectID != "" || input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "project.update":
		if !validOpaque(intent.ProjectID) || !boundedMutationText(intent.Name, 2048) || input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "epic.create":
		if !validOpaque(intent.ProjectID) || !validOpaque(intent.Key) || !boundedMutationText(intent.Title, 2048) ||
			!boundedMutationText(intent.Description, 2048) || !validMutationPriority(intent.Priority, true) ||
			!validMutationLabels(intent.Labels) || input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "epic.update":
		if !validOpaque(intent.EpicID) || !boundedMutationText(intent.Title, 2048) || !boundedMutationText(intent.Description, 2048) ||
			!validMutationPriority(intent.Priority, true) || !validMutationLabels(intent.Labels) ||
			input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "task.create":
		if !validOpaque(intent.ProjectID) || !validOpaque(intent.WorkspaceID) || !validOpaque(intent.Key) ||
			(intent.EpicID != "" && !validOpaque(intent.EpicID)) || !boundedMutationText(intent.Title, 2048) ||
			!boundedMutationText(intent.Objective, 2048) || !validUnique(intent.AcceptanceCriteria, 128, nil) ||
			len(intent.AcceptanceCriteria) == 0 || !validMutationPriority(intent.Priority, false) || !validMutationLabels(intent.Labels) ||
			input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "task.update":
		if !validOpaque(intent.TaskID) || (intent.EpicID != "" && !validOpaque(intent.EpicID)) ||
			!boundedMutationText(intent.Title, 2048) || !boundedMutationText(intent.Objective, 2048) ||
			!validUnique(intent.AcceptanceCriteria, 128, nil) || len(intent.AcceptanceCriteria) == 0 ||
			!validMutationPriority(intent.Priority, false) || !validMutationLabels(intent.Labels) ||
			input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "dependency.add", "dependency.remove":
		if !validOpaque(intent.TaskID) || !validOpaque(intent.DependencyID) ||
			(intent.DependencyKind != "task" && intent.DependencyKind != "epic") ||
			input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "configuration.preview":
		if !validConfigurationTarget(intent.Target) || len(intent.Overrides) > 8 || input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
		seen := map[string]struct{}{}
		for _, override := range intent.Overrides {
			if !validConfigurationOverride(override) {
				return ErrQueryInvalid
			}
			if _, duplicate := seen[override.Key]; duplicate {
				return ErrQueryInvalid
			}
			seen[override.Key] = struct{}{}
		}
	case "configuration.apply":
		if !validConfigurationTarget(intent.Target) || !validOpaque(intent.PreviewID) ||
			input.HumanApprovalRef == nil || !validOpaque(*input.HumanApprovalRef) ||
			input.AcknowledgementRevision == nil {
			return ErrQueryInvalid
		}
		if _, err := ParseExpectedVersion(*input.AcknowledgementRevision); err != nil {
			return err
		}
	case "task.launch-now":
		if !validOpaque(intent.TaskID) || input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "dependency.override":
		if !validOpaque(intent.TaskID) || !validOpaque(intent.DependencyID) ||
			(intent.DependencyKind != "task" && intent.DependencyKind != "epic") || input.HumanApprovalRef == nil ||
			!validOpaque(*input.HumanApprovalRef) || input.AcknowledgementRevision == nil {
			return ErrQueryInvalid
		}
		if _, err := ParseExpectedVersion(*input.AcknowledgementRevision); err != nil {
			return err
		}
	case "project.pause", "project.resume", "project.emergency-stop.prepare":
		if !validOpaque(intent.ProjectID) || input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "project.emergency-stop.confirm":
		if !validOpaque(intent.ProjectID) || input.HumanApprovalRef == nil ||
			!validOpaque(*input.HumanApprovalRef) || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "task.cancel", "task.integrate":
		if !validOpaque(intent.ProjectID) || !validOpaque(intent.TaskID) || !validOpaque(intent.RunID) ||
			input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "task.feedback":
		if !validOpaque(intent.ProjectID) || !validOpaque(intent.TaskID) || !validOpaque(intent.RunID) ||
			!boundedMutationText(intent.Body, 2048) || (intent.Severity != "P0" && intent.Severity != "P1" && intent.Severity != "P2" && intent.Severity != "P3") ||
			input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	default:
		return errors.New("planning mutation intent is not in the generated contract")
	}
	return nil
}

// ValidateControlMutation is retained for callers compiled against the M3
// execution-control-only boundary. Its behavior now matches the complete
// generated planning mutation vocabulary.
func ValidateControlMutation(input MutationInput) error { return ValidateMutation(input) }
