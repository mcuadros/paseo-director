// SPDX-License-Identifier: Apache-2.0

// Package feedback owns the immutable, exact-context human-feedback model.
// It has no Paseo, GitHub, TaskStore, prompting, or planning side effects.
package feedback

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/safedata"
)

const (
	BindingSchemaVersion  = "director.feedback-binding/v1"
	SnapshotSchemaVersion = "director.feedback-snapshot/v1"
	RecordSchemaVersion   = "director.feedback-record/v1"
	StateSchemaVersion    = "director.feedback-state/v1"
	MaximumPageSize       = uint32(100)
	MaximumPages          = uint32(10)
	MaximumSnapshots      = 128
	MaximumRecords        = 1024
	MaximumBodyBytes      = 16 * 1024
	MaximumSummaryBytes   = 1024
	MaximumObservationAge = int64(30_000)
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	loginPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/\[\]-]{0,255}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type Source string

const (
	SourcePaseoDirect         Source = "paseo_direct"
	SourceGitHubReview        Source = "github_review"
	SourceGitHubReviewComment Source = "github_review_comment"
	SourceGitHubIssueComment  Source = "github_issue_comment"
)

var sources = []Source{SourcePaseoDirect, SourceGitHubReview, SourceGitHubReviewComment, SourceGitHubIssueComment}

type ActorKind string

const (
	ActorHuman ActorKind = "human"
	ActorBot   ActorKind = "bot"
	ActorApp   ActorKind = "app"
)

const (
	AttestationPaseoHuman = "paseo.authenticated-human/v1"
	AttestationGitHubUser = "github.account-type-user/v1"
	AttestationGitHubBot  = "github.account-type-bot/v1"
	AttestationGitHubApp  = "github.performed-via-app/v1"
)

type Kind string

const (
	KindComment          Kind = "comment"
	KindChangesRequested Kind = "changes_requested"
	KindApproved         Kind = "approved"
	KindDismissed        Kind = "dismissed"
)

type ContextStatus string

const (
	ContextCurrent   ContextStatus = "current"
	ContextStale     ContextStatus = "stale"
	ContextAmbiguous ContextStatus = "ambiguous"
)

type Disposition string

const (
	DispositionActionable      Disposition = "actionable"
	DispositionInformational   Disposition = "informational"
	DispositionDeleted         Disposition = "deleted"
	DispositionIgnoredNonHuman Disposition = "ignored_non_human"
	DispositionHistorical      Disposition = "historical_context"
	DispositionNeedsDecision   Disposition = "needs_human_decision"
)

type Phase string

const (
	PhaseObserved         Phase = "observed"
	PhaseCorrectionReady  Phase = "correction_ready"
	PhaseCorrectionRouted Phase = "correction_routed"
	PhaseNeedsYou         Phase = "needs_you"
)

type Actor struct {
	Kind          ActorKind `json:"kind"`
	ID            string    `json:"id"`
	Login         string    `json:"login"`
	Authenticated bool      `json:"authenticated"`
	Attestation   string    `json:"attestation"`
}

// Binding fixes feedback to the exact current Candidate and, for GitHub,
// the exact owned pull request. A comment can never be rebound in place.
type Binding struct {
	SchemaVersion       string `json:"schemaVersion"`
	ProjectID           string `json:"projectId"`
	WorkspaceID         string `json:"workspaceId"`
	TaskID              string `json:"taskId"`
	TaskVersion         uint64 `json:"taskVersion"`
	RunID               string `json:"runId"`
	CandidateID         string `json:"candidateId"`
	CandidateSHA        string `json:"candidateSha"`
	BaseSHA             string `json:"baseSha"`
	CandidateGeneration uint64 `json:"candidateGeneration"`
	ManifestSHA256      string `json:"manifestSha256"`
	RepositoryID        int64  `json:"repositoryId,omitempty"`
	RepositoryNodeID    string `json:"repositoryNodeId,omitempty"`
	PullRequestNumber   int64  `json:"pullRequestNumber,omitempty"`
	BindingSHA256       string `json:"bindingSha256"`
}

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed feedback value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func DigestText(value string) string { return digest(value) }

func bindingValue(value Binding) Binding { value.BindingSHA256 = ""; return value }

func SealBinding(value Binding) Binding {
	value.SchemaVersion = BindingSchemaVersion
	value.BindingSHA256 = digest(bindingValue(value))
	return value
}

func ValidBinding(value Binding) bool {
	github := value.RepositoryID > 0 || value.RepositoryNodeID != "" || value.PullRequestNumber > 0
	return value.SchemaVersion == BindingSchemaVersion && identifierPattern.MatchString(value.ProjectID) &&
		identifierPattern.MatchString(value.WorkspaceID) && identifierPattern.MatchString(value.TaskID) && value.TaskVersion > 0 &&
		identifierPattern.MatchString(value.RunID) && identifierPattern.MatchString(value.CandidateID) &&
		gitOIDPattern.MatchString(value.CandidateSHA) && gitOIDPattern.MatchString(value.BaseSHA) &&
		len(value.CandidateSHA) == len(value.BaseSHA) && value.CandidateGeneration > 0 && digestPattern.MatchString(value.ManifestSHA256) &&
		(!github || value.RepositoryID > 0 && identifierPattern.MatchString(value.RepositoryNodeID) && value.PullRequestNumber > 0) &&
		digestPattern.MatchString(value.BindingSHA256) && value.BindingSHA256 == digest(bindingValue(value))
}

