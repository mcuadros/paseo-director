// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"errors"
	"fmt"
	"testing"

	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	"github.com/mcuadros/director-engine/internal/testkit/secretfixture"
)

func workspaceFixture(t *testing.T, projectID string, index int) Workspace {
	t.Helper()
	remote, err := repositorydomain.CanonicalRemote(fmt.Sprintf("https://git.example/group/repository-%d.git", index))
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/srv/workspaces/repository-%d", index)
	return Workspace{
		ID: WorkspaceID(projectID, fmt.Sprintf("workspace-%d", index)), ProjectID: projectID,
		Key: fmt.Sprintf("workspace-%d", index), Name: fmt.Sprintf("Repository %d", index),
		Repository: RepositoryIdentity{
			ID: remote.ID, Key: remote.Key, CanonicalRemote: remote.Canonical,
			SourcePath: path, SourceDevice: 1, SourceInode: uint64(index + 1),
			GitCommonDirectory: path + "/.git", GitCommonDevice: 1, GitCommonInode: uint64(index + 1000),
		},
		DefaultBaseBranch: "main",
		Policy:            WorkspacePolicy{LaunchPolicy: "inherit", DeliveryMode: "inherit"},
	}
}

func TestOrganizerIdentityIsStableAndIndependentOfMutableFields(t *testing.T) {
	first := OrganizerID("project-1")
	if first == "" || first != OrganizerID("project-1") || first == OrganizerID("project-2") {
		t.Fatalf("Organizer IDs are not stable and Project-scoped: %q", first)
	}
	project := Project{ID: "project-1", Name: "Before"}
	before := OrganizerID(project.ID)
	project.Name = "After"
	if OrganizerID(project.ID) != before {
		t.Fatal("Project rename changed Organizer identity")
	}
	project.State = "active"
	project.Organizer = &Organizer{ID: before}
	if err := ValidateProject(project); err != nil {
		t.Fatalf("ValidateProject() error = %v", err)
	}
	project.Organizer = nil
	if err := ValidateProject(project); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("Project without Organizer error = %v", err)
	}
	project.Organizer = &Organizer{ID: OrganizerID("project-2")}
	if err := ValidateProject(project); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("Project with foreign Organizer error = %v", err)
	}
}

func TestProjectAndWorkspaceRejectCredentialShapedDurableFields(t *testing.T) {
	project := Project{ID: "project-1", Name: "token=" + secretfixture.GitHubFineGrained(), State: "active",
		Organizer: &Organizer{ID: OrganizerID("project-1")}}
	if !errors.Is(ValidateProject(project), ErrInvalidProject) {
		t.Fatal("credential-bearing Project name was admitted")
	}
	workspace := workspaceFixture(t, "project-1", 1)
	workspace.Repository.SourcePath = "/srv/workspaces/sk-private"
	workspace.Repository.GitCommonDirectory = workspace.Repository.SourcePath + "/.git"
	if !errors.Is(ValidateWorkspace(workspace), ErrInvalidWorkspace) {
		t.Fatal("credential-bearing repository path was admitted")
	}
}

func TestWorkspaceSetBoundsAndCanonicalConflicts(t *testing.T) {
	projectID := "project-1"
	stable := WorkspaceID(projectID, "product")
	if stable != WorkspaceID(projectID, "product") || stable == WorkspaceID("project-2", "product") ||
		stable == WorkspaceID(projectID, "other") {
		t.Fatalf("Workspace IDs are not stable and scope-bound: %q", stable)
	}
	if err := ValidateWorkspaceSet(projectID, nil); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("empty Workspace set error = %v", err)
	}
	maximum := make([]Workspace, MaximumWorkspacesPerProject)
	for index := range maximum {
		maximum[index] = workspaceFixture(t, projectID, index)
	}
	if err := ValidateWorkspaceSet(projectID, maximum); err != nil {
		t.Fatalf("maximum Workspace set: %v", err)
	}
	tooMany := append(append([]Workspace{}, maximum...), workspaceFixture(t, projectID, len(maximum)))
	if err := ValidateWorkspaceSet(projectID, tooMany); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("oversize Workspace set error = %v", err)
	}

	base := workspaceFixture(t, projectID, 1)
	for name, mutate := range map[string]func(*Workspace){
		"stable id": func(value *Workspace) { value.ID, value.Key = base.ID, base.Key },
		"repository alias": func(value *Workspace) {
			alias, err := repositorydomain.CanonicalRemote("git@git.example:group/repository-1.git")
			if err != nil {
				t.Fatal(err)
			}
			value.Repository.ID, value.Repository.Key, value.Repository.CanonicalRemote = alias.ID, alias.Key, alias.Canonical
		},
		"source path": func(value *Workspace) {
			value.Repository.SourcePath = base.Repository.SourcePath
			value.Repository.GitCommonDirectory = base.Repository.GitCommonDirectory
		},
		"common directory": func(value *Workspace) {
			value.Repository.SourcePath = base.Repository.SourcePath
			value.Repository.GitCommonDirectory = base.Repository.GitCommonDirectory
		},
		"source filesystem identity": func(value *Workspace) {
			value.Repository.SourceDevice = base.Repository.SourceDevice
			value.Repository.SourceInode = base.Repository.SourceInode
		},
		"common filesystem identity": func(value *Workspace) {
			value.Repository.GitCommonDevice = base.Repository.GitCommonDevice
			value.Repository.GitCommonInode = base.Repository.GitCommonInode
		},
	} {
		t.Run(name, func(t *testing.T) {
			other := workspaceFixture(t, projectID, 2)
			mutate(&other)
			if err := ValidateWorkspaceSet(projectID, []Workspace{base, other}); !errors.Is(err, ErrWorkspaceConflict) {
				t.Fatalf("conflict error = %v", err)
			}
		})
	}
}

