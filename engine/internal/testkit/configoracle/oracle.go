// SPDX-License-Identifier: Apache-2.0

package configoracle

// SelectionKind is the closed representation of one scope's configuration.
type SelectionKind string

const (
	SelectionInherit SelectionKind = "inherit"
	SelectionValue   SelectionKind = "value"
)

// Scope is the closed Project to Workspace to Task precedence order.
type Scope string

const (
	ScopeNone      Scope = "none"
	ScopeProject   Scope = "project"
	ScopeWorkspace Scope = "workspace"
	ScopeTask      Scope = "task"
)

var scopeOrder = [...]Scope{ScopeProject, ScopeWorkspace, ScopeTask}

// RefusalCode is the oracle's closed fail-closed result vocabulary.
type RefusalCode string

const (
	RefusalNone                            RefusalCode = "none"
	RefusalInvalidSelection                RefusalCode = "invalid_selection"
	RefusalProjectValueRequired            RefusalCode = "project_value_required"
	RefusalInvalidSecurityEnvelope         RefusalCode = "invalid_security_envelope"
	RefusalOutsideSecurityEnvelope         RefusalCode = "outside_security_envelope"
	RefusalOneOffOverrideInvalid           RefusalCode = "one_off_override_invalid"
	RefusalActiveRevisionRequired          RefusalCode = "active_revision_required"
	RefusalActiveRevisionInvalid           RefusalCode = "active_revision_invalid"
	RefusalProposalRequired                RefusalCode = "proposal_required"
	RefusalProposalRevisionInvalid         RefusalCode = "proposal_revision_invalid"
	RefusalProposalInvalid                 RefusalCode = "proposal_invalid"
	RefusalPreviewRequired                 RefusalCode = "preview_required"
	RefusalPreviewStale                    RefusalCode = "preview_stale"
	RefusalHumanApprovalRequired           RefusalCode = "human_approval_required"
	RefusalHumanApprovalInvalid            RefusalCode = "human_approval_invalid"
	RefusalApprovalRevisionMismatch        RefusalCode = "approval_revision_mismatch"
	RefusalHumanAcknowledgementRequired    RefusalCode = "human_acknowledgement_required"
	RefusalHumanAcknowledgementInvalid     RefusalCode = "human_acknowledgement_invalid"
	RefusalAcknowledgementRevisionMismatch RefusalCode = "acknowledgement_revision_mismatch"
)

// ActorKind distinguishes human authority from untrusted proposal sources.
type ActorKind string

const (
	ActorHuman     ActorKind = "human"
	ActorOrganizer ActorKind = "organizer"
	ActorModel     ActorKind = "model"
)

// Selection is immutable because all representation fields are private.
type Selection struct {
	kind  SelectionKind
	value int
}

// Inherit selects the next less-specific scope.
func Inherit() Selection {
	return Selection{kind: SelectionInherit}
}

// Concrete selects one explicit value.
func Concrete(value int) Selection {
	return Selection{kind: SelectionValue, value: value}
}

// Kind returns whether the selection inherits or carries a value.
func (selection Selection) Kind() SelectionKind {
	return selection.kind
}

// Value returns the concrete value and whether one is present.
func (selection Selection) Value() (int, bool) {
	return selection.value, selection.kind == SelectionValue
}

func (selection Selection) valid() bool {
	return selection.kind == SelectionInherit || selection.kind == SelectionValue
}

// Configuration is one frozen Project, Workspace, and Task selection tuple.
// Builders return a copy and never expose a mutable field.
type Configuration struct {
	project   Selection
	workspace Selection
	task      Selection
}

// NewConfiguration creates a tuple with inherited Workspace and Task scopes.
func NewConfiguration(project Selection) Configuration {
	return Configuration{
		project:   project,
		workspace: Inherit(),
		task:      Inherit(),
	}
}

// WithWorkspace returns a copy with the Workspace selection replaced.
func (configuration Configuration) WithWorkspace(selection Selection) Configuration {
	configuration.workspace = selection
	return configuration
}

// WithTask returns a copy with the Task selection replaced.
func (configuration Configuration) WithTask(selection Selection) Configuration {
	configuration.task = selection
	return configuration
}

// Selection returns the immutable selection at one closed scope.
func (configuration Configuration) Selection(scope Scope) Selection {
	switch scope {
	case ScopeProject:
		return configuration.project
	case ScopeWorkspace:
		return configuration.workspace
	case ScopeTask:
		return configuration.task
	default:
		return Selection{}
	}
}