// Item is an ephemeral connector observation. Body is normalized and redacted
// before persistence; no raw comment body exists in State or audit Events.
type Item struct {
	Source                Source              `json:"source"`
	ExternalID            string              `json:"externalId"`
	RevisionID            string              `json:"revisionId"`
	Actor                 Actor               `json:"actor"`
	Kind                  Kind                `json:"kind"`
	CandidateSHA          string              `json:"candidateSha,omitempty"`
	BaseSHA               string              `json:"baseSha,omitempty"`
	ContextSHA256         string              `json:"contextSha256"`
	Body                  string              `json:"body,omitempty"`
	Actionable            bool                `json:"actionable"`
	Severity              correction.Severity `json:"severity"`
	RequiresHumanDecision bool                `json:"requiresHumanDecision"`
	CreatedAtMillis       int64               `json:"createdAtMillis"`
	UpdatedAtMillis       int64               `json:"updatedAtMillis"`
	Deleted               bool                `json:"deleted"`
}

type Snapshot struct {
	SchemaVersion    string `json:"schemaVersion"`
	ID               string `json:"id"`
	Source           Source `json:"source"`
	BindingSHA256    string `json:"bindingSha256"`
	Complete         bool   `json:"complete"`
	PageCount        uint32 `json:"pageCount"`
	ObservedAtMillis int64  `json:"observedAtMillis"`
	MaximumAgeMillis int64  `json:"maximumAgeMillis"`
	Items            []Item `json:"items"`
	SHA256           string `json:"sha256"`
}

func snapshotValue(value Snapshot) Snapshot { value.SHA256 = ""; return value }

func SealSnapshot(value Snapshot) Snapshot {
	value.SchemaVersion = SnapshotSchemaVersion
	value.SHA256 = digest(snapshotValue(value))
	return value
}

func validSource(source Source) bool { return slices.Contains(sources, source) }
func sourceRank(source Source) int   { return slices.Index(sources, source) }

func validActor(actor Actor, source Source) bool {
	if !identifierPattern.MatchString(actor.ID) || !loginPattern.MatchString(actor.Login) || !actor.Authenticated {
		return false
	}
	switch actor.Kind {
	case ActorHuman:
		if source == SourcePaseoDirect {
			return actor.Attestation == AttestationPaseoHuman
		}
		return actor.Attestation == AttestationGitHubUser
	case ActorBot:
		return source != SourcePaseoDirect && actor.Attestation == AttestationGitHubBot
	case ActorApp:
		return source != SourcePaseoDirect && actor.Attestation == AttestationGitHubApp
	default:
		return false
	}
}

func validItem(item Item, snapshot Snapshot) bool {
	if item.Source != snapshot.Source || !identifierPattern.MatchString(item.ExternalID) || !identifierPattern.MatchString(item.RevisionID) ||
		!validActor(item.Actor, item.Source) || !digestPattern.MatchString(item.ContextSHA256) || item.CreatedAtMillis < 0 ||
		item.UpdatedAtMillis < item.CreatedAtMillis || item.UpdatedAtMillis > snapshot.ObservedAtMillis ||
		!slices.Contains([]correction.Severity{correction.SeverityP0, correction.SeverityP1, correction.SeverityP2, correction.SeverityP3}, item.Severity) {
		return false
	}
	if item.Deleted {
		return item.Body == "" && !item.Actionable && !item.RequiresHumanDecision && item.Kind == KindDismissed
	}
	if item.Body == "" || len(item.Body) > MaximumBodyBytes || !utf8.ValidString(item.Body) || strings.IndexByte(item.Body, 0) >= 0 {
		return false
	}
	switch item.Source {
	case SourcePaseoDirect:
		return item.Kind == KindComment && gitOIDPattern.MatchString(item.CandidateSHA) && gitOIDPattern.MatchString(item.BaseSHA)
	case SourceGitHubReview:
		return slices.Contains([]Kind{KindComment, KindChangesRequested, KindApproved, KindDismissed}, item.Kind) &&
			gitOIDPattern.MatchString(item.CandidateSHA) && (item.BaseSHA == "" || gitOIDPattern.MatchString(item.BaseSHA))
	case SourceGitHubReviewComment:
		return item.Kind == KindComment && gitOIDPattern.MatchString(item.CandidateSHA) && (item.BaseSHA == "" || gitOIDPattern.MatchString(item.BaseSHA))
	case SourceGitHubIssueComment:
		return item.Kind == KindComment && (item.CandidateSHA == "" || gitOIDPattern.MatchString(item.CandidateSHA)) &&
			(item.BaseSHA == "" || gitOIDPattern.MatchString(item.BaseSHA))
	default:
		return false
	}
}

func ValidSnapshot(value Snapshot, binding Binding, nowMillis int64) bool {
	if !ValidBinding(binding) || value.SchemaVersion != SnapshotSchemaVersion || !identifierPattern.MatchString(value.ID) ||
		!validSource(value.Source) || value.BindingSHA256 != binding.BindingSHA256 || value.PageCount == 0 || value.PageCount > MaximumPages ||
		len(value.Items) > int(MaximumPages*MaximumPageSize) || value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis ||
		value.MaximumAgeMillis <= 0 || value.MaximumAgeMillis > MaximumObservationAge || nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis ||
		!digestPattern.MatchString(value.SHA256) || value.SHA256 != digest(snapshotValue(value)) {
		return false
	}
	if value.Source != SourcePaseoDirect && !value.Complete {
		return false
	}
	seen := make(map[string]string, len(value.Items))
	for _, item := range value.Items {
		if !validItem(item, value) {
			return false
		}
		key := item.SourceKey()
		fingerprint := digest(item)
		if prior, duplicate := seen[key]; duplicate && prior != fingerprint {
			return false
		}
		seen[key] = fingerprint
	}
	return true
}

