// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"

	"github.com/mcuadros/director-engine/domain/execution"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
)

type RemoteRefRequest struct {
	Repository         execution.RepositoryBinding `json:"repository"`
	RepositorySHA256   string                      `json:"repositorySha256"`
	RemoteName         string                      `json:"remoteName"`
	CanonicalRemote    string                      `json:"canonicalRemote"`
	Branch             string                      `json:"branch"`
	TaskStoreNowMillis int64                       `json:"taskStoreNowMillis"`
}

type PushRequest struct {
	RemoteRefRequest
	CandidateSHA      string   `json:"candidateSha"`
	ExpectedRemoteOID string   `json:"expectedRemoteOid,omitempty"`
	ProtectedBranches []string `json:"protectedBranches"`
}

type PushResult struct {
	Handoff bool                           `json:"handoff"`
	Code    publicationdomain.ExternalCode `json:"code"`
}

type BranchPort interface {
	ObserveRemoteRef(context.Context, RemoteRefRequest) (publicationdomain.RefObservation, error)
	PushExact(context.Context, PushRequest) (PushResult, error)
}
