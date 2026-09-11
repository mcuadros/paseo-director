// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"sync"

	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

// Branches is a deterministic exact-ref adapter used with fake GitHub. It
// applies only the compare condition supplied by the engine.
type Branches struct {
	mu              sync.Mutex
	RemoteCanonical string
	Heads           map[string]string
	Code            publicationdomain.ExternalCode
	PushDispatches  uint64
}

var _ gitport.BranchPort = (*Branches)(nil)

func NewBranches(remote string) *Branches {
	return &Branches{RemoteCanonical: remote, Heads: map[string]string{}, Code: publicationdomain.CodeOK}
}

func (branches *Branches) ObserveRemoteRef(_ context.Context, request gitport.RemoteRefRequest) (publicationdomain.RefObservation, error) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	code := branches.Code
	if code == "" {
		code = publicationdomain.CodeOK
	}
	oid, exists := branches.Heads[request.Branch]
	return publicationdomain.SealRefObservation(publicationdomain.RefObservation{ID: fmt.Sprintf("fake-ref-%d", request.TaskStoreNowMillis),
		Code: code, Ref: "refs/heads/" + request.Branch, Exists: exists, OID: oid,
		RemoteCanonical: branches.RemoteCanonical, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}), nil
}

func (branches *Branches) PushExact(_ context.Context, request gitport.PushRequest) (gitport.PushResult, error) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	branches.PushDispatches++
	current, exists := branches.Heads[request.Branch]
	if !exists {
		current = ""
	}
	if current != request.ExpectedRemoteOID {
		return gitport.PushResult{Handoff: true, Code: publicationdomain.CodeRefChanged}, nil
	}
	branches.Heads[request.Branch] = request.CandidateSHA
	return gitport.PushResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
}

func (branches *Branches) Set(branch, oid string) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	if oid == "" {
		delete(branches.Heads, branch)
	} else {
		branches.Heads[branch] = oid
	}
}

func (branches *Branches) Snapshot(branch string) (string, uint64) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	return branches.Heads[branch], branches.PushDispatches
}