func (item Item) SourceKey() string {
	return string(item.Source) + ":" + item.ExternalID + ":" + item.RevisionID
}

// SnapshotRecord is the bounded immutable audit of one complete source scan.
type SnapshotRecord struct {
	ID               string `json:"id"`
	Source           Source `json:"source"`
	SHA256           string `json:"sha256"`
	RevisionSHA256   string `json:"revisionSha256"`
	Complete         bool   `json:"complete"`
	PageCount        uint32 `json:"pageCount"`
	ItemCount        uint32 `json:"itemCount"`
	ObservedAtMillis int64  `json:"observedAtMillis"`
	AuditSHA256      string `json:"auditSha256"`
}

type Record struct {
	SchemaVersion         string              `json:"schemaVersion"`
	ID                    string              `json:"id"`
	SnapshotID            string              `json:"snapshotId"`
	Source                Source              `json:"source"`
	ExternalID            string              `json:"externalId"`
	RevisionID            string              `json:"revisionId"`
	Actor                 Actor               `json:"actor"`
	Kind                  Kind                `json:"kind"`
	CandidateSHA          string              `json:"candidateSha,omitempty"`
	BaseSHA               string              `json:"baseSha,omitempty"`
	ContextSHA256         string              `json:"contextSha256"`
	ContextStatus         ContextStatus       `json:"contextStatus"`
	Disposition           Disposition         `json:"disposition"`
	Summary               string              `json:"summary,omitempty"`
	ContentSHA256         string              `json:"contentSha256"`
	Actionable            bool                `json:"actionable"`
	Severity              correction.Severity `json:"severity"`
	RequiresHumanDecision bool                `json:"requiresHumanDecision"`
	CreatedAtMillis       int64               `json:"createdAtMillis"`
	UpdatedAtMillis       int64               `json:"updatedAtMillis"`
	ObservedAtMillis      int64               `json:"observedAtMillis"`
	Deleted               bool                `json:"deleted"`
	ReplacesRecordID      string              `json:"replacesRecordId,omitempty"`
	AuditSHA256           string              `json:"auditSha256"`
}

// RevisionAudit is stable across repeated observations of the same immutable
// source revision. Follow-up creation uses this value so a later full GitHub
// scan cannot duplicate already recorded post-Done work.
type RevisionAudit struct {
	Source                Source              `json:"source"`
	ExternalID            string              `json:"externalId"`
	RevisionID            string              `json:"revisionId"`
	Actor                 Actor               `json:"actor"`
	Kind                  Kind                `json:"kind"`
	CandidateSHA          string              `json:"candidateSha,omitempty"`
	BaseSHA               string              `json:"baseSha,omitempty"`
	ContextSHA256         string              `json:"contextSha256"`
	ContextStatus         ContextStatus       `json:"contextStatus"`
	Disposition           Disposition         `json:"disposition"`
	Summary               string              `json:"summary,omitempty"`
	ContentSHA256         string              `json:"contentSha256"`
	Actionable            bool                `json:"actionable"`
	Severity              correction.Severity `json:"severity"`
	RequiresHumanDecision bool                `json:"requiresHumanDecision"`
	CreatedAtMillis       int64               `json:"createdAtMillis"`
	UpdatedAtMillis       int64               `json:"updatedAtMillis"`
	Deleted               bool                `json:"deleted"`
	SHA256                string              `json:"sha256"`
}

type CurrentRecord struct {
	Source          Source `json:"source"`
	ExternalID      string `json:"externalId"`
	RevisionID      string `json:"revisionId"`
	RecordID        string `json:"recordId"`
	UpdatedAtMillis int64  `json:"updatedAtMillis"`
}

type HumanDecision struct {
	ID                  string `json:"id"`
	ActorID             string `json:"actorId"`
	FeedbackStateSHA256 string `json:"feedbackStateSha256"`
	AllowCorrection     bool   `json:"allowCorrection"`
	DecidedAtMillis     int64  `json:"decidedAtMillis"`
	AuditSHA256         string `json:"auditSha256"`
}

type State struct {
	SchemaVersion      string           `json:"schemaVersion"`
	Binding            Binding          `json:"binding"`
	Snapshots          []SnapshotRecord `json:"snapshots"`
	Records            []Record         `json:"records"`
	Current            []CurrentRecord  `json:"current"`
	Phase              Phase            `json:"phase"`
	CurrentRevision    string           `json:"currentRevision"`
	CorrectionBatchSHA string           `json:"correctionBatchSha,omitempty"`
	NeedsYouCode       string           `json:"needsYouCode,omitempty"`
	WakeCondition      string           `json:"wakeCondition,omitempty"`
	Decision           *HumanDecision   `json:"decision,omitempty"`
	Invalidated        bool             `json:"invalidated"`
	InvalidationCode   string           `json:"invalidationCode,omitempty"`
	SHA256             string           `json:"sha256"`
}

func Redact(body string) (string, bool) {
	if body == "" || len(body) > MaximumBodyBytes || !utf8.ValidString(body) || strings.IndexByte(body, 0) >= 0 {
		return "", false
	}
	value, _ := safedata.Redact(body)
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return "", false
	}
	if len(value) > MaximumSummaryBytes {
		value = strings.TrimSpace(value[:MaximumSummaryBytes])
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value, true
}