// Envelope is the immutable human-approved inclusive value interval.
type Envelope struct {
	minimum int
	maximum int
	set     bool
}

// NewEnvelope creates one explicit inclusive security envelope.
func NewEnvelope(minimum, maximum int) Envelope {
	return Envelope{minimum: minimum, maximum: maximum, set: true}
}

// Minimum returns the lower inclusive bound.
func (envelope Envelope) Minimum() int {
	return envelope.minimum
}

// Maximum returns the upper inclusive bound.
func (envelope Envelope) Maximum() int {
	return envelope.maximum
}

func (envelope Envelope) valid() bool {
	return envelope.set && envelope.minimum <= envelope.maximum
}

func (envelope Envelope) contains(value int) bool {
	return envelope.valid() && value >= envelope.minimum && value <= envelope.maximum
}

type confirmation struct {
	present   bool
	actorKind ActorKind
	actorID   string
	revision  string
	confirmed bool
}

func (value confirmation) isHuman() bool {
	return value.present && value.actorKind == ActorHuman && value.actorID != "" && value.confirmed
}

type oneOffOverride struct {
	present   bool
	actorKind ActorKind
	actorID   string
	revision  string
	scope     Scope
	value     int
	confirmed bool
	audited   bool
}

func (override oneOffOverride) admits(revision string, scope Scope, value int) bool {
	return override.present && override.actorKind == ActorHuman && override.actorID != "" &&
		override.revision == revision && override.scope == scope && override.value == value &&
		override.confirmed && override.audited
}

// Facts contains one closed immutable oracle input. Every With method returns
// a new value so a Preview or Run snapshot cannot observe later caller edits.
type Facts struct {
	envelope Envelope

	hasActive           bool
	activeRevision      string
	activeConfiguration Configuration

	hasProposal           bool
	proposalRevision      string
	proposalConfiguration Configuration
	proposalValid         bool

	approval        confirmation
	acknowledgement confirmation
	oneOff          oneOffOverride
}

// NewFacts creates empty revision state inside a frozen security envelope.
func NewFacts(envelope Envelope) Facts {
	return Facts{envelope: envelope}
}

// WithActive returns a copy with one active revision and configuration.
func (facts Facts) WithActive(revision string, configuration Configuration) Facts {
	facts.hasActive = true
	facts.activeRevision = revision
	facts.activeConfiguration = configuration
	return facts
}

// WithProposal returns a copy with the current pending proposal.
func (facts Facts) WithProposal(revision string, configuration Configuration, valid bool) Facts {
	facts.hasProposal = true
	facts.proposalRevision = revision
	facts.proposalConfiguration = configuration
	facts.proposalValid = valid
	return facts
}

// WithApproval binds an Apply confirmation to an exact proposed revision.
func (facts Facts) WithApproval(kind ActorKind, actorID, revision string, confirmed bool) Facts {
	facts.approval = confirmation{
		present: true, actorKind: kind, actorID: actorID, revision: revision, confirmed: confirmed,
	}
	return facts
}

// WithAcknowledgement binds the human's acknowledgement to the active
// revision shown by Preview. The empty revision is the exact initial-state
// acknowledgement when no revision is active yet.
func (facts Facts) WithAcknowledgement(kind ActorKind, actorID, revision string, confirmed bool) Facts {
	facts.acknowledgement = confirmation{
		present: true, actorKind: kind, actorID: actorID, revision: revision, confirmed: confirmed,
	}
	return facts
}

// WithOneOffOverride returns a copy containing a separately confirmed and
// audited override. The override never changes the durable Envelope.
func (facts Facts) WithOneOffOverride(
	kind ActorKind,
	actorID, revision string,
	scope Scope,
	value int,
	confirmed, audited bool,
) Facts {
	facts.oneOff = oneOffOverride{
		present: true, actorKind: kind, actorID: actorID, revision: revision,
		scope: scope, value: value, confirmed: confirmed, audited: audited,
	}
	return facts
}

// ActiveRevision returns the current active revision, if any.
func (facts Facts) ActiveRevision() (string, bool) {
	return facts.activeRevision, facts.hasActive
}

// SecurityEnvelope returns the immutable envelope carried by the facts.
func (facts Facts) SecurityEnvelope() Envelope {
	return facts.envelope
}

