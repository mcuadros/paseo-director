// SPDX-License-Identifier: Apache-2.0

package executionoracle

import (
	"errors"
	"slices"
	"sync"
)

var (
	// ErrInjectedCrash marks a deterministic crash cut requested by a test.
	ErrInjectedCrash = errors.New("executionoracle: injected crash")
	// ErrEffectConflict rejects one idempotency key reused for another action.
	ErrEffectConflict = errors.New("executionoracle: effect key conflict")
	// ErrScopeViolation rejects any cross-Run fake-host access.
	ErrScopeViolation = errors.New("executionoracle: fixed scope violation")
	// ErrObservationAmbiguous marks a result which cannot be adopted or retried.
	ErrObservationAmbiguous = errors.New("executionoracle: observation ambiguous")
)

// EffectPhase is the closed fake-store intent/dispatch/observe vocabulary.
type EffectPhase uint8

const (
	EffectIntentRecorded EffectPhase = iota + 1
	EffectDispatched
	EffectObserved
)

func (phase EffectPhase) valid() bool {
	return phase >= EffectIntentRecorded && phase <= EffectObserved
}

// CrashPoint injects a process loss immediately after one durable boundary.
type CrashPoint uint8

const (
	CrashNone CrashPoint = iota + 1
	CrashAfterIntent
	CrashAfterDispatch
	CrashAfterObserve
)

// CrashPoints returns every injected boundary, excluding the no-crash case.
func CrashPoints() []CrashPoint {
	return []CrashPoint{CrashAfterIntent, CrashAfterDispatch, CrashAfterObserve}
}

func (point CrashPoint) valid() bool { return point >= CrashNone && point <= CrashAfterObserve }

// EffectRecord is the immutable action plus its monotonically advancing phase.
type EffectRecord struct {
	Key    string
	Action Action
	Phase  EffectPhase
}

// FakeStore is an in-memory deterministic durable-intent model. It owns no
// production interface and is safe for concurrent test calls.
type FakeStore struct {
	mu      sync.Mutex
	records map[string]EffectRecord
}

// NewFakeStore constructs an empty fake durable store.
func NewFakeStore() *FakeStore { return &FakeStore{records: make(map[string]EffectRecord)} }

func (store *FakeStore) intent(key string, action Action) (EffectRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if key == "" || !validAction(action) {
		return EffectRecord{}, ErrScopeViolation
	}
	if existing, ok := store.records[key]; ok {
		if existing.Action != action {
			return EffectRecord{}, ErrEffectConflict
		}
		return existing, nil
	}
	record := EffectRecord{Key: key, Action: action, Phase: EffectIntentRecorded}
	store.records[key] = record
	return record, nil
}

func (store *FakeStore) advance(key string, action Action, phase EffectPhase) (EffectRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !phase.valid() {
		return EffectRecord{}, ErrEffectConflict
	}
	record, ok := store.records[key]
	if !ok || record.Action != action {
		return EffectRecord{}, ErrEffectConflict
	}
	if phase > record.Phase {
		record.Phase = phase
		store.records[key] = record
	}
	return record, nil
}

// Record returns a copy of one durable effect.
func (store *FakeStore) Record(key string) (EffectRecord, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[key]
	return record, ok
}

// Records returns every effect in stable key order.
func (store *FakeStore) Records() []EffectRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]EffectRecord, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	slices.SortFunc(result, func(left, right EffectRecord) int {
		if left.Key < right.Key {
			return -1
		}
		if left.Key > right.Key {
			return 1
		}
		return 0
	})
	return result
}

// FakeResource is the complete host-visible resource state. A workspace uses
// Role zero; every agent uses one of the three execution roles.
type FakeResource struct {
	ID       string
	Scope    Scope
	Role     Role
	ParentID string
	Prompted bool
	Archived bool
}

// FakeHost is a deterministic, scope-checking host with idempotent physical
// mutations and no process, filesystem, network, Git, or provider effects.
type FakeHost struct {
	mu           sync.Mutex
	resources    map[string]FakeResource
	mutations    map[string]uint64
	replacements map[Scope]uint8
}