func recordValue(value Record) Record { value.AuditSHA256 = ""; return value }
func stateValue(value State) State    { value.SHA256 = ""; return value }

func revisionValue(value Record) Record {
	value.SnapshotID, value.ObservedAtMillis, value.ReplacesRecordID, value.AuditSHA256 = "", 0, "", ""
	return value
}

func Revision(record Record) (RevisionAudit, bool) {
	if !ValidRecord(record) {
		return RevisionAudit{}, false
	}
	value := RevisionAudit{Source: record.Source, ExternalID: record.ExternalID, RevisionID: record.RevisionID,
		Actor: record.Actor, Kind: record.Kind, CandidateSHA: record.CandidateSHA, BaseSHA: record.BaseSHA,
		ContextSHA256: record.ContextSHA256, ContextStatus: record.ContextStatus, Disposition: record.Disposition,
		Summary: record.Summary, ContentSHA256: record.ContentSHA256, Actionable: record.Actionable, Severity: record.Severity,
		RequiresHumanDecision: record.RequiresHumanDecision, CreatedAtMillis: record.CreatedAtMillis,
		UpdatedAtMillis: record.UpdatedAtMillis, Deleted: record.Deleted}
	value.SHA256 = digest(value)
	return value, ValidRevision(value)
}

func ValidRevision(value RevisionAudit) bool {
	want := value.SHA256
	value.SHA256 = ""
	return digestPattern.MatchString(want) && want == digest(value)
}

func contextFor(item Item, binding Binding) ContextStatus {
	if item.CandidateSHA == "" || item.BaseSHA == "" {
		return ContextAmbiguous
	}
	if item.CandidateSHA != binding.CandidateSHA || item.BaseSHA != binding.BaseSHA {
		return ContextStale
	}
	return ContextCurrent
}

func dispositionFor(item Item, status ContextStatus) Disposition {
	if item.Deleted || item.Kind == KindDismissed {
		return DispositionDeleted
	}
	if item.Actor.Kind != ActorHuman {
		return DispositionIgnoredNonHuman
	}
	if status == ContextStale {
		return DispositionHistorical
	}
	if status == ContextAmbiguous || item.RequiresHumanDecision || item.Severity == correction.SeverityP2 {
		return DispositionNeedsDecision
	}
	if !item.Actionable || item.Kind == KindApproved {
		return DispositionInformational
	}
	return DispositionActionable
}

func normalize(item Item, snapshot Snapshot, binding Binding, replaces string) (Record, bool) {
	summary := ""
	if !item.Deleted {
		var ok bool
		summary, ok = Redact(item.Body)
		if !ok {
			return Record{}, false
		}
	}
	status := contextFor(item, binding)
	record := Record{SchemaVersion: RecordSchemaVersion, SnapshotID: snapshot.ID, Source: item.Source,
		ExternalID: item.ExternalID, RevisionID: item.RevisionID, Actor: item.Actor, Kind: item.Kind,
		CandidateSHA: item.CandidateSHA, BaseSHA: item.BaseSHA, ContextSHA256: item.ContextSHA256,
		ContextStatus: status, Summary: summary, ContentSHA256: digest(summary), Actionable: item.Actionable,
		Severity: item.Severity, RequiresHumanDecision: item.RequiresHumanDecision,
		CreatedAtMillis: item.CreatedAtMillis, UpdatedAtMillis: item.UpdatedAtMillis,
		ObservedAtMillis: snapshot.ObservedAtMillis, Deleted: item.Deleted, ReplacesRecordID: replaces}
	record.Disposition = dispositionFor(item, status)
	record.ID = "feedback-record-" + digest(struct {
		Source                 Source
		ExternalID, RevisionID string
	}{item.Source, item.ExternalID, item.RevisionID})[:32]
	record.AuditSHA256 = digest(recordValue(record))
	return record, ValidRecord(record)
}

func ValidRecord(value Record) bool {
	if value.SchemaVersion != RecordSchemaVersion || !identifierPattern.MatchString(value.ID) || !identifierPattern.MatchString(value.SnapshotID) ||
		!validSource(value.Source) || !identifierPattern.MatchString(value.ExternalID) || !identifierPattern.MatchString(value.RevisionID) ||
		!validActor(value.Actor, value.Source) || !digestPattern.MatchString(value.ContextSHA256) ||
		!slices.Contains([]ContextStatus{ContextCurrent, ContextStale, ContextAmbiguous}, value.ContextStatus) ||
		!slices.Contains([]Disposition{DispositionActionable, DispositionInformational, DispositionDeleted, DispositionIgnoredNonHuman, DispositionHistorical, DispositionNeedsDecision}, value.Disposition) ||
		!digestPattern.MatchString(value.ContentSHA256) || value.CreatedAtMillis < 0 || value.UpdatedAtMillis < value.CreatedAtMillis ||
		value.ObservedAtMillis < value.UpdatedAtMillis || !slices.Contains([]correction.Severity{correction.SeverityP0, correction.SeverityP1, correction.SeverityP2, correction.SeverityP3}, value.Severity) ||
		(value.ReplacesRecordID != "" && !identifierPattern.MatchString(value.ReplacesRecordID)) || !digestPattern.MatchString(value.AuditSHA256) ||
		value.AuditSHA256 != digest(recordValue(value)) {
		return false
	}
	if value.Deleted {
		return value.Summary == "" && value.Disposition == DispositionDeleted
	}
	return value.Summary != "" && len(value.Summary) <= MaximumSummaryBytes && utf8.ValidString(value.Summary) &&
		!safedata.ContainsSecret(value.Summary) && !safedata.ContainsPrivatePath(value.Summary)
}