// Effective is one deterministic value/source or a closed refusal.
type Effective struct {
	value           int
	source          Scope
	refusal         RefusalCode
	oneOffConfirmed bool
}

// Value returns the selected value. It is meaningful when Allowed is true.
func (effective Effective) Value() int {
	return effective.value
}

// Source returns the scope which supplied the selected value.
func (effective Effective) Source() Scope {
	return effective.source
}

// Refusal returns the closed refusal code.
func (effective Effective) Refusal() RefusalCode {
	return effective.refusal
}

// Allowed reports whether all required facts admit the effective value.
func (effective Effective) Allowed() bool {
	return effective.refusal == RefusalNone
}

// UsedOneOffOverride reports whether a separately audited one-off confirmation
// admitted this exact Task value.
func (effective Effective) UsedOneOffOverride() bool {
	return effective.oneOffConfirmed
}

func refused(code RefusalCode) Effective {
	return Effective{source: ScopeNone, refusal: code}
}

func resolve(
	configuration Configuration,
	envelope Envelope,
	revision string,
	override oneOffOverride,
) Effective {
	if !envelope.valid() {
		return refused(RefusalInvalidSecurityEnvelope)
	}
	for _, scope := range scopeOrder {
		if !configuration.Selection(scope).valid() {
			return refused(RefusalInvalidSelection)
		}
	}
	if configuration.project.kind != SelectionValue {
		return refused(RefusalProjectValueRequired)
	}

	selected := configuration.project
	source := ScopeProject
	if configuration.workspace.kind == SelectionValue {
		selected = configuration.workspace
		source = ScopeWorkspace
	}
	if configuration.task.kind == SelectionValue {
		selected = configuration.task
		source = ScopeTask
	}

	for _, scope := range []Scope{ScopeProject, ScopeWorkspace} {
		selection := configuration.Selection(scope)
		if selection.kind == SelectionValue && !envelope.contains(selection.value) {
			return Effective{value: selected.value, source: source, refusal: RefusalOutsideSecurityEnvelope}
		}
	}
	if configuration.task.kind == SelectionValue && !envelope.contains(configuration.task.value) {
		if override.admits(revision, ScopeTask, configuration.task.value) {
			return Effective{
				value: selected.value, source: source, refusal: RefusalNone, oneOffConfirmed: true,
			}
		}
		code := RefusalOutsideSecurityEnvelope
		if override.present {
			code = RefusalOneOffOverrideInvalid
		}
		return Effective{value: selected.value, source: source, refusal: code}
	}
	return Effective{value: selected.value, source: source, refusal: RefusalNone}
}

// Resolve computes inheritance directly without revision activation. It is
// useful for exhaustive precedence matrices which do not exercise Apply.
func Resolve(configuration Configuration, envelope Envelope) Effective {
	return resolve(configuration, envelope, "", oneOffOverride{})
}

// DiffEntry records one changed raw scope selection in fixed precedence order.
type DiffEntry struct {
	scope  Scope
	before Selection
	after  Selection
}

// Scope returns the changed scope.
func (entry DiffEntry) Scope() Scope {
	return entry.scope
}

// Before returns the active selection shown by Preview.
func (entry DiffEntry) Before() Selection {
	return entry.before
}

// After returns the proposed selection shown by Preview.
func (entry DiffEntry) After() Selection {
	return entry.after
}

func selectionEqual(left, right Selection) bool {
	return left.kind == right.kind && left.value == right.value
}

func configurationEqual(left, right Configuration) bool {
	return selectionEqual(left.project, right.project) &&
		selectionEqual(left.workspace, right.workspace) &&
		selectionEqual(left.task, right.task)
}

func configurationDiff(before, after Configuration, hasBefore bool) []DiffEntry {
	entries := make([]DiffEntry, 0, len(scopeOrder))
	for _, scope := range scopeOrder {
		beforeSelection := Inherit()
		if hasBefore {
			beforeSelection = before.Selection(scope)
		}
		afterSelection := after.Selection(scope)
		if !hasBefore || !selectionEqual(beforeSelection, afterSelection) {
			entries = append(entries, DiffEntry{scope: scope, before: beforeSelection, after: afterSelection})
		}
	}
	return entries
}

// Preview is an immutable comparison and exact Apply binding. Its private
// copies are deliberately independent of production Preview representations.
type Preview struct {
	activeRevision        string
	hasActive             bool
	activeConfiguration   Configuration
	proposalRevision      string
	proposalConfiguration Configuration
	proposalValid         bool
	envelope              Envelope
	oneOff                oneOffOverride
	effectiveBefore       Effective
	effectiveAfter        Effective
	diff                  []DiffEntry
	refusal               RefusalCode
}