// NewFakeHost constructs an empty fake host.
func NewFakeHost() *FakeHost {
	return &FakeHost{
		resources: make(map[string]FakeResource), mutations: make(map[string]uint64),
		replacements: make(map[Scope]uint8),
	}
}

// Seed inserts an authoritative host observation without counting a mutation.
// It is intended only to establish a test precondition such as an existing
// workspace or Task-Agent-created helper.
func (host *FakeHost) Seed(resource FakeResource) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if !validResource(resource) {
		return ErrScopeViolation
	}
	if resource.Role != 0 {
		workspace, ok := host.resources[resource.Scope.WorkspaceID]
		if !ok || workspace.Scope != resource.Scope || workspace.Role != 0 || workspace.Archived {
			return ErrScopeViolation
		}
	}
	if resource.Role == RoleHelper {
		parent, ok := host.resources[resource.ParentID]
		if !ok || parent.Scope != resource.Scope || parent.Role != RoleWorker || parent.Archived {
			return ErrScopeViolation
		}
	}
	if (resource.Role == RoleWorker || resource.Role == RoleReviewer) &&
		host.hasActiveRole(resource.Scope, resource.Role, resource.ID) && !resource.Archived {
		return ErrEffectConflict
	}
	if existing, ok := host.resources[resource.ID]; ok && existing != resource {
		return ErrEffectConflict
	}
	host.resources[resource.ID] = resource
	return nil
}

func (host *FakeHost) apply(action Action) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if !validAction(action) {
		return ErrScopeViolation
	}
	switch action.Kind {
	case ActionCreateWorkspace:
		return host.create(FakeResource{ID: action.TargetID, Scope: action.Scope})
	case ActionCreateWorkerBootstrap:
		if !host.workspaceReady(action.Scope) {
			return ErrScopeViolation
		}
		if host.hasWorkerRecord(action.Scope, action.TargetID) {
			return ErrEffectConflict
		}
		return host.create(FakeResource{ID: action.TargetID, Scope: action.Scope, Role: RoleWorker})
	case ActionReplaceWorkerBootstrap:
		if !host.workspaceReady(action.Scope) {
			return ErrScopeViolation
		}
		desired := FakeResource{ID: action.TargetID, Scope: action.Scope, Role: RoleWorker}
		if _, ok := host.resources[action.TargetID]; ok {
			return host.create(desired)
		}
		if host.replacements[action.Scope] >= 1 || !host.hasArchivedWorker(action.Scope) || host.hasActiveRole(action.Scope, RoleWorker, "") {
			return ErrEffectConflict
		}
		if err := host.create(desired); err != nil {
			return err
		}
		host.replacements[action.Scope]++
		return nil
	case ActionCreateReviewerBootstrap:
		if !host.workspaceReady(action.Scope) {
			return ErrScopeViolation
		}
		if host.hasActiveRole(action.Scope, RoleReviewer, action.TargetID) {
			return ErrEffectConflict
		}
		return host.create(FakeResource{ID: action.TargetID, Scope: action.Scope, Role: RoleReviewer})
	case ActionSendWorkerPrompt, ActionSendReviewerPrompt:
		resource, ok := host.resources[action.TargetID]
		if !ok || resource.Archived || resource.Scope != action.Scope || resource.Role != action.Role {
			return ErrScopeViolation
		}
		if !resource.Prompted {
			resource.Prompted = true
			host.resources[action.TargetID] = resource
			host.mutations[action.TargetID]++
		}
		return nil
	case ActionArchiveWorker, ActionArchiveReviewer, ActionArchiveHelper:
		resource, ok := host.resources[action.TargetID]
		if !ok || resource.Scope != action.Scope || resource.Role != action.Role || resource.ParentID != action.ParentID {
			return ErrScopeViolation
		}
		if !resource.Archived {
			resource.Archived = true
			host.resources[action.TargetID] = resource
			host.mutations[action.TargetID]++
		}
		return nil
	case ActionIssueHelperAdmission, ActionObserveWorker, ActionObserveReviewer,
		ActionObserveHelper, ActionObserveAll, ActionAwaitNotification,
		ActionAwaitSafeBoundary, ActionPauseRun, ActionResumeReconcile,
		ActionCancelRun, ActionParkNeedsYou:
		return nil
	default:
		return ErrScopeViolation
	}
}