func snapshotAudit(snapshot Snapshot) SnapshotRecord {
	items := make([]string, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		items = append(items, item.SourceKey()+":"+digest(item))
	}
	slices.Sort(items)
	revision := digest(struct {
		Source   Source
		Complete bool
		Items    []string
	}{snapshot.Source, snapshot.Complete, items})
	value := SnapshotRecord{ID: snapshot.ID, Source: snapshot.Source, SHA256: snapshot.SHA256, RevisionSHA256: revision, Complete: snapshot.Complete,
		PageCount: snapshot.PageCount, ItemCount: uint32(len(snapshot.Items)), ObservedAtMillis: snapshot.ObservedAtMillis}
	value.AuditSHA256 = digest(value)
	return value
}

func Clone(state State) State {
	state.Snapshots = slices.Clone(state.Snapshots)
	state.Records = slices.Clone(state.Records)
	state.Current = slices.Clone(state.Current)
	if state.Decision != nil {
		decision := *state.Decision
		state.Decision = &decision
	}
	return state
}

func recordIndex(state State) map[string]int {
	result := make(map[string]int, len(state.Records))
	for index, record := range state.Records {
		result[record.ID] = index
	}
	return result
}

func currentKey(source Source, externalID string) string { return string(source) + "\x1f" + externalID }

func currentIndex(state State) map[string]int {
	result := make(map[string]int, len(state.Current))
	for index, current := range state.Current {
		result[currentKey(current.Source, current.ExternalID)] = index
	}
	return result
}

func newer(record Record, current CurrentRecord) bool {
	return record.UpdatedAtMillis > current.UpdatedAtMillis ||
		record.UpdatedAtMillis == current.UpdatedAtMillis && record.RevisionID > current.RevisionID
}

func tombstone(snapshot Snapshot, prior Record) Record {
	revision := "deleted-" + digest(struct{ Snapshot, External string }{snapshot.ID, prior.ExternalID})[:32]
	item := Item{Source: prior.Source, ExternalID: prior.ExternalID, RevisionID: revision, Actor: prior.Actor,
		Kind: KindDismissed, CandidateSHA: prior.CandidateSHA, BaseSHA: prior.BaseSHA, ContextSHA256: prior.ContextSHA256,
		Severity: correction.SeverityP3, CreatedAtMillis: prior.CreatedAtMillis, UpdatedAtMillis: snapshot.ObservedAtMillis, Deleted: true}
	record, _ := normalize(item, snapshot, Binding{CandidateSHA: prior.CandidateSHA, BaseSHA: prior.BaseSHA}, prior.ID)
	// normalize only reads Candidate/Base from this intentionally narrow value.
	record.ContextStatus, record.Disposition = prior.ContextStatus, DispositionDeleted
	record.AuditSHA256 = digest(recordValue(record))
	return record
}

func reseal(state *State) {
	slices.SortFunc(state.Current, func(left, right CurrentRecord) int {
		if bySource := strings.Compare(string(left.Source), string(right.Source)); bySource != 0 {
			return bySource
		}
		return strings.Compare(left.ExternalID, right.ExternalID)
	})
	active := make([]string, 0, len(state.Current))
	byID := recordIndex(*state)
	manual, actionable := false, false
	for _, current := range state.Current {
		record := state.Records[byID[current.RecordID]]
		if record.Deleted || record.Actor.Kind != ActorHuman || record.ContextStatus == ContextStale {
			continue
		}
		if record.Disposition == DispositionNeedsDecision {
			manual = true
		}
		if record.Disposition == DispositionActionable || record.Disposition == DispositionNeedsDecision {
			actionable = true
			active = append(active, record.AuditSHA256)
		}
	}
	state.CurrentRevision = digest(active)
	state.NeedsYouCode, state.WakeCondition = "", ""
	switch {
	case manual:
		state.Phase, state.NeedsYouCode, state.WakeCondition = PhaseNeedsYou, "feedback_human_decision_required", "authenticated_human_feedback_decision"
	case actionable:
		state.Phase = PhaseCorrectionReady
	default:
		state.Phase = PhaseObserved
	}
	state.SHA256 = digest(stateValue(*state))
}

