// SPDX-License-Identifier: Apache-2.0

// Package configuration owns the typed Preview/Apply boundary for Organizer
// revisions and the frozen configuration snapshot embedded in a Run. These
// state transitions are engine-only application behavior.
package configuration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/jsondocument"
)

const (
	PreviewSchemaVersion  = "director.configuration-preview/v1"
	SnapshotSchemaVersion = "director.run-configuration-snapshot/v1"
)

var (
	ErrVersionConflict         = errors.New("Organizer revision state version conflict")
	ErrRevisionInvalid         = errors.New("Organizer revision must be a lowercase full Git object ID")
	ErrPendingRevisionMissing  = errors.New("pending Organizer revision is required")
	ErrPreviewMismatch         = errors.New("Apply must name the exact current Preview")
	ErrHumanApprovalRequired   = errors.New("Apply requires a confirmed server-authenticated human actor")
	ErrPendingRevisionInvalid  = errors.New("invalid pending Organizer revision cannot be applied")
	ErrRevisionContentConflict = errors.New("active Organizer revision cannot identify different configuration content")
	ErrActiveRevisionMissing   = errors.New("an active Organizer revision is required to freeze a Run")
	ErrSnapshotInvalid         = errors.New("Run configuration snapshot is invalid")
	revisionPattern            = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	sha256Pattern              = regexp.MustCompile(`^[0-9a-f]{64}$`)
	configurationSectionOrder  = []string{"project", "workspaces", "agentProfiles", "defaults", "workspaceOverrides", "skills", "templates"}
)

// ImpactKind is the closed high-level result of comparing a pending revision
// with the active revision.
type ImpactKind string

const (
	ImpactInvalid               ImpactKind = "invalid"
	ImpactInitialActivation     ImpactKind = "initial_activation"
	ImpactConfigurationChange   ImpactKind = "configuration_change"
	ImpactNoConfigurationChange ImpactKind = "no_configuration_change"
)

// Impact is a deterministic top-level semantic comparison. A future Git
// adapter may project an exact textual diff without changing Apply admission.
type Impact struct {
	Kind            ImpactKind `json:"kind"`
	ChangedSections []string   `json:"changedSections"`
}

// Preview is the policy-free projection rendered by a host. Its ID binds the
// proposed revision, validated content, current active revision, aggregate
// version, issues, and impact.
type Preview struct {
	SchemaVersion             string               `json:"schemaVersion"`
	ID                        string               `json:"id"`
	AggregateVersion          uint64               `json:"aggregateVersion"`
	ActiveRevision            string               `json:"activeRevision,omitempty"`
	ActiveConfigurationSHA256 string               `json:"activeConfigurationSha256,omitempty"`
	ProposedRevision          string               `json:"proposedRevision"`
	ContentSHA256             string               `json:"contentSha256"`
	ConfigurationSHA256       string               `json:"configurationSha256,omitempty"`
	Valid                     bool                 `json:"valid"`
	Issues                    []domainconfig.Issue `json:"issues"`
	Impact                    Impact               `json:"impact"`
}

func clonePreview(value Preview) Preview {
	value.Issues = slices.Clone(value.Issues)
	value.Impact.ChangedSections = slices.Clone(value.Impact.ChangedSections)
	return value
}

// ActiveRevision is the bounded projection of the currently approved
// Organizer revision. Configuration bytes remain encapsulated by the engine.
type ActiveRevision struct {
	OrganizerRevision   string `json:"organizerRevision"`
	ConfigurationSHA256 string `json:"configurationSha256"`
	SchemaVersion       int    `json:"schemaVersion"`
}

type storedRevision struct {
	revision string
	document domainconfig.Document
}

type pendingRevision struct {
	preview  Preview
	document *domainconfig.Document
}

// State is the optimistic-concurrency aggregate for one Project's active and
// pending Organizer revisions. Its zero value is an empty version-0 state.
type State struct {
	version uint64
	active  *storedRevision
	pending *pendingRevision
}

// Version returns the aggregate version required by the next mutation.
func (state State) Version() uint64 {
	return state.version
}

// Active returns the approved revision projection, if one exists.
func (state State) Active() (ActiveRevision, bool) {
	if state.active == nil {
		return ActiveRevision{}, false
	}
	return ActiveRevision{
		OrganizerRevision:   state.active.revision,
		ConfigurationSHA256: state.active.document.SHA256(),
		SchemaVersion:       domainconfig.SchemaVersion,
	}, true
}

// Pending returns the latest previewed revision projection, whether valid or
// invalid. A pending projection is never an active Run input.
func (state State) Pending() (Preview, bool) {
	if state.pending == nil {
		return Preview{}, false
	}
	return clonePreview(state.pending.preview), true
}

// PreviewCommand proposes the paseo-director.json content found at an exact
// Organizer Git revision.
type PreviewCommand struct {
	ExpectedVersion   uint64
	OrganizerRevision string
	ConfigurationJSON []byte
}