func TestWorkspaceValidationFailsClosedOnMalformedIdentityAndPolicy(t *testing.T) {
	valid := workspaceFixture(t, "project-1", 1)
	for name, mutate := range map[string]func(*Workspace){
		"noncanonical remote":         func(value *Workspace) { value.Repository.CanonicalRemote += ".git" },
		"repository key mismatch":     func(value *Workspace) { value.Repository.Key = "other/repository" },
		"missing filesystem identity": func(value *Workspace) { value.Repository.SourceInode = 0 },
		"source traversal":            func(value *Workspace) { value.Repository.SourcePath += "/../escape" },
		"external common directory":   func(value *Workspace) { value.Repository.GitCommonDirectory = "/srv/other/.git" },
		"invalid branch":              func(value *Workspace) { value.DefaultBaseBranch = "refs/../main" },
		"invalid launch override":     func(value *Workspace) { value.Policy.LaunchPolicy = "sometimes" },
		"invalid delivery override":   func(value *Workspace) { value.Policy.DeliveryMode = "fallback" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := ValidateWorkspace(candidate); !errors.Is(err, ErrInvalidWorkspace) {
				t.Fatalf("ValidateWorkspace() error = %v", err)
			}
		})
	}
}

func TestProjectLeaseUsesExactExpiryAndPersistedObservation(t *testing.T) {
	acquire := ProjectLeaseMutation{
		Kind: ProjectLeaseAcquire, ProjectID: "project-1", HolderInstance: "engine-a",
		HolderProcessIdentity: "pid-100:start-1", DurationMillis: 1_000,
	}
	lease, lastLeaseEpoch, err := ApplyProjectLeaseMutation(nil, 0, acquire, 1_000)
	if err != nil || lease.ExpiresAtMillis != 2_000 || !lease.DispatchAllowed || lastLeaseEpoch != 1 {
		t.Fatalf("initial lease = %#v epoch=%d, %v", lease, lastLeaseEpoch, err)
	}
	activeTakeover := ProjectLeaseMutation{
		Kind: ProjectLeaseTakeover, ProjectID: "project-1", ExpectedLeaseEpoch: 1,
		HolderInstance: "engine-b", HolderProcessIdentity: "pid-200:start-2", DurationMillis: 100_000,
	}
	if _, _, err := ApplyProjectLeaseMutation(lease, lastLeaseEpoch, activeTakeover, 1_999); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("pre-expiry takeover error = %v", err)
	}
	if _, _, err := ApplyProjectLeaseMutation(lease, lastLeaseEpoch, ProjectLeaseMutation{
		Kind: ProjectLeaseTakeover, ProjectID: "project-1", ExpectedLeaseEpoch: 1,
		HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 100_000,
	}, 2_000); !errors.Is(err, ErrLeaseIdentityMismatch) {
		t.Fatalf("same-process takeover error = %v", err)
	}
	takeover, lastLeaseEpoch, err := ApplyProjectLeaseMutation(lease, lastLeaseEpoch, activeTakeover, 2_000)
	if err != nil || takeover.Epoch != 2 || lastLeaseEpoch != 2 || takeover.DispatchAllowed || takeover.AcquiredAtMillis != 2_000 {
		t.Fatalf("exact-expiry takeover = %#v epoch=%d, %v", takeover, lastLeaseEpoch, err)
	}
	observation, err := NewProjectLeaseObservation("project-1", takeover, ProjectLeaseObservationInput{
		AdapterKind: "linux-process-supervisor", AdapterVersion: "v1",
		FactHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, 2_100)
	if err != nil || observation.ID == "" {
		t.Fatalf("observation = %#v, %v", observation, err)
	}
	boundaryEnabled, err := EnableProjectLeaseDispatch(
		"project-1", takeover, observation,
		observation.RecordedAtMillis+observation.MaximumAgeMillis,
	)
	if err != nil || !boundaryEnabled.DispatchAllowed {
		t.Fatalf("exact freshness boundary = %#v, %v", boundaryEnabled, err)
	}
	if _, err := EnableProjectLeaseDispatch("project-1", takeover, observation, 32_101); !errors.Is(err, ErrLeaseProofInvalid) {
		t.Fatalf("stale observation error = %v", err)
	}
	enabled, err := EnableProjectLeaseDispatch("project-1", takeover, observation, 2_500)
	if err != nil || !enabled.DispatchAllowed || enabled.TakeoverObservationID != observation.ID {
		t.Fatalf("enabled lease = %#v, %v", enabled, err)
	}
	released, lastLeaseEpoch, err := ApplyProjectLeaseMutation(enabled, lastLeaseEpoch, ProjectLeaseMutation{
		Kind: ProjectLeaseRelease, ProjectID: "project-1", ExpectedLeaseEpoch: 2,
		HolderInstance: "engine-b", HolderProcessIdentity: "pid-200:start-2",
	}, 3_000)
	if err != nil || released != nil || lastLeaseEpoch != 2 {
		t.Fatalf("release = %#v epoch=%d, %v", released, lastLeaseEpoch, err)
	}
	if _, _, err := ApplyProjectLeaseMutation(nil, lastLeaseEpoch, acquire, 3_100); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("stale reacquire epoch error = %v", err)
	}
	reacquire := ProjectLeaseMutation{
		Kind: ProjectLeaseAcquire, ProjectID: "project-1", ExpectedLeaseEpoch: lastLeaseEpoch,
		HolderInstance: "engine-b", HolderProcessIdentity: "pid-200:start-2", DurationMillis: 1_000,
	}
	reacquired, lastLeaseEpoch, err := ApplyProjectLeaseMutation(nil, lastLeaseEpoch, reacquire, 3_100)
	if err != nil || reacquired.Epoch != 3 || lastLeaseEpoch != 3 || !reacquired.DispatchAllowed {
		t.Fatalf("monotonic reacquire = %#v epoch=%d, %v", reacquired, lastLeaseEpoch, err)
	}
	if _, err := EnableProjectLeaseDispatch("project-1", reacquired, observation, 3_200); !errors.Is(err, ErrLeaseProofInvalid) {
		t.Fatalf("old epoch observation error = %v", err)
	}
	project := Project{
		ID: "project-1", Name: "Project", State: "active", Organizer: &Organizer{ID: OrganizerID("project-1")},
		LastLeaseEpoch: lastLeaseEpoch, Lease: reacquired,
	}
	if err := ValidateProject(project); err != nil {
		t.Fatalf("Project with matching fencing epoch = %v", err)
	}
	project.LastLeaseEpoch--
	if err := ValidateProject(project); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("Project with regressed fencing epoch error = %v", err)
	}
}