// Reconcile appends immutable source revisions, keeps the newest revision
// current, synthesizes deletions only from complete GitHub scans, and never
// turns a bot/app or ambiguous context into human authority.
func Reconcile(prior *State, binding Binding, snapshots []Snapshot, nowMillis int64) (State, bool, bool) {
	if !ValidBinding(binding) || len(snapshots) == 0 {
		return State{}, false, false
	}
	state := State{SchemaVersion: StateSchemaVersion, Binding: binding, Snapshots: []SnapshotRecord{}, Records: []Record{}, Current: []CurrentRecord{}}
	if prior != nil {
		if !ValidState(*prior) || prior.Invalidated || prior.Binding != binding {
			return State{}, false, false
		}
		state = Clone(*prior)
	}
	existingSnapshots := make(map[string]SnapshotRecord, len(state.Snapshots))
	latestSourceSnapshot := make(map[Source]SnapshotRecord, len(sources))
	for _, audit := range state.Snapshots {
		existingSnapshots[audit.ID] = audit
		latestSourceSnapshot[audit.Source] = audit
	}
	byRecord := recordIndex(state)
	byCurrent := currentIndex(state)
	changed := false
	snapshots = slices.Clone(snapshots)
	slices.SortFunc(snapshots, func(left, right Snapshot) int {
		if rank := cmp.Compare(sourceRank(left.Source), sourceRank(right.Source)); rank != 0 {
			return rank
		}
		if byTime := cmp.Compare(left.ObservedAtMillis, right.ObservedAtMillis); byTime != 0 {
			return byTime
		}
		return strings.Compare(left.ID, right.ID)
	})
	for _, snapshot := range snapshots {
		if !ValidSnapshot(snapshot, binding, nowMillis) {
			return State{}, false, false
		}
		if existing, duplicate := existingSnapshots[snapshot.ID]; duplicate {
			if existing.SHA256 != snapshot.SHA256 {
				return State{}, false, false
			}
			continue
		}
		audit := snapshotAudit(snapshot)
		if latest, exists := latestSourceSnapshot[snapshot.Source]; exists && latest.RevisionSHA256 == audit.RevisionSHA256 {
			continue
		}
		if len(state.Snapshots) >= MaximumSnapshots {
			return State{}, false, false
		}
		seenExternal := make(map[string]struct{}, len(snapshot.Items))
		items := slices.Clone(snapshot.Items)
		slices.SortFunc(items, func(left, right Item) int {
			if left.UpdatedAtMillis != right.UpdatedAtMillis {
				return cmp.Compare(left.UpdatedAtMillis, right.UpdatedAtMillis)
			}
			return strings.Compare(left.SourceKey(), right.SourceKey())
		})
		for _, item := range items {
			seenExternal[item.ExternalID] = struct{}{}
			key := currentKey(item.Source, item.ExternalID)
			replaces := ""
			if index, exists := byCurrent[key]; exists {
				current := state.Current[index]
				if item.UpdatedAtMillis > current.UpdatedAtMillis || item.UpdatedAtMillis == current.UpdatedAtMillis && item.RevisionID > current.RevisionID {
					replaces = current.RecordID
				}
			}
			record, ok := normalize(item, snapshot, binding, replaces)
			if !ok {
				return State{}, false, false
			}
			if index, duplicate := byRecord[record.ID]; duplicate {
				if digest(revisionValue(state.Records[index])) != digest(revisionValue(record)) {
					return State{}, false, false
				}
				continue
			}
			if len(state.Records) >= MaximumRecords {
				return State{}, false, false
			}
			state.Records = append(state.Records, record)
			byRecord[record.ID] = len(state.Records) - 1
			changed = true
			current := CurrentRecord{Source: record.Source, ExternalID: record.ExternalID, RevisionID: record.RevisionID,
				RecordID: record.ID, UpdatedAtMillis: record.UpdatedAtMillis}
			if index, exists := byCurrent[key]; !exists {
				state.Current = append(state.Current, current)
				byCurrent[key] = len(state.Current) - 1
			} else if newer(record, state.Current[index]) {
				state.Current[index] = current
			}
		}
		if snapshot.Complete {
			for key, index := range byCurrent {
				current := state.Current[index]
				if current.Source != snapshot.Source {
					continue
				}
				if _, seen := seenExternal[current.ExternalID]; seen {
					continue
				}
				priorRecord := state.Records[byRecord[current.RecordID]]
				if priorRecord.Deleted {
					continue
				}
				deleted := tombstone(snapshot, priorRecord)
				if !ValidRecord(deleted) || len(state.Records) >= MaximumRecords {
					return State{}, false, false
				}
				state.Records = append(state.Records, deleted)
				byRecord[deleted.ID] = len(state.Records) - 1
				state.Current[index] = CurrentRecord{Source: deleted.Source, ExternalID: deleted.ExternalID,
					RevisionID: deleted.RevisionID, RecordID: deleted.ID, UpdatedAtMillis: deleted.UpdatedAtMillis}
				_ = key
				changed = true
			}
		}
		state.Snapshots = append(state.Snapshots, audit)
		existingSnapshots[audit.ID] = audit
		latestSourceSnapshot[audit.Source] = audit
		changed = true
	}
	if !changed {
		return state, false, ValidState(state)
	}
	state.Decision, state.CorrectionBatchSHA = nil, ""
	reseal(&state)
	return state, changed, ValidState(state)
}

func CurrentActionable(state State) []Record {
	if !ValidState(state) || state.Invalidated {
		return nil
	}
	byID := recordIndex(state)
	result := make([]Record, 0)
	for _, current := range state.Current {
		record := state.Records[byID[current.RecordID]]
		contextAllowed := record.ContextStatus == ContextCurrent || record.ContextStatus == ContextAmbiguous && state.Decision != nil && state.Decision.AllowCorrection
		if !record.Deleted && record.Actor.Kind == ActorHuman && contextAllowed &&
			(record.Disposition == DispositionActionable || record.Disposition == DispositionNeedsDecision) {
			result = append(result, record)
		}
	}
	slices.SortFunc(result, func(left, right Record) int {
		if left.UpdatedAtMillis != right.UpdatedAtMillis {
			return cmp.Compare(left.UpdatedAtMillis, right.UpdatedAtMillis)
		}
		if bySource := strings.Compare(string(left.Source), string(right.Source)); bySource != 0 {
			return bySource
		}
		return strings.Compare(left.ExternalID, right.ExternalID)
	})
	return result
}

