// SPDX-License-Identifier: Apache-2.0

package planning

import (
	"errors"
	"strconv"
)

const MutationPath = "/v1/planning/mutate"

type MutationIntent struct {
	Type      string `json:"type"`
	ProjectID string `json:"projectId,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
	RunID     string `json:"runId,omitempty"`
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

func ValidateControlMutation(input MutationInput) error {
	hash, err := SchemaSHA256()
	if err != nil {
		return ErrQueryInvalid
	}
	if input.SchemaVersion != 1 || input.ContractVersion != "director-planning/v1" ||
		input.ContractHash != hash ||
		!validOpaque(input.RequestID) || input.IdempotencyKey != input.RequestID ||
		!validOpaque(input.Intent.ProjectID) {
		return ErrQueryInvalid
	}
	if _, err := ParseExpectedVersion(input.ExpectedVersion); err != nil {
		return err
	}
	switch input.Intent.Type {
	case "project.pause", "project.resume", "project.emergency-stop.prepare":
		if input.Intent.TaskID != "" || input.Intent.RunID != "" || input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "project.emergency-stop.confirm":
		if input.Intent.TaskID != "" || input.Intent.RunID != "" || input.HumanApprovalRef == nil ||
			!validOpaque(*input.HumanApprovalRef) || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	case "task.cancel":
		if !validOpaque(input.Intent.TaskID) || !validOpaque(input.Intent.RunID) ||
			input.HumanApprovalRef != nil || input.AcknowledgementRevision != nil {
			return ErrQueryInvalid
		}
	default:
		return errors.New("planning mutation is not an execution control")
	}
	return nil
}