func (host *FakeHost) create(desired FakeResource) error {
	if existing, ok := host.resources[desired.ID]; ok {
		if !sameResourceIdentity(existing, desired) || existing.Archived {
			return ErrEffectConflict
		}
		return nil
	}
	host.resources[desired.ID] = desired
	host.mutations[desired.ID]++
	return nil
}

func (host *FakeHost) workspaceReady(scope Scope) bool {
	resource, ok := host.resources[scope.WorkspaceID]
	return ok && resource.Scope == scope && resource.Role == 0 && !resource.Archived
}

func (host *FakeHost) hasWorkerRecord(scope Scope, exceptID string) bool {
	for _, resource := range host.resources {
		if resource.Scope == scope && resource.Role == RoleWorker && resource.ID != exceptID {
			return true
		}
	}
	return false
}

func (host *FakeHost) hasArchivedWorker(scope Scope) bool {
	for _, resource := range host.resources {
		if resource.Scope == scope && resource.Role == RoleWorker && resource.Archived {
			return true
		}
	}
	return false
}

func (host *FakeHost) hasActiveRole(scope Scope, role Role, exceptID string) bool {
	for _, resource := range host.resources {
		if resource.Scope == scope && resource.Role == role && resource.ID != exceptID && !resource.Archived {
			return true
		}
	}
	return false
}

func (host *FakeHost) desired(action Action) bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	switch action.Kind {
	case ActionCreateWorkspace:
		resource, ok := host.resources[action.TargetID]
		return ok && sameResourceIdentity(resource, FakeResource{ID: action.TargetID, Scope: action.Scope}) && !resource.Archived
	case ActionCreateWorkerBootstrap, ActionReplaceWorkerBootstrap:
		resource, ok := host.resources[action.TargetID]
		return ok && sameResourceIdentity(resource, FakeResource{ID: action.TargetID, Scope: action.Scope, Role: RoleWorker}) && !resource.Archived
	case ActionCreateReviewerBootstrap:
		resource, ok := host.resources[action.TargetID]
		return ok && sameResourceIdentity(resource, FakeResource{ID: action.TargetID, Scope: action.Scope, Role: RoleReviewer}) && !resource.Archived
	case ActionSendWorkerPrompt, ActionSendReviewerPrompt:
		resource, ok := host.resources[action.TargetID]
		return ok && resource.Scope == action.Scope && resource.Role == action.Role && resource.Prompted && !resource.Archived
	case ActionArchiveWorker, ActionArchiveReviewer, ActionArchiveHelper:
		resource, ok := host.resources[action.TargetID]
		return ok && resource.Scope == action.Scope && resource.Role == action.Role && resource.ParentID == action.ParentID && resource.Archived
	default:
		return true
	}
}

func sameResourceIdentity(left, right FakeResource) bool {
	return left.ID == right.ID && left.Scope == right.Scope && left.Role == right.Role && left.ParentID == right.ParentID
}

// Resource returns a copy of one host resource.
func (host *FakeHost) Resource(id string) (FakeResource, bool) {
	host.mu.Lock()
	defer host.mu.Unlock()
	resource, ok := host.resources[id]
	return resource, ok
}

// Resources returns every host resource in stable ID order.
func (host *FakeHost) Resources() []FakeResource {
	host.mu.Lock()
	defer host.mu.Unlock()
	result := make([]FakeResource, 0, len(host.resources))
	for _, resource := range host.resources {
		result = append(result, resource)
	}
	slices.SortFunc(result, func(left, right FakeResource) int {
		if left.ID < right.ID {
			return -1
		}
		if left.ID > right.ID {
			return 1
		}
		return 0
	})
	return result
}