// BlocksDelivery reports unresolved current human feedback. It is a delivery
// gate only; it grants no correction, review, integration, or human authority.
// Stale, deleted, informational, bot, and App records never block.
func BlocksDelivery(state State) bool {
	if !ValidState(state) || state.Invalidated {
		return true
	}
	return state.Phase == PhaseCorrectionReady || state.Phase == PhaseCorrectionRouted || state.Phase == PhaseNeedsYou
}

// FollowUpRecords returns authenticated human requests irrespective of their
// old Candidate context. They may seed new work after Done but can never
// mutate or reopen the completed Task/Run.
func FollowUpRecords(state State) []Record {
	if !ValidState(state) || state.Invalidated {
		return nil
	}
	byID := recordIndex(state)
	result := make([]Record, 0)
	for _, current := range state.Current {
		record := state.Records[byID[current.RecordID]]
		if !record.Deleted && record.Actor.Kind == ActorHuman && record.Actionable &&
			record.Kind != KindApproved && record.Kind != KindDismissed {
			result = append(result, record)
		}
	}
	slices.SortFunc(result, func(left, right Record) int {
		if left.UpdatedAtMillis != right.UpdatedAtMillis {
			return cmp.Compare(left.UpdatedAtMillis, right.UpdatedAtMillis)
		}
		if bySource := strings.Compare(string(left.Source), string(right.Source)); bySource != 0 {
			return bySource
		}
		return strings.Compare(left.ExternalID, right.ExternalID)
	})
	return result
}

func CorrectionInputs(state State) (correction.SourceSnapshot, []correction.FindingInput, bool) {
	current := CurrentActionable(state)
	if len(current) == 0 {
		return correction.SourceSnapshot{}, nil, false
	}
	inputs := make([]correction.FindingInput, 0, len(current))
	for _, record := range current {
		class := "HUMAN_FEEDBACK"
		if record.Source != SourcePaseoDirect {
			class = "GITHUB_HUMAN_FEEDBACK"
		}
		if record.Kind == KindChangesRequested {
			class = "GITHUB_CHANGES_REQUESTED"
		}
		inputs = append(inputs, correction.FindingInput{ID: record.ID, Source: correction.SourceHuman, Class: class,
			Severity: record.Severity, CandidateID: state.Binding.CandidateID, CandidateSHA: state.Binding.CandidateSHA,
			Summary: record.Summary, Evidence: []correction.Evidence{{ID: record.ID, Kind: correction.EvidenceHumanFeedback, SHA256: record.AuditSHA256}},
			AcceptanceCoverage: []string{}, Blocking: record.Severity == correction.SeverityP0 || record.Severity == correction.SeverityP1 || record.Kind == KindChangesRequested,
			RequiresHumanDecision: record.Disposition == DispositionNeedsDecision && (state.Decision == nil || !state.Decision.AllowCorrection)})
	}
	return correction.SourceSnapshot{Source: correction.SourceHuman, Revision: state.CurrentRevision, Count: uint32(len(inputs))}, inputs, true
}

func MarkCorrectionRouted(state State, batchSHA string) (State, bool) {
	if !ValidState(state) || state.Invalidated || state.Phase != PhaseCorrectionReady || !digestPattern.MatchString(batchSHA) {
		return state, false
	}
	state = Clone(state)
	state.Phase, state.CorrectionBatchSHA = PhaseCorrectionRouted, batchSHA
	state.SHA256 = digest(stateValue(state))
	return state, ValidState(state)
}

// DispatchInFlight identifies the durable response-loss window between
// recording a correction-ready feedback batch and observing that the
// correction router accepted that exact batch. Candidate replacement and
// base invalidation must retain the current state until this window closes.
func DispatchInFlight(state State) bool {
	return ValidState(state) && !state.Invalidated && state.Phase == PhaseCorrectionReady
}

func RequireManual(state State, code, wake string) (State, bool) {
	if !ValidState(state) || state.Invalidated || !identifierPattern.MatchString(code) || !identifierPattern.MatchString(wake) {
		return state, false
	}
	state = Clone(state)
	state.Phase, state.NeedsYouCode, state.WakeCondition = PhaseNeedsYou, code, wake
	state.Decision = nil
	state.SHA256 = digest(stateValue(state))
	return state, ValidState(state)
}

func ApplyDecision(state State, actor Actor, id string, allow bool, nowMillis int64) (State, bool) {
	if !ValidState(state) || state.Invalidated || state.Phase != PhaseNeedsYou || !validActor(actor, SourcePaseoDirect) ||
		actor.Kind != ActorHuman || !identifierPattern.MatchString(id) || nowMillis < 0 {
		return state, false
	}
	state = Clone(state)
	decision := HumanDecision{ID: id, ActorID: actor.ID, FeedbackStateSHA256: state.SHA256, AllowCorrection: allow, DecidedAtMillis: nowMillis}
	decision.AuditSHA256 = digest(decision)
	state.Decision, state.NeedsYouCode, state.WakeCondition = &decision, "", ""
	if allow {
		state.Phase = PhaseCorrectionReady
	} else {
		state.Phase = PhaseObserved
	}
	state.SHA256 = digest(stateValue(state))
	return state, ValidState(state)
}

func Invalidate(state State, code string) State {
	state = Clone(state)
	state.Invalidated, state.InvalidationCode = true, code
	state.SHA256 = digest(stateValue(state))
	return state
}

