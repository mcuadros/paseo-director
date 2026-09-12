// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	"github.com/mcuadros/director-engine/domain/safedata"
)

const (
	MinimumWorkspacesPerProject             = 1
	MaximumWorkspacesPerProject             = 128
	MaximumDisplayNameBytes                 = 128
	MaximumProjectLeaseDurationMillis       = 5 * 60 * 1000
	ProjectLeaseObservationMaximumAgeMillis = 30 * 1000
)

var (
	ErrInvalidProject         = errors.New("Project aggregate is invalid")
	ErrInvalidWorkspace       = errors.New("Workspace aggregate is invalid")
	ErrWorkspaceConflict      = errors.New("Workspace repository mapping conflicts with another Workspace")
	ErrLeaseHeld              = errors.New("Project execution lease is held by another engine")
	ErrLeaseExpired           = errors.New("Project execution lease is expired")
	ErrLeaseIdentityMismatch  = errors.New("Project execution lease identity does not match")
	ErrLeaseProofInvalid      = errors.New("Project execution lease takeover proof is invalid")
	ErrLeaseTransitionInvalid = errors.New("Project execution lease transition is invalid")
)

var stableIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]*$`)

// OrganizerID derives the single stable Organizer aggregate identity for a
// Project. It is independent of the Organizer display name and repository
// path and cannot be caller-selected.
func OrganizerID(projectID string) string {
	digest := sha256.Sum256([]byte("director-organizer\x1f" + projectID))
	return "organizer-" + hex.EncodeToString(digest[:])
}

// WorkspaceID derives a globally stable aggregate identity from a Project and
// its stable Project-local Workspace key. Display names, checkout paths, and
// remote transport aliases are intentionally absent from the identity.
func WorkspaceID(projectID, workspaceKey string) string {
	digest := sha256.Sum256([]byte("director-workspace\x1f" + projectID + "\x1f" + workspaceKey))
	return "workspace-" + hex.EncodeToString(digest[:])
}

// WorkspacePolicy stores only intrinsic Workspace overrides. The engine
// application configuration package owns effective Project -> Workspace ->
// Task inheritance.
type WorkspacePolicy struct {
	LaunchPolicy string `json:"launchPolicy"`
	DeliveryMode string `json:"deliveryMode"`
}

// RepositoryIdentity binds a Workspace to one canonical source repository.
// ID and Key are stable across supported remote aliases and checkout moves.
type RepositoryIdentity struct {
	ID                 string `json:"id"`
	Key                string `json:"key"`
	CanonicalRemote    string `json:"canonicalRemote"`
	SourcePath         string `json:"sourcePath"`
	SourceDevice       uint64 `json:"sourceDevice"`
	SourceInode        uint64 `json:"sourceInode"`
	GitCommonDirectory string `json:"gitCommonDirectory"`
	GitCommonDevice    uint64 `json:"gitCommonDevice"`
	GitCommonInode     uint64 `json:"gitCommonInode"`
}

// Workspace is the mutable Director aggregate for exactly one canonical
// source repository. ID and ProjectID are immutable; Name and checkout path
// may change without changing identity.
type Workspace struct {
	ID                string             `json:"id"`
	ProjectID         string             `json:"projectId"`
	Key               string             `json:"key"`
	Name              string             `json:"name"`
	Repository        RepositoryIdentity `json:"repository"`
	DefaultBaseBranch string             `json:"defaultBaseBranch"`
	Policy            WorkspacePolicy    `json:"policy"`
	Version           uint64             `json:"version"`
}

// ProjectLease is the one durable execution lease for a Project. A takeover
// after expiry starts observe-only until a fresh absence/reconciliation proof
// is consumed by the engine.
type ProjectLease struct {
	HolderInstance        string `json:"holderInstance"`
	HolderProcessIdentity string `json:"holderProcessIdentity"`
	Epoch                 uint64 `json:"epoch"`
	AcquiredAtMillis      int64  `json:"acquiredAtMillis"`
	ExpiresAtMillis       int64  `json:"expiresAtMillis"`
	RenewedAtMillis       int64  `json:"renewedAtMillis"`
	DispatchAllowed       bool   `json:"dispatchAllowed"`
	PriorHolderInstance   string `json:"priorHolderInstance,omitempty"`
	PriorProcessIdentity  string `json:"priorProcessIdentity,omitempty"`
	TakeoverObservationID string `json:"takeoverObservationId,omitempty"`
}

// ProjectLeaseMutationKind is the closed TaskStore-authorized lease command.
type ProjectLeaseMutationKind string

const (
	ProjectLeaseAcquire  ProjectLeaseMutationKind = "acquire"
	ProjectLeaseRenew    ProjectLeaseMutationKind = "renew"
	ProjectLeaseTakeover ProjectLeaseMutationKind = "takeover"
	ProjectLeaseRelease  ProjectLeaseMutationKind = "release"
)

// ProjectLeaseMutation contains no clock or authority fact. The TaskStore
// supplies time inside the versioned transaction. ExpectedLeaseEpoch names
// the current lease epoch, or the last allocated epoch when acquiring after
// an explicit release.
type ProjectLeaseMutation struct {
	Kind                  ProjectLeaseMutationKind `json:"kind"`
	ProjectID             string                   `json:"projectId"`
	ExpectedLeaseEpoch    uint64                   `json:"expectedLeaseEpoch"`
	HolderInstance        string                   `json:"holderInstance"`
	HolderProcessIdentity string                   `json:"holderProcessIdentity"`
	DurationMillis        int64                    `json:"durationMillis"`
}

// ProjectLeaseObservationInput is returned only by the configured authorized
// process/reconciliation adapter after it proves the complete takeover fact.
// It intentionally contains no caller-selected authority booleans or time.
type ProjectLeaseObservationInput struct {
	AdapterKind    string `json:"adapterKind"`
	AdapterVersion string `json:"adapterVersion"`
	FactHash       string `json:"factHash"`
}

type ProjectLeaseObservationKind string

const ProjectLeaseTakeoverReconciled ProjectLeaseObservationKind = "prior_process_and_dispatch_absent_reconciled"

// ProjectLeaseObservation is the typed immutable takeover fact. Recorded time
// and freshness are assigned by TaskStore inside the versioned transaction.
type ProjectLeaseObservation struct {
	ID                   string                      `json:"id"`
	Kind                 ProjectLeaseObservationKind `json:"kind"`
	ProjectID            string                      `json:"projectId"`
	HolderInstance       string                      `json:"holderInstance"`
	Epoch                uint64                      `json:"epoch"`
	PriorHolderInstance  string                      `json:"priorHolderInstance"`
	PriorProcessIdentity string                      `json:"priorProcessIdentity"`
	AdapterKind          string                      `json:"adapterKind"`
	AdapterVersion       string                      `json:"adapterVersion"`
	FactHash             string                      `json:"factHash"`
	RecordedAtMillis     int64                       `json:"recordedAtMillis"`
	MaximumAgeMillis     int64                       `json:"maximumAgeMillis"`
}

// ProjectLeaseObservationID binds every immutable adapter and TaskStore fact.
func ProjectLeaseObservationID(observation ProjectLeaseObservation) string {
	values := []string{
		string(observation.Kind), observation.ProjectID, observation.HolderInstance,
		strconv.FormatUint(observation.Epoch, 10),
		observation.PriorHolderInstance, observation.PriorProcessIdentity,
		observation.AdapterKind, observation.AdapterVersion, observation.FactHash,
		strconv.FormatInt(observation.RecordedAtMillis, 10), strconv.FormatInt(observation.MaximumAgeMillis, 10),
	}
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return "lease-observation-" + hex.EncodeToString(digest[:])
}

func boundedIdentity(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0 &&
		safedata.ClassifyText(value, true) == safedata.Safe
}

func boundedDisplayName(value string) bool {
	return boundedIdentity(value, MaximumDisplayNameBytes)
}

func stableIdentity(value string, maximum int) bool {
	return boundedIdentity(value, maximum) && stableIdentityPattern.MatchString(value)
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
	}) < 0
}

func validLeaseObservation(projectID string, lease *ProjectLease, observation *ProjectLeaseObservation) bool {
	if observation == nil {
		return lease == nil || lease.TakeoverObservationID == ""
	}
	if lease == nil || observation.Kind != ProjectLeaseTakeoverReconciled ||
		lease.PriorHolderInstance == "" || lease.PriorProcessIdentity == "" ||
		observation.ProjectID != projectID || observation.HolderInstance != lease.HolderInstance ||
		observation.Epoch != lease.Epoch || observation.PriorHolderInstance != lease.PriorHolderInstance ||
		observation.PriorProcessIdentity != lease.PriorProcessIdentity ||
		!stableIdentity(observation.AdapterKind, 64) || !stableIdentity(observation.AdapterVersion, 64) ||
		!validHash(observation.FactHash) || observation.RecordedAtMillis <= 0 ||
		observation.RecordedAtMillis < lease.AcquiredAtMillis || observation.RecordedAtMillis >= lease.ExpiresAtMillis ||
		observation.MaximumAgeMillis != ProjectLeaseObservationMaximumAgeMillis ||
		observation.ID != ProjectLeaseObservationID(*observation) {
		return false
	}
	return lease.TakeoverObservationID == "" || lease.TakeoverObservationID == observation.ID
}

// ValidateProject checks the Project aggregate invariants which are
// independent of Organizer saga phase details.
func ValidateProject(project Project) error {
	if !stableIdentity(project.ID, 128) || !boundedDisplayName(project.Name) ||
		(project.State != "active" && project.State != "paused" && project.State != "degraded" && project.State != "archived") ||
		project.Organizer == nil || project.Organizer.ID != OrganizerID(project.ID) || !validLease(project.Lease) ||
		(project.Lease != nil && project.LastLeaseEpoch != project.Lease.Epoch) ||
		!validLeaseObservation(project.ID, project.Lease, project.LeaseObservation) ||
		!execution.ValidProjectControl(project.Control, project.ID) ||
		!execution.ValidProjectControlState(project.Control, project.State) {
		return ErrInvalidProject
	}
	return nil
}

func validCanonicalPath(value string) bool {
	return len(value) >= 2 && len(value) <= repositorydomain.MaximumPathBytes && filepath.IsAbs(value) &&
		filepath.Clean(value) == value && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	}) < 0 && safedata.ClassifyText(value, false) == safedata.Safe
}

func validGitBranch(value string) bool {
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
	return strings.IndexFunc(value, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r)
	}) < 0
}

func validWorkspacePolicy(policy WorkspacePolicy) bool {
	if policy.LaunchPolicy != "inherit" && policy.LaunchPolicy != "manual" && policy.LaunchPolicy != "automatic" {
		return false
	}
	return policy.DeliveryMode == "inherit" || policy.DeliveryMode == "pull_request" || policy.DeliveryMode == "direct"
}

// ValidateWorkspace checks the pure, already-observed Workspace contract.
func ValidateWorkspace(workspace Workspace) error {
	remote, err := repositorydomain.CanonicalRemote(workspace.Repository.CanonicalRemote)
	relativeCommon, relativeErr := filepath.Rel(workspace.Repository.SourcePath, workspace.Repository.GitCommonDirectory)
	if err != nil || remote.Canonical != workspace.Repository.CanonicalRemote ||
		remote.ID != workspace.Repository.ID || remote.Key != workspace.Repository.Key ||
		!stableIdentity(workspace.ID, 128) || !stableIdentity(workspace.ProjectID, 128) ||
		!stableIdentity(workspace.Key, 128) || workspace.ID != WorkspaceID(workspace.ProjectID, workspace.Key) ||
		!boundedDisplayName(workspace.Name) || !validCanonicalPath(workspace.Repository.SourcePath) ||
		workspace.Repository.SourceDevice == 0 || workspace.Repository.SourceInode == 0 ||
		!validCanonicalPath(workspace.Repository.GitCommonDirectory) ||
		workspace.Repository.GitCommonDevice == 0 || workspace.Repository.GitCommonInode == 0 ||
		relativeErr != nil || relativeCommon == "." || relativeCommon == ".." ||
		strings.HasPrefix(relativeCommon, ".."+string(filepath.Separator)) ||
		!validGitBranch(workspace.DefaultBaseBranch) ||
		!validWorkspacePolicy(workspace.Policy) {
		return ErrInvalidWorkspace
	}
	return nil
}

// ValidateWorkspaceSet checks the frozen 1..128 Project bound and rejects any
// duplicate stable identity, canonical remote repository, canonical checkout,
// or Git common-directory mapping.
func ValidateWorkspaceSet(projectID string, workspaces []Workspace) error {
	if len(workspaces) < MinimumWorkspacesPerProject || len(workspaces) > MaximumWorkspacesPerProject {
		return ErrInvalidWorkspace
	}
	ids := make(map[string]struct{}, len(workspaces))
	keys := make(map[string]struct{}, len(workspaces))
	repositories := make(map[string]struct{}, len(workspaces))
	paths := make(map[string]struct{}, len(workspaces))
	commonDirectories := make(map[string]struct{}, len(workspaces))
	sourceIdentities := make(map[[2]uint64]struct{}, len(workspaces))
	commonIdentities := make(map[[2]uint64]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		if workspace.ProjectID != projectID || ValidateWorkspace(workspace) != nil {
			return ErrInvalidWorkspace
		}
		for value, seen := range map[string]map[string]struct{}{
			workspace.ID:                            ids,
			workspace.Key:                           keys,
			workspace.Repository.Key:                repositories,
			workspace.Repository.SourcePath:         paths,
			workspace.Repository.GitCommonDirectory: commonDirectories,
		} {
			if _, exists := seen[value]; exists {
				return ErrWorkspaceConflict
			}
			seen[value] = struct{}{}
		}
		sourceIdentity := [2]uint64{workspace.Repository.SourceDevice, workspace.Repository.SourceInode}
		if _, exists := sourceIdentities[sourceIdentity]; exists {
			return ErrWorkspaceConflict
		}
		sourceIdentities[sourceIdentity] = struct{}{}
		commonIdentity := [2]uint64{workspace.Repository.GitCommonDevice, workspace.Repository.GitCommonInode}
		if _, exists := commonIdentities[commonIdentity]; exists {
			return ErrWorkspaceConflict
		}
		commonIdentities[commonIdentity] = struct{}{}
	}
	return nil
}

func validLease(lease *ProjectLease) bool {
	if lease == nil {
		return true
	}
	if !boundedIdentity(lease.HolderInstance, 256) || !boundedIdentity(lease.HolderProcessIdentity, 256) ||
		lease.Epoch == 0 || lease.AcquiredAtMillis <= 0 || lease.RenewedAtMillis < lease.AcquiredAtMillis ||
		lease.ExpiresAtMillis <= lease.RenewedAtMillis {
		return false
	}
	if lease.DispatchAllowed {
		return (lease.PriorHolderInstance == "" && lease.PriorProcessIdentity == "" && lease.TakeoverObservationID == "") ||
			(lease.PriorHolderInstance != "" && lease.PriorProcessIdentity != "" && boundedIdentity(lease.TakeoverObservationID, 128))
	}
	return lease.PriorHolderInstance != "" && lease.PriorProcessIdentity != "" && lease.TakeoverObservationID == ""
}

// ValidateProjectLease validates one persisted lease without authorizing a
// transition.
func ValidateProjectLease(lease *ProjectLease) error {
	if !validLease(lease) {
		return ErrLeaseTransitionInvalid
	}
	return nil
}

func validLeaseMutation(mutation ProjectLeaseMutation) bool {
	if !stableIdentity(mutation.ProjectID, 128) || !stableIdentity(mutation.HolderInstance, 256) ||
		!stableIdentity(mutation.HolderProcessIdentity, 256) {
		return false
	}
	switch mutation.Kind {
	case ProjectLeaseAcquire:
		return mutation.DurationMillis > 0 &&
			mutation.DurationMillis <= MaximumProjectLeaseDurationMillis
	case ProjectLeaseRenew, ProjectLeaseTakeover:
		return mutation.ExpectedLeaseEpoch > 0 && mutation.DurationMillis > 0 &&
			mutation.DurationMillis <= MaximumProjectLeaseDurationMillis
	case ProjectLeaseRelease:
		return mutation.ExpectedLeaseEpoch > 0 && mutation.DurationMillis == 0
	default:
		return false
	}
}

// ApplyProjectLeaseMutation reduces one explicit mutation using authoritative
// TaskStore time read inside the caller transaction.
func ApplyProjectLeaseMutation(
	current *ProjectLease,
	lastAllocatedEpoch uint64,
	mutation ProjectLeaseMutation,
	nowMillis int64,
) (*ProjectLease, uint64, error) {
	if !validLeaseMutation(mutation) || nowMillis <= 0 ||
		mutation.DurationMillis > 0 && nowMillis > int64(^uint64(0)>>1)-mutation.DurationMillis {
		return current, lastAllocatedEpoch, ErrLeaseTransitionInvalid
	}
	if current != nil && (!validLease(current) || current.Epoch != lastAllocatedEpoch) {
		return current, lastAllocatedEpoch, ErrLeaseTransitionInvalid
	}
	switch mutation.Kind {
	case ProjectLeaseAcquire:
		if current != nil {
			return current, lastAllocatedEpoch, ErrLeaseHeld
		}
		if mutation.ExpectedLeaseEpoch != lastAllocatedEpoch {
			return current, lastAllocatedEpoch, ErrLeaseExpired
		}
		if lastAllocatedEpoch == ^uint64(0) {
			return current, lastAllocatedEpoch, ErrLeaseTransitionInvalid
		}
		nextEpoch := lastAllocatedEpoch + 1
		return &ProjectLease{
			HolderInstance: mutation.HolderInstance, HolderProcessIdentity: mutation.HolderProcessIdentity,
			Epoch: nextEpoch, AcquiredAtMillis: nowMillis, RenewedAtMillis: nowMillis,
			ExpiresAtMillis: nowMillis + mutation.DurationMillis, DispatchAllowed: true,
		}, nextEpoch, nil
	case ProjectLeaseRenew:
		if current == nil || current.Epoch != mutation.ExpectedLeaseEpoch || current.ExpiresAtMillis <= nowMillis {
			return current, lastAllocatedEpoch, ErrLeaseExpired
		}
		if current.HolderInstance != mutation.HolderInstance || current.HolderProcessIdentity != mutation.HolderProcessIdentity {
			return current, lastAllocatedEpoch, ErrLeaseIdentityMismatch
		}
		next := *current
		next.RenewedAtMillis = nowMillis
		next.ExpiresAtMillis = nowMillis + mutation.DurationMillis
		return &next, lastAllocatedEpoch, nil
	case ProjectLeaseTakeover:
		if current == nil || current.Epoch != mutation.ExpectedLeaseEpoch {
			return current, lastAllocatedEpoch, ErrLeaseExpired
		}
		if current.ExpiresAtMillis > nowMillis {
			return current, lastAllocatedEpoch, ErrLeaseHeld
		}
		if current.HolderProcessIdentity == mutation.HolderProcessIdentity {
			return current, lastAllocatedEpoch, ErrLeaseIdentityMismatch
		}
		if lastAllocatedEpoch == ^uint64(0) {
			return current, lastAllocatedEpoch, ErrLeaseTransitionInvalid
		}
		nextEpoch := lastAllocatedEpoch + 1
		return &ProjectLease{
			HolderInstance: mutation.HolderInstance, HolderProcessIdentity: mutation.HolderProcessIdentity,
			Epoch: nextEpoch, AcquiredAtMillis: nowMillis, RenewedAtMillis: nowMillis,
			ExpiresAtMillis: nowMillis + mutation.DurationMillis, DispatchAllowed: false,
			PriorHolderInstance: current.HolderInstance, PriorProcessIdentity: current.HolderProcessIdentity,
		}, nextEpoch, nil
	case ProjectLeaseRelease:
		if current == nil || current.Epoch != mutation.ExpectedLeaseEpoch || current.ExpiresAtMillis <= nowMillis {
			return current, lastAllocatedEpoch, ErrLeaseExpired
		}
		if current.HolderInstance != mutation.HolderInstance || current.HolderProcessIdentity != mutation.HolderProcessIdentity {
			return current, lastAllocatedEpoch, ErrLeaseIdentityMismatch
		}
		return nil, lastAllocatedEpoch, nil
	default:
		return current, lastAllocatedEpoch, ErrLeaseTransitionInvalid
	}
}

// NewProjectLeaseObservation binds an authorized adapter result to exact
// current lease facts and TaskStore time.
func NewProjectLeaseObservation(
	projectID string,
	lease *ProjectLease,
	input ProjectLeaseObservationInput,
	recordedAtMillis int64,
) (*ProjectLeaseObservation, error) {
	if lease == nil || !validLease(lease) || lease.DispatchAllowed || lease.ExpiresAtMillis <= recordedAtMillis ||
		lease.PriorHolderInstance == "" ||
		lease.PriorProcessIdentity == "" || !stableIdentity(input.AdapterKind, 64) ||
		!stableIdentity(input.AdapterVersion, 64) || !validHash(input.FactHash) || recordedAtMillis <= 0 {
		return nil, ErrLeaseProofInvalid
	}
	observation := &ProjectLeaseObservation{
		Kind: ProjectLeaseTakeoverReconciled, ProjectID: projectID,
		HolderInstance: lease.HolderInstance, Epoch: lease.Epoch,
		PriorHolderInstance: lease.PriorHolderInstance, PriorProcessIdentity: lease.PriorProcessIdentity,
		AdapterKind: input.AdapterKind, AdapterVersion: input.AdapterVersion, FactHash: input.FactHash,
		RecordedAtMillis: recordedAtMillis, MaximumAgeMillis: ProjectLeaseObservationMaximumAgeMillis,
	}
	observation.ID = ProjectLeaseObservationID(*observation)
	return observation, nil
}

// EnableProjectLeaseDispatch consumes the exact persisted observation. The
// observation contains no caller-selected authority booleans or clock.
func EnableProjectLeaseDispatch(
	projectID string,
	current *ProjectLease,
	observation *ProjectLeaseObservation,
	nowMillis int64,
) (*ProjectLease, error) {
	if current == nil || !validLease(current) || current.DispatchAllowed || current.ExpiresAtMillis <= nowMillis ||
		!validLeaseObservation(projectID, current, observation) || observation == nil ||
		observation.RecordedAtMillis > nowMillis ||
		nowMillis-observation.RecordedAtMillis > observation.MaximumAgeMillis {
		return current, ErrLeaseProofInvalid
	}
	next := *current
	next.DispatchAllowed = true
	next.TakeoverObservationID = observation.ID
	return &next, nil
}