// MutationCount is the number of physical state changes for one resource.
func (host *FakeHost) MutationCount(id string) uint64 {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.mutations[id]
}

// FakeRuntime joins the deterministic store and host for crash/replay tests.
type FakeRuntime struct {
	Store *FakeStore
	Host  *FakeHost
	Scope Scope
}

// NewFakeRuntime creates a complete empty fake execution environment.
func NewFakeRuntime(scope Scope) *FakeRuntime {
	return &FakeRuntime{Store: NewFakeStore(), Host: NewFakeHost(), Scope: scope}
}

// Execute advances one action across durable intent, fake dispatch, and
// authoritative observation. Replaying the same key/action adopts the existing
// result. A different action for the key fails before host mutation.
func (runtime *FakeRuntime) Execute(key string, action Action, crash CrashPoint) error {
	if runtime == nil || runtime.Store == nil || runtime.Host == nil || !runtime.Scope.valid() ||
		action.Scope != runtime.Scope || !crash.valid() {
		return ErrScopeViolation
	}
	record, err := runtime.Store.intent(key, action)
	if err != nil {
		return err
	}
	if record.Phase == EffectObserved {
		return nil
	}
	if crash == CrashAfterIntent && record.Phase == EffectIntentRecorded {
		return ErrInjectedCrash
	}
	if record.Phase == EffectIntentRecorded {
		if err := runtime.Host.apply(action); err != nil {
			return err
		}
		if _, err := runtime.Store.advance(key, action, EffectDispatched); err != nil {
			return err
		}
		if crash == CrashAfterDispatch {
			return ErrInjectedCrash
		}
	}
	if !runtime.Host.desired(action) {
		return ErrObservationAmbiguous
	}
	if _, err := runtime.Store.advance(key, action, EffectObserved); err != nil {
		return err
	}
	if crash == CrashAfterObserve {
		return ErrInjectedCrash
	}
	return nil
}

func validAction(action Action) bool {
	if !action.Kind.valid() || !action.Scope.valid() || action.TargetID == "" || !action.Reason.valid() {
		return false
	}
	wantsNotification := action.Kind == ActionSendWorkerPrompt || action.Kind == ActionSendReviewerPrompt
	if action.NotifyOnFinish != wantsNotification {
		return false
	}
	switch action.Kind {
	case ActionCreateWorkerBootstrap, ActionSendWorkerPrompt, ActionArchiveWorker, ActionReplaceWorkerBootstrap:
		return action.Role == RoleWorker && action.ParentID == ""
	case ActionCreateReviewerBootstrap, ActionSendReviewerPrompt, ActionArchiveReviewer:
		return action.Role == RoleReviewer && action.ParentID == ""
	case ActionIssueHelperAdmission, ActionObserveHelper, ActionArchiveHelper:
		return action.Role == RoleHelper && action.ParentID != ""
	case ActionCreateWorkspace:
		return action.Role == 0 && action.ParentID == "" && action.TargetID == action.Scope.WorkspaceID
	case ActionObserveAll, ActionAwaitSafeBoundary,
		ActionPauseRun, ActionResumeReconcile, ActionCancelRun, ActionParkNeedsYou:
		return action.Role == 0 && action.ParentID == ""
	case ActionObserveWorker:
		return action.Role == RoleWorker && action.ParentID == ""
	case ActionObserveReviewer:
		return action.Role == RoleReviewer && action.ParentID == ""
	case ActionAwaitNotification:
		return (action.Role == RoleWorker || action.Role == RoleReviewer) && action.ParentID == ""
	default:
		return false
	}
}

func validResource(resource FakeResource) bool {
	if resource.ID == "" || !resource.Scope.valid() {
		return false
	}
	if resource.Role == 0 {
		return resource.ID == resource.Scope.WorkspaceID && resource.ParentID == ""
	}
	if resource.Role != RoleWorker && resource.Role != RoleReviewer && resource.Role != RoleHelper {
		return false
	}
	if resource.Role == RoleHelper {
		return resource.ParentID != ""
	}
	return resource.ParentID == ""
}