func ValidState(state State) bool {
	if state.SchemaVersion != StateSchemaVersion || !ValidBinding(state.Binding) || len(state.Snapshots) > MaximumSnapshots ||
		len(state.Records) > MaximumRecords || len(state.Current) > MaximumRecords ||
		!slices.Contains([]Phase{PhaseObserved, PhaseCorrectionReady, PhaseCorrectionRouted, PhaseNeedsYou}, state.Phase) ||
		!digestPattern.MatchString(state.CurrentRevision) || !digestPattern.MatchString(state.SHA256) || state.SHA256 != digest(stateValue(state)) ||
		(state.Invalidated != (state.InvalidationCode != "")) {
		return false
	}
	seenSnapshots := map[string]struct{}{}
	seenRecords := map[string]Record{}
	for _, snapshot := range state.Snapshots {
		if !identifierPattern.MatchString(snapshot.ID) || !validSource(snapshot.Source) || !digestPattern.MatchString(snapshot.SHA256) ||
			!digestPattern.MatchString(snapshot.RevisionSHA256) ||
			snapshot.PageCount == 0 || snapshot.PageCount > MaximumPages || snapshot.ItemCount > MaximumPages*MaximumPageSize ||
			snapshot.ObservedAtMillis < 0 || snapshot.Source != SourcePaseoDirect && !snapshot.Complete ||
			!digestPattern.MatchString(snapshot.AuditSHA256) || snapshot.AuditSHA256 != digest(SnapshotRecord{ID: snapshot.ID, Source: snapshot.Source,
			SHA256: snapshot.SHA256, RevisionSHA256: snapshot.RevisionSHA256, Complete: snapshot.Complete,
			PageCount: snapshot.PageCount, ItemCount: snapshot.ItemCount, ObservedAtMillis: snapshot.ObservedAtMillis}) {
			return false
		}
		if _, duplicate := seenSnapshots[snapshot.ID]; duplicate {
			return false
		}
		seenSnapshots[snapshot.ID] = struct{}{}
	}
	for _, record := range state.Records {
		if !ValidRecord(record) {
			return false
		}
		if _, exists := seenSnapshots[record.SnapshotID]; !exists {
			return false
		}
		if _, duplicate := seenRecords[record.ID]; duplicate {
			return false
		}
		seenRecords[record.ID] = record
	}
	for _, record := range state.Records {
		if record.ReplacesRecordID == "" {
			continue
		}
		prior, exists := seenRecords[record.ReplacesRecordID]
		if !exists || prior.ID == record.ID || prior.Source != record.Source || prior.ExternalID != record.ExternalID ||
			prior.UpdatedAtMillis > record.UpdatedAtMillis || prior.UpdatedAtMillis == record.UpdatedAtMillis && prior.RevisionID >= record.RevisionID {
			return false
		}
	}
	seenCurrent := map[string]struct{}{}
	latest := map[string]Record{}
	for _, record := range state.Records {
		key := currentKey(record.Source, record.ExternalID)
		if previous, exists := latest[key]; !exists || record.UpdatedAtMillis > previous.UpdatedAtMillis ||
			record.UpdatedAtMillis == previous.UpdatedAtMillis && record.RevisionID > previous.RevisionID {
			latest[key] = record
		}
	}
	for _, current := range state.Current {
		key := currentKey(current.Source, current.ExternalID)
		record, exists := seenRecords[current.RecordID]
		if !exists || record.Source != current.Source || record.ExternalID != current.ExternalID || record.RevisionID != current.RevisionID ||
			record.UpdatedAtMillis != current.UpdatedAtMillis {
			return false
		}
		if _, duplicate := seenCurrent[key]; duplicate {
			return false
		}
		if newest, exists := latest[key]; !exists || newest.ID != record.ID {
			return false
		}
		seenCurrent[key] = struct{}{}
	}
	if len(seenCurrent) != len(latest) {
		return false
	}
	active := make([]string, 0, len(state.Current))
	for _, current := range state.Current {
		record := seenRecords[current.RecordID]
		if !record.Deleted && record.Actor.Kind == ActorHuman && record.ContextStatus != ContextStale &&
			(record.Disposition == DispositionActionable || record.Disposition == DispositionNeedsDecision) {
			active = append(active, record.AuditSHA256)
		}
	}
	if state.CurrentRevision != digest(active) {
		return false
	}
	if state.Phase == PhaseCorrectionRouted && !digestPattern.MatchString(state.CorrectionBatchSHA) || state.Phase != PhaseCorrectionRouted && state.CorrectionBatchSHA != "" {
		return false
	}
	if state.Phase == PhaseNeedsYou {
		if !identifierPattern.MatchString(state.NeedsYouCode) || !identifierPattern.MatchString(state.WakeCondition) {
			return false
		}
	} else if state.NeedsYouCode != "" || state.WakeCondition != "" {
		return false
	}
	if state.Decision != nil {
		decision := *state.Decision
		if !identifierPattern.MatchString(decision.ID) || !identifierPattern.MatchString(decision.ActorID) || !digestPattern.MatchString(decision.FeedbackStateSHA256) ||
			decision.DecidedAtMillis < 0 || !digestPattern.MatchString(decision.AuditSHA256) || decision.AuditSHA256 != digest(HumanDecision{ID: decision.ID,
			ActorID: decision.ActorID, FeedbackStateSHA256: decision.FeedbackStateSHA256, AllowCorrection: decision.AllowCorrection, DecidedAtMillis: decision.DecidedAtMillis}) {
			return false
		}
	}
	return true
}