func validRevision(value string) bool {
	return revisionPattern.MatchString(value)
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validHumanActor(value string) bool {
	return value == strings.TrimSpace(value) && len(value) > 0 && len(value) <= 256 &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func changedSections(active, pending domainconfig.Configuration) []string {
	changed := make([]string, 0, len(configurationSectionOrder))
	if !reflect.DeepEqual(active.Project, pending.Project) {
		changed = append(changed, "project")
	}
	if !reflect.DeepEqual(active.Workspaces, pending.Workspaces) {
		changed = append(changed, "workspaces")
	}
	if !reflect.DeepEqual(active.AgentProfiles, pending.AgentProfiles) {
		changed = append(changed, "agentProfiles")
	}
	if !reflect.DeepEqual(active.Defaults, pending.Defaults) {
		changed = append(changed, "defaults")
	}
	if !reflect.DeepEqual(active.WorkspaceOverrides, pending.WorkspaceOverrides) {
		changed = append(changed, "workspaceOverrides")
	}
	if !reflect.DeepEqual(active.Skills, pending.Skills) {
		changed = append(changed, "skills")
	}
	if !reflect.DeepEqual(active.Templates, pending.Templates) {
		changed = append(changed, "templates")
	}
	return changed
}

func previewImpact(active *storedRevision, pending domainconfig.Document) Impact {
	if active == nil {
		return Impact{Kind: ImpactInitialActivation, ChangedSections: slices.Clone(configurationSectionOrder)}
	}
	changed := changedSections(active.document.Configuration(), pending.Configuration())
	if len(changed) == 0 {
		return Impact{Kind: ImpactNoConfigurationChange, ChangedSections: []string{}}
	}
	return Impact{Kind: ImpactConfigurationChange, ChangedSections: changed}
}

func assignPreviewID(preview *Preview) error {
	payload := struct {
		SchemaVersion             string               `json:"schemaVersion"`
		PriorAggregateVersion     uint64               `json:"priorAggregateVersion"`
		ActiveRevision            string               `json:"activeRevision"`
		ActiveConfigurationSHA256 string               `json:"activeConfigurationSha256"`
		ProposedRevision          string               `json:"proposedRevision"`
		ContentSHA256             string               `json:"contentSha256"`
		ConfigurationSHA256       string               `json:"configurationSha256"`
		Valid                     bool                 `json:"valid"`
		Issues                    []domainconfig.Issue `json:"issues"`
		Impact                    Impact               `json:"impact"`
	}{
		SchemaVersion:             preview.SchemaVersion,
		PriorAggregateVersion:     preview.AggregateVersion - 1,
		ActiveRevision:            preview.ActiveRevision,
		ActiveConfigurationSHA256: preview.ActiveConfigurationSHA256,
		ProposedRevision:          preview.ProposedRevision,
		ContentSHA256:             preview.ContentSHA256,
		ConfigurationSHA256:       preview.ConfigurationSHA256,
		Valid:                     preview.Valid,
		Issues:                    preview.Issues,
		Impact:                    preview.Impact,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal deterministic configuration preview: %w", err)
	}
	preview.ID = hashBytes(encoded)
	return nil
}

// Preview validates and stages an Organizer revision without changing the
// active revision. Invalid configuration becomes a visible invalid pending
// revision so it cannot be mistaken for absence or activation.
func (state State) Preview(command PreviewCommand) (State, Preview, error) {
	if command.ExpectedVersion != state.version {
		return state, Preview{}, ErrVersionConflict
	}
	if !validRevision(command.OrganizerRevision) {
		return state, Preview{}, ErrRevisionInvalid
	}
	preview := Preview{
		SchemaVersion:    PreviewSchemaVersion,
		AggregateVersion: state.version + 1,
		ProposedRevision: command.OrganizerRevision,
		Issues:           []domainconfig.Issue{},
	}
	if state.active != nil {
		preview.ActiveRevision = state.active.revision
		preview.ActiveConfigurationSHA256 = state.active.document.SHA256()
	}
	configurationJSON := slices.Clone(command.ConfigurationJSON)
	document, err := domainconfig.Parse(configurationJSON)
	var pendingDocument *domainconfig.Document
	if err != nil {
		issues, ok := domainconfig.ValidationIssues(err)
		if !ok {
			return state, Preview{}, fmt.Errorf("validate Organizer configuration: %w", err)
		}
		preview.ContentSHA256 = hashBytes(configurationJSON)
		preview.Valid = false
		preview.Issues = issues
		preview.Impact = Impact{Kind: ImpactInvalid, ChangedSections: []string{}}
	} else {
		if state.active != nil && state.active.revision == command.OrganizerRevision && state.active.document.SHA256() != document.SHA256() {
			return state, Preview{}, ErrRevisionContentConflict
		}
		preview.ContentSHA256 = document.SHA256()
		preview.ConfigurationSHA256 = document.SHA256()
		preview.Valid = true
		preview.Impact = previewImpact(state.active, document)
		pendingDocument = &document
	}
	if err := assignPreviewID(&preview); err != nil {
		return state, Preview{}, err
	}
	next := state
	next.version = preview.AggregateVersion
	next.pending = &pendingRevision{preview: clonePreview(preview), document: pendingDocument}
	return next, clonePreview(preview), nil
}

// HumanConfirmation is populated only after the engine boundary authenticates
// a human Apply action. Model claims and host narration are not confirmation.
type HumanConfirmation struct {
	ActorID   string
	Confirmed bool
}

// ApplyCommand confirms the exact current Preview under optimistic
// concurrency.
type ApplyCommand struct {
	ExpectedVersion uint64
	PreviewID       string
	Confirmation    HumanConfirmation
}

// Apply activates the exact valid pending revision. It never reparses caller-
// supplied configuration and clears the pending revision only after success.
func (state State) Apply(command ApplyCommand) (State, error) {
	if command.ExpectedVersion != state.version {
		return state, ErrVersionConflict
	}
	if state.pending == nil {
		return state, ErrPendingRevisionMissing
	}
	if command.PreviewID == "" || command.PreviewID != state.pending.preview.ID {
		return state, ErrPreviewMismatch
	}
	if !command.Confirmation.Confirmed || !validHumanActor(command.Confirmation.ActorID) {
		return state, ErrHumanApprovalRequired
	}
	if !state.pending.preview.Valid || state.pending.document == nil {
		return state, ErrPendingRevisionInvalid
	}
	next := state
	next.version++
	next.active = &storedRevision{
		revision: state.pending.preview.ProposedRevision,
		document: *state.pending.document,
	}
	next.pending = nil
	return next, nil
}

// RunConfigurationSnapshot is the immutable, serializable configuration input
// for one Run. Accessors return copies, and later Organizer changes cannot
// mutate it.
type RunConfigurationSnapshot struct {
	organizerRevision string
	document          domainconfig.Document
}

type snapshotWire struct {
	SnapshotVersion     string          `json:"snapshotVersion"`
	OrganizerRevision   string          `json:"organizerRevision"`
	ConfigurationSHA256 string          `json:"configurationSha256"`
	Configuration       json.RawMessage `json:"configuration"`
}

// OrganizerRevision returns the exact approved Organizer Git revision frozen
// for the Run.
func (snapshot RunConfigurationSnapshot) OrganizerRevision() string {
	return snapshot.organizerRevision
}

// ConfigurationSHA256 returns the canonical configuration identity frozen for
// the Run.
func (snapshot RunConfigurationSnapshot) ConfigurationSHA256() string {
	return snapshot.document.SHA256()
}

// ConfigurationJSON returns defensive canonical configuration bytes.
func (snapshot RunConfigurationSnapshot) ConfigurationJSON() []byte {
	return snapshot.document.CanonicalJSON()
}

// Configuration returns a deep copy of the frozen typed configuration.
func (snapshot RunConfigurationSnapshot) Configuration() domainconfig.Configuration {
	return snapshot.document.Configuration()
}

// MarshalJSON emits the complete frozen snapshot with a verified content hash.
func (snapshot RunConfigurationSnapshot) MarshalJSON() ([]byte, error) {
	if !validRevision(snapshot.organizerRevision) || snapshot.document.SHA256() == "" {
		return nil, ErrSnapshotInvalid
	}
	return json.Marshal(snapshotWire{
		SnapshotVersion:     SnapshotSchemaVersion,
		OrganizerRevision:   snapshot.organizerRevision,
		ConfigurationSHA256: snapshot.document.SHA256(),
		Configuration:       snapshot.document.CanonicalJSON(),
	})
}

// ParseRunConfigurationSnapshot strictly reconstructs a persisted snapshot and
// proves its schema version, revision identity, configuration validity, and
// canonical content hash.
func ParseRunConfigurationSnapshot(input []byte) (RunConfigurationSnapshot, error) {
	if len(input) == 0 || len(input) > domainconfig.MaximumDocumentBytes+4096 {
		return RunConfigurationSnapshot{}, ErrSnapshotInvalid
	}
	canonical, err := jsondocument.Canonical(input)
	if err != nil {
		return RunConfigurationSnapshot{}, ErrSnapshotInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var wire snapshotWire
	if err := decoder.Decode(&wire); err != nil {
		return RunConfigurationSnapshot{}, ErrSnapshotInvalid
	}
	if wire.SnapshotVersion != SnapshotSchemaVersion || !validRevision(wire.OrganizerRevision) ||
		!sha256Pattern.MatchString(wire.ConfigurationSHA256) {
		return RunConfigurationSnapshot{}, ErrSnapshotInvalid
	}
	document, err := domainconfig.Parse(wire.Configuration)
	if err != nil || document.SHA256() != wire.ConfigurationSHA256 {
		return RunConfigurationSnapshot{}, ErrSnapshotInvalid
	}
	return RunConfigurationSnapshot{organizerRevision: wire.OrganizerRevision, document: document}, nil
}

// FreezeRunConfiguration captures only the approved active revision. A valid
// or invalid pending revision is deliberately ignored.
func (state State) FreezeRunConfiguration() (RunConfigurationSnapshot, error) {
	if state.active == nil {
		return RunConfigurationSnapshot{}, ErrActiveRevisionMissing
	}
	return RunConfigurationSnapshot{
		organizerRevision: state.active.revision,
		document:          state.active.document,
	}, nil
}
