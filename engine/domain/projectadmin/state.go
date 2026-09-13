// SPDX-License-Identifier: Apache-2.0

package projectadmin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
)

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type SessionState string

const (
	SessionActive  SessionState = "active"
	SessionRevoked SessionState = "revoked"
)

// SessionBinding is the complete immutable authority of one administration
// session. It deliberately has no Task, Run, Candidate, host, or path field.
type SessionBinding struct {
	SchemaVersion     string `json:"schemaVersion"`
	SessionID         string `json:"sessionId"`
	ProjectID         string `json:"projectId"`
	NativeWorkspaceID string `json:"nativeWorkspaceId"`
	NativeAgentID     string `json:"nativeAgentId"`
	Audience          string `json:"audience"`
}

type SessionRecord struct {
	Binding         SessionBinding `json:"binding"`
	TokenSHA256     string         `json:"tokenSha256"`
	State           SessionState   `json:"state"`
	CreatedAtMillis int64          `json:"createdAtMillis"`
	RevokedAtMillis int64          `json:"revokedAtMillis,omitempty"`
}

type Receipt struct {
	Key              string          `json:"key"`
	SessionID        string          `json:"sessionId"`
	ToolName         string          `json:"toolName"`
	RequestID        string          `json:"requestId"`
	PayloadSHA256    string          `json:"payloadSha256"`
	Output           json.RawMessage `json:"output"`
	RecordedAtMillis int64           `json:"recordedAtMillis"`
}

type State struct {
	Sessions []SessionRecord `json:"sessions,omitempty"`
	Receipts []Receipt       `json:"receipts,omitempty"`
}

func ValidBinding(binding SessionBinding) bool {
	return binding.SchemaVersion == SessionSchemaVersion && identityPattern.MatchString(binding.SessionID) &&
		identityPattern.MatchString(binding.ProjectID) && identityPattern.MatchString(binding.NativeWorkspaceID) &&
		identityPattern.MatchString(binding.NativeAgentID) && identityPattern.MatchString(binding.Audience)
}

func ValidState(state State, projectID string) bool {
	if len(state.Sessions) > MaximumSessions || len(state.Receipts) > MaximumReceipts {
		return false
	}
	seenSessions, seenReceipts := map[string]struct{}{}, map[string]struct{}{}
	for _, session := range state.Sessions {
		if !ValidBinding(session.Binding) || session.Binding.ProjectID != projectID || !hashPattern.MatchString(session.TokenSHA256) ||
			(session.State != SessionActive && session.State != SessionRevoked) || session.CreatedAtMillis <= 0 ||
			(session.State == SessionActive && session.RevokedAtMillis != 0) ||
			(session.State == SessionRevoked && session.RevokedAtMillis < session.CreatedAtMillis) {
			return false
		}
		if _, duplicate := seenSessions[session.Binding.SessionID]; duplicate {
			return false
		}
		seenSessions[session.Binding.SessionID] = struct{}{}
	}
	for _, receipt := range state.Receipts {
		if !identityPattern.MatchString(receipt.Key) || !identityPattern.MatchString(receipt.SessionID) ||
			!identityPattern.MatchString(receipt.ToolName) || !identityPattern.MatchString(receipt.RequestID) ||
			!hashPattern.MatchString(receipt.PayloadSHA256) || receipt.RecordedAtMillis <= 0 ||
			len(receipt.Output) == 0 || len(receipt.Output) > MaximumResponseBytes {
			return false
		}
		if _, exists := seenSessions[receipt.SessionID]; !exists {
			return false
		}
		if _, duplicate := seenReceipts[receipt.Key]; duplicate {
			return false
		}
		seenReceipts[receipt.Key] = struct{}{}
	}
	return true
}

func Clone(state State) State {
	result := State{Sessions: slices.Clone(state.Sessions), Receipts: slices.Clone(state.Receipts)}
	for index := range result.Receipts {
		result.Receipts[index].Output = slices.Clone(result.Receipts[index].Output)
	}
	return result
}

func TokenMatches(record SessionRecord, token string) bool {
	digest := sha256.Sum256([]byte(token))
	want, err := hex.DecodeString(record.TokenSHA256)
	return err == nil && len(want) == len(digest) && subtle.ConstantTimeCompare(want, digest[:]) == 1
}