// ActiveRevision returns the revision against which this Preview was made.
func (preview Preview) ActiveRevision() (string, bool) {
	return preview.activeRevision, preview.hasActive
}

// ProposedRevision returns the exact proposed revision.
func (preview Preview) ProposedRevision() string {
	return preview.proposalRevision
}

// EffectiveBefore returns the active effective value or its refusal.
func (preview Preview) EffectiveBefore() Effective {
	return preview.effectiveBefore
}

// EffectiveAfter returns the proposed effective value or its refusal.
func (preview Preview) EffectiveAfter() Effective {
	return preview.effectiveAfter
}

// Diff returns a defensive copy in Project, Workspace, Task order.
func (preview Preview) Diff() []DiffEntry {
	return append([]DiffEntry(nil), preview.diff...)
}

// Refusal returns the closed Preview refusal.
func (preview Preview) Refusal() RefusalCode {
	return preview.refusal
}

// Valid reports whether this exact Preview may proceed to Apply admission.
func (preview Preview) Valid() bool {
	return preview.refusal == RefusalNone
}

// PreviewConfiguration computes impact without mutating active state.
func PreviewConfiguration(facts Facts) Preview {
	preview := Preview{
		activeRevision: facts.activeRevision, hasActive: facts.hasActive,
		activeConfiguration:   facts.activeConfiguration,
		proposalRevision:      facts.proposalRevision,
		proposalConfiguration: facts.proposalConfiguration,
		proposalValid:         facts.proposalValid,
		envelope:              facts.envelope, oneOff: facts.oneOff,
		refusal: RefusalNone,
	}
	if facts.hasActive {
		if facts.activeRevision == "" {
			preview.effectiveBefore = refused(RefusalActiveRevisionInvalid)
			preview.refusal = RefusalActiveRevisionInvalid
		} else {
			preview.effectiveBefore = resolve(
				facts.activeConfiguration, facts.envelope, facts.activeRevision, facts.oneOff,
			)
		}
	} else {
		preview.effectiveBefore = refused(RefusalActiveRevisionRequired)
	}
	if !facts.hasProposal {
		preview.effectiveAfter = refused(RefusalProposalRequired)
		preview.refusal = RefusalProposalRequired
		return preview
	}
	if facts.proposalRevision == "" {
		preview.effectiveAfter = refused(RefusalProposalRevisionInvalid)
		preview.refusal = RefusalProposalRevisionInvalid
		return preview
	}
	preview.diff = configurationDiff(
		facts.activeConfiguration, facts.proposalConfiguration, facts.hasActive,
	)
	if !facts.proposalValid {
		preview.effectiveAfter = refused(RefusalProposalInvalid)
		preview.refusal = RefusalProposalInvalid
		return preview
	}
	preview.effectiveAfter = resolve(
		facts.proposalConfiguration, facts.envelope, facts.proposalRevision, facts.oneOff,
	)
	if preview.refusal == RefusalNone && !preview.effectiveAfter.Allowed() {
		preview.refusal = preview.effectiveAfter.Refusal()
	}
	return preview
}

func oneOffEqual(left, right oneOffOverride) bool {
	return left.present == right.present && left.actorKind == right.actorKind &&
		left.actorID == right.actorID && left.revision == right.revision &&
		left.scope == right.scope && left.value == right.value &&
		left.confirmed == right.confirmed && left.audited == right.audited
}

func previewCurrent(facts Facts, preview Preview) bool {
	return preview.hasActive == facts.hasActive && preview.activeRevision == facts.activeRevision &&
		configurationEqual(preview.activeConfiguration, facts.activeConfiguration) &&
		preview.proposalRevision == facts.proposalRevision &&
		configurationEqual(preview.proposalConfiguration, facts.proposalConfiguration) &&
		preview.proposalValid == facts.proposalValid && preview.envelope == facts.envelope &&
		oneOffEqual(preview.oneOff, facts.oneOff)
}

// ApplyResult records only the attempted activation outcome. Apply never
// changes the security envelope.
type ApplyResult struct {
	activeRevision string
	envelope       Envelope
	refusal        RefusalCode
}

// ActiveRevision returns the active revision after the attempt.
func (result ApplyResult) ActiveRevision() string {
	return result.activeRevision
}