func TestProjectLeaseRejectsMalformedMutationsAndObservations(t *testing.T) {
	valid := ProjectLeaseMutation{
		Kind: ProjectLeaseAcquire, ProjectID: "project-1", HolderInstance: "engine-a",
		HolderProcessIdentity: "pid-100:start-1", DurationMillis: 1_000,
	}
	for name, mutate := range map[string]func(*ProjectLeaseMutation){
		"caller timestamp field impossible": func(value *ProjectLeaseMutation) { value.DurationMillis = -1 },
		"unbounded duration":                func(value *ProjectLeaseMutation) { value.DurationMillis = MaximumProjectLeaseDurationMillis + 1 },
		"invalid process identity":          func(value *ProjectLeaseMutation) { value.HolderProcessIdentity = "bad identity" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if _, _, err := ApplyProjectLeaseMutation(nil, 0, candidate, 1_000); !errors.Is(err, ErrLeaseTransitionInvalid) {
				t.Fatalf("mutation error = %v", err)
			}
		})
	}
	if _, _, err := ApplyProjectLeaseMutation(nil, 0, ProjectLeaseMutation{
		Kind: ProjectLeaseAcquire, ProjectID: "project-1", ExpectedLeaseEpoch: 1,
		HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 1_000,
	}, 1_000); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("stale initial epoch error = %v", err)
	}
	if _, retainedEpoch, err := ApplyProjectLeaseMutation(nil, ^uint64(0), ProjectLeaseMutation{
		Kind: ProjectLeaseAcquire, ProjectID: "project-1", ExpectedLeaseEpoch: ^uint64(0),
		HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 1_000,
	}, 1_000); !errors.Is(err, ErrLeaseTransitionInvalid) || retainedEpoch != ^uint64(0) {
		t.Fatalf("epoch overflow = %d, %v", retainedEpoch, err)
	}
	lease, _, err := ApplyProjectLeaseMutation(nil, 0, valid, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewProjectLeaseObservation("project-1", lease, ProjectLeaseObservationInput{
		AdapterKind: "linux-process-supervisor", AdapterVersion: "v1",
		FactHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, 1_100); !errors.Is(err, ErrLeaseProofInvalid) {
		t.Fatalf("observation on dispatch-enabled lease error = %v", err)
	}
}