// SecurityEnvelope returns the unchanged durable envelope.
func (result ApplyResult) SecurityEnvelope() Envelope {
	return result.envelope
}

// Refusal returns the closed Apply refusal.
func (result ApplyResult) Refusal() RefusalCode {
	return result.refusal
}

// Applied reports whether the exact proposal became active.
func (result ApplyResult) Applied() bool {
	return result.refusal == RefusalNone
}

func applyRefusal(facts Facts, code RefusalCode) (Facts, ApplyResult) {
	return facts, ApplyResult{
		activeRevision: facts.activeRevision,
		envelope:       facts.envelope,
		refusal:        code,
	}
}

// ApplyConfiguration activates only the exact current valid Preview with a
// confirmed human proposal approval and exact active-revision acknowledgement.
func ApplyConfiguration(facts Facts, preview Preview) (Facts, ApplyResult) {
	if preview.proposalRevision == "" {
		return applyRefusal(facts, RefusalPreviewRequired)
	}
	if preview.refusal != RefusalNone {
		return applyRefusal(facts, preview.refusal)
	}
	if !previewCurrent(facts, preview) {
		return applyRefusal(facts, RefusalPreviewStale)
	}
	if !facts.approval.present {
		return applyRefusal(facts, RefusalHumanApprovalRequired)
	}
	if !facts.approval.isHuman() {
		return applyRefusal(facts, RefusalHumanApprovalInvalid)
	}
	if facts.approval.revision != facts.proposalRevision {
		return applyRefusal(facts, RefusalApprovalRevisionMismatch)
	}
	if !facts.acknowledgement.present {
		return applyRefusal(facts, RefusalHumanAcknowledgementRequired)
	}
	if !facts.acknowledgement.isHuman() {
		return applyRefusal(facts, RefusalHumanAcknowledgementInvalid)
	}
	if facts.acknowledgement.revision != facts.activeRevision {
		return applyRefusal(facts, RefusalAcknowledgementRevisionMismatch)
	}

	next := facts
	next.hasActive = true
	next.activeRevision = facts.proposalRevision
	next.activeConfiguration = facts.proposalConfiguration
	next.hasProposal = false
	next.proposalRevision = ""
	next.proposalConfiguration = Configuration{}
	next.proposalValid = false
	next.approval = confirmation{}
	next.acknowledgement = confirmation{}
	return next, ApplyResult{
		activeRevision: next.activeRevision,
		envelope:       next.envelope,
		refusal:        RefusalNone,
	}
}

// RunSnapshot is an immutable active-revision and effective-value fact for one
// Run. Later Preview or Apply calls cannot alter it.
type RunSnapshot struct {
	activeRevision string
	configuration  Configuration
	envelope       Envelope
	effective      Effective
}

// ActiveRevision returns the exact revision frozen for this Run.
func (snapshot RunSnapshot) ActiveRevision() string {
	return snapshot.activeRevision
}

// Configuration returns the frozen selection tuple.
func (snapshot RunSnapshot) Configuration() Configuration {
	return snapshot.configuration
}

// SecurityEnvelope returns the unchanged durable envelope used for admission.
func (snapshot RunSnapshot) SecurityEnvelope() Envelope {
	return snapshot.envelope
}

// Effective returns the frozen effective value/source or refusal.
func (snapshot RunSnapshot) Effective() Effective {
	return snapshot.effective
}

// FreezeRun uses only the active revision. A successfully used one-off
// confirmation is consumed in the returned Facts and cannot admit a second
// Run. The original Facts value remains unchanged and repeatable.
func FreezeRun(facts Facts) (Facts, RunSnapshot) {
	snapshot := RunSnapshot{
		activeRevision: facts.activeRevision,
		configuration:  facts.activeConfiguration,
		envelope:       facts.envelope,
	}
	if !facts.hasActive {
		snapshot.effective = refused(RefusalActiveRevisionRequired)
		return facts, snapshot
	}
	if facts.activeRevision == "" {
		snapshot.effective = refused(RefusalActiveRevisionInvalid)
		return facts, snapshot
	}
	snapshot.effective = resolve(
		facts.activeConfiguration, facts.envelope, facts.activeRevision, facts.oneOff,
	)
	if !snapshot.effective.Allowed() || !snapshot.effective.UsedOneOffOverride() {
		return facts, snapshot
	}
	next := facts
	next.oneOff = oneOffOverride{}
	return next, snapshot
}
