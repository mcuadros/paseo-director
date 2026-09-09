// SPDX-License-Identifier: Apache-2.0

// Package organizer owns the typed Preview/Apply and recovery flow for one
// Project's Organizer repository/configuration. Hosts only render its
// projections and submit commands.
package organizer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	repoport "github.com/mcuadros/director-engine/ports/organizer"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

const PreviewSchemaVersion = "director.organizer-preview/v1"

var (
	ErrPreviewInvalid        = errors.New("Organizer Preview is invalid")
	ErrPreviewMismatch       = errors.New("Apply must name the exact current Organizer Preview")
	ErrHumanApprovalRequired = errors.New("Apply requires a confirmed server-authenticated human actor")
	ErrOperationConflict     = errors.New("Organizer operation identity conflicts with durable state")
	ErrVersionConflict       = errors.New("Organizer Project version conflict")
	ErrOrganizerNotActive    = errors.New("Project has no active Organizer")
	ErrOrganizerDrift        = errors.New("active Organizer repository/configuration drifted")
	identityPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]*$`)
)

// Boundary identifies a durable or external cut point used by recovery tests.
type Boundary string

const (
	BoundaryIntentPersisted        Boundary = "intent_persisted"
	BoundaryRepositoryPrepared     Boundary = "repository_prepared"
	BoundaryRepositoryRecorded     Boundary = "repository_progress_persisted"
	BoundaryConfigurationWritten   Boundary = "configuration_written"
	BoundaryConfigurationRecorded  Boundary = "configuration_progress_persisted"
	BoundaryReadmeWritten          Boundary = "readme_written"
	BoundaryReadmeRecorded         Boundary = "readme_progress_persisted"
	BoundaryReferencesWritten      Boundary = "references_written"
	BoundaryReferencesRecorded     Boundary = "references_progress_persisted"
	BoundaryRepositoryInitialized  Boundary = "repository_initialized"
	BoundaryInitializationRecorded Boundary = "initialization_progress_persisted"
	BoundaryRevisionCommitted      Boundary = "revision_committed"
	BoundaryRevisionRecorded       Boundary = "revision_progress_persisted"
	BoundaryActivationPersisted    Boundary = "activation_persisted"
)

// BoundaryHook injects a stop immediately after the named boundary. It is nil
// in production and permits deterministic crash/restart coverage in tests.
type BoundaryHook func(Boundary) error

// ProjectStore is the narrow Project subset of the selected TaskStore port.
type ProjectStore interface {
	CreateProject(context.Context, domain.CommandRequest, domain.Project, []domain.Workspace, domain.Event) (domain.CommandResult, error)
	Project(context.Context, string) (domain.Project, error)
	Workspaces(context.Context, string) ([]domain.Workspace, error)
	UpdateProject(context.Context, domain.CommandRequest, domain.Project, domain.Event) (domain.CommandResult, error)
}

// Service owns Organizer Preview/Apply/recovery orchestration.
type Service struct {
	store      ProjectStore
	repository repoport.Repository
	after      BoundaryHook
}

// New constructs the engine application service.
func New(store ProjectStore, repository repoport.Repository, after BoundaryHook) *Service {
	return &Service{store: store, repository: repository, after: after}
}

// Issue is a bounded deterministic Preview rejection.
type Issue struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// File previews one exact file written by Create Apply.
type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Preview is the complete policy projection for Create or Adopt.
type Preview struct {
	SchemaVersion       string   `json:"schemaVersion"`
	ID                  string   `json:"id"`
	Kind                string   `json:"kind"`
	RequestID           string   `json:"requestId"`
	ProjectID           string   `json:"projectId"`
	ProjectName         string   `json:"projectName"`
	RepositoryPath      string   `json:"repositoryPath"`
	OrganizerRevision   string   `json:"organizerRevision,omitempty"`
	ConfigurationSHA256 string   `json:"configurationSha256,omitempty"`
	Files               []File   `json:"files"`
	Operations          []string `json:"operations"`
	Valid               bool     `json:"valid"`
	Issues              []Issue  `json:"issues"`

	configurationJSON []byte
	readme            []byte
	ownership         []byte
	references        []referenceFile
	workspaces        []domain.Workspace
}

type referenceFile struct {
	path    string
	content []byte
}

// CreateRequest describes a local-only M1 Organizer creation.
type CreateRequest struct {
	RequestID         string
	ProjectID         string
	ProjectName       string
	RepositoryPath    string
	ConfigurationJSON []byte
}

// AdoptRequest describes a clean existing Organizer repository.
type AdoptRequest struct {
	RequestID      string
	ProjectID      string
	ProjectName    string
	RepositoryPath string
}

// HumanConfirmation is populated only after the engine boundary authenticates
// a human Apply action. Model claims and host narration are not confirmation.
type HumanConfirmation struct {
	ActorID   string
	Confirmed bool
}

// ApplyCreateCommand confirms one exact Create Preview.
type ApplyCreateCommand struct {
	RequestID    string
	PreviewID    string
	Request      CreateRequest
	Confirmation HumanConfirmation
}

// ApplyAdoptCommand confirms one exact Adopt Preview.
type ApplyAdoptCommand struct {
	RequestID    string
	PreviewID    string
	Request      AdoptRequest
	Confirmation HumanConfirmation
}

// Projection is the bounded durable Organizer state returned to hosts.
type Projection struct {
	ProjectID           string                `json:"projectId"`
	ProjectName         string                `json:"projectName"`
	State               string                `json:"state"`
	Version             uint64                `json:"version"`
	Mode                domain.OrganizerMode  `json:"mode"`
	Phase               domain.OrganizerPhase `json:"phase"`
	RepositoryPath      string                `json:"repositoryPath"`
	OrganizerRevision   string                `json:"organizerRevision"`
	ConfigurationSHA256 string                `json:"configurationSha256"`
}

func validIdentity(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && identityPattern.MatchString(value)
}

func validRequestIdentity(value string) bool {
	return len(value) >= 16 && validIdentity(value, 64)
}

func validHuman(confirmation HumanConfirmation) bool {
	value := confirmation.ActorID
	return confirmation.Confirmed && utf8.ValidString(value) && value == strings.TrimSpace(value) &&
		len(value) > 0 && len(value) <= 256 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func hash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func generatedReadme(projectName string) []byte {
	return []byte("# " + projectName + "\n\nThis is the Director Organizer repository for this Project.\n")
}

func generatedReferences(configuration domainconfig.Configuration) []referenceFile {
	references := make([]referenceFile, 0, len(configuration.Skills)+len(configuration.Templates))
	for _, skill := range configuration.Skills {
		references = append(references, referenceFile{
			path:    skill.Path,
			content: []byte("---\nname: " + skill.ID + "\ndescription: Director Organizer placeholder for " + skill.ID + ".\n---\n\n# " + skill.ID + "\n"),
		})
	}
	for _, template := range configuration.Templates {
		references = append(references, referenceFile{
			path:    template.Path,
			content: []byte("# " + template.ID + " template\n\nReplace this placeholder through Organizer Preview/Apply before launch.\n"),
		})
	}
	slices.SortFunc(references, func(left, right referenceFile) int {
		return strings.Compare(left.path, right.path)
	})
	return references
}

func referencePaths(configuration domainconfig.Configuration) []string {
	references := generatedReferences(configuration)
	paths := make([]string, len(references))
	for index, reference := range references {
		paths[index] = reference.path
	}
	return paths
}

func ownershipMarker(projectID, operationID, configurationSHA256 string) []byte {
	// This committed marker correlates an approved durable intent with an exact
	// private Create root. OperationID is not secret authorization.
	encoded, _ := json.Marshal(struct {
		SchemaVersion       string `json:"schemaVersion"`
		ProjectID           string `json:"projectId"`
		OperationID         string `json:"operationId"`
		ConfigurationSHA256 string `json:"configurationSha256"`
	}{
		SchemaVersion:       "director.organizer/v1",
		ProjectID:           projectID,
		OperationID:         operationID,
		ConfigurationSHA256: configurationSHA256,
	})
	return append(encoded, '\n')
}

func issue(code, path, message string) Issue {
	return Issue{Code: code, Path: path, Message: message}
}

func appendConfigurationIssues(preview *Preview, err error) {
	issues, ok := domainconfig.ValidationIssues(err)
	if !ok {
		preview.Issues = append(preview.Issues, issue("configuration.invalid", "configuration", "Organizer configuration is invalid"))
		return
	}
	for _, entry := range issues {
		preview.Issues = append(preview.Issues, Issue{
			Code: "configuration." + entry.Code, Path: entry.Path, Message: entry.Message,
		})
	}
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (service *Service) validateDocument(
	ctx context.Context,
	preview *Preview,
	configurationJSON []byte,
	verifyExternal bool,
) (domainconfig.Document, bool) {
	document, err := domainconfig.Parse(configurationJSON)
	if err != nil {
		appendConfigurationIssues(preview, err)
		return domainconfig.Document{}, false
	}
	configuration := document.Configuration()
	if configuration.Project.ID != preview.ProjectID {
		preview.Issues = append(preview.Issues, issue("project.id_mismatch", "project.id", "configuration Project ID differs from the requested Project"))
	}
	if configuration.Project.Name != preview.ProjectName {
		preview.Issues = append(preview.Issues, issue("project.name_mismatch", "project.name", "configuration Project name differs from the requested Project"))
	}
	overrides := make(map[string]domain.WorkspacePolicy, len(configuration.WorkspaceOverrides))
	for _, override := range configuration.WorkspaceOverrides {
		overrides[override.WorkspaceID] = domain.WorkspacePolicy{
			LaunchPolicy: string(override.LaunchPolicy), DeliveryMode: string(override.DeliveryMode),
		}
	}
	for index, workspace := range configuration.Workspaces {
		if pathContains(workspace.SourcePath, preview.RepositoryPath) || pathContains(preview.RepositoryPath, workspace.SourcePath) {
			preview.Issues = append(preview.Issues, issue(
				"workspace.organizer_overlap", fmt.Sprintf("workspaces[%d].sourcePath", index),
				"Organizer and product Workspace paths must be disjoint",
			))
			continue
		}
		if verifyExternal {
			resolved, err := service.repository.ResolveWorkspace(ctx, workspace.SourcePath, workspace.Remote)
			if err != nil {
				preview.Issues = append(preview.Issues, issue(
					"workspace.identity_mismatch", fmt.Sprintf("workspaces[%d]", index),
					"Workspace path or origin remote does not match the configuration",
				))
				continue
			}
			name := workspace.Name
			if name == "" {
				name = workspace.ID
			}
			policy, overridden := overrides[workspace.ID]
			if !overridden {
				policy = domain.WorkspacePolicy{LaunchPolicy: "inherit", DeliveryMode: "inherit"}
			}
			preview.workspaces = append(preview.workspaces, domain.Workspace{
				ID: domain.WorkspaceID(preview.ProjectID, workspace.ID), ProjectID: preview.ProjectID,
				Key: workspace.ID, Name: name,
				Repository: domain.RepositoryIdentity{
					ID: resolved.RepositoryID, Key: resolved.RepositoryKey,
					CanonicalRemote: resolved.CanonicalRemote, SourcePath: resolved.SourcePath,
					SourceDevice: resolved.SourceDevice, SourceInode: resolved.SourceInode,
					GitCommonDirectory: resolved.GitCommonDirectory,
					GitCommonDevice:    resolved.GitCommonDevice, GitCommonInode: resolved.GitCommonInode,
				},
				DefaultBaseBranch: workspace.DefaultBaseBranch, Policy: policy,
			})
		}
	}
	if verifyExternal && preview.Kind != "create" {
		if err := service.repository.VerifyFiles(ctx, preview.RepositoryPath, referencePaths(configuration)); err != nil {
			preview.Issues = append(preview.Issues, issue(
				"configuration.reference_missing", "configuration",
				"Every explicit skill and template reference must be a committed regular Organizer file",
			))
		}
	}
	return document, len(preview.Issues) == 0
}

func assignPreviewID(preview *Preview) error {
	encoded, err := json.Marshal(struct {
		SchemaVersion       string   `json:"schemaVersion"`
		Kind                string   `json:"kind"`
		RequestID           string   `json:"requestId"`
		ProjectID           string   `json:"projectId"`
		ProjectName         string   `json:"projectName"`
		RepositoryPath      string   `json:"repositoryPath"`
		OrganizerRevision   string   `json:"organizerRevision"`
		ConfigurationSHA256 string   `json:"configurationSha256"`
		Files               []File   `json:"files"`
		Operations          []string `json:"operations"`
		Valid               bool     `json:"valid"`
		Issues              []Issue  `json:"issues"`
	}{
		SchemaVersion: preview.SchemaVersion, Kind: preview.Kind, RequestID: preview.RequestID,
		ProjectID: preview.ProjectID, ProjectName: preview.ProjectName,
		RepositoryPath: preview.RepositoryPath, OrganizerRevision: preview.OrganizerRevision,
		ConfigurationSHA256: preview.ConfigurationSHA256,
		Files:               preview.Files, Operations: preview.Operations,
		Valid: preview.Valid, Issues: preview.Issues,
	})
	if err != nil {
		return err
	}
	preview.ID = hash(encoded)
	return nil
}

func basePreview(kind, requestID, projectID, projectName, repositoryPath string) Preview {
	preview := Preview{
		SchemaVersion: PreviewSchemaVersion, Kind: kind, RequestID: requestID,
		ProjectID: projectID, ProjectName: projectName, RepositoryPath: repositoryPath,
		Files: []File{}, Operations: []string{}, Issues: []Issue{},
	}
	if !validRequestIdentity(requestID) {
		preview.Issues = append(preview.Issues, issue("request.id_invalid", "requestId", "Request ID must be a stable correlation identity of at least 16 characters"))
	}
	if !validIdentity(projectID, 128) {
		preview.Issues = append(preview.Issues, issue("project.id_invalid", "projectId", "Project ID is invalid"))
	}
	if projectName == "" || !utf8.ValidString(projectName) || projectName != strings.TrimSpace(projectName) || len(projectName) > 512 || strings.IndexFunc(projectName, unicode.IsControl) >= 0 {
		preview.Issues = append(preview.Issues, issue("project.name_invalid", "projectName", "Project name is invalid"))
	}
	if repositoryPath == "" || !utf8.ValidString(repositoryPath) || !filepath.IsAbs(repositoryPath) || filepath.Clean(repositoryPath) != repositoryPath {
		preview.Issues = append(preview.Issues, issue("repository.path_invalid", "repositoryPath", "Organizer path must be a clean absolute path"))
	}
	return preview
}

func (service *Service) buildCreatePreview(ctx context.Context, request CreateRequest, observeTarget bool) (Preview, error) {
	preview := basePreview("create", request.RequestID, request.ProjectID, request.ProjectName, request.RepositoryPath)
	document, documentValid := service.validateDocument(ctx, &preview, request.ConfigurationJSON, observeTarget)
	if documentValid {
		preview.configurationJSON = document.CanonicalJSON()
		preview.ConfigurationSHA256 = document.SHA256()
		preview.readme = generatedReadme(request.ProjectName)
		preview.ownership = ownershipMarker(request.ProjectID, request.RequestID, preview.ConfigurationSHA256)
		preview.references = generatedReferences(document.Configuration())
		preview.Files = []File{
			{Path: ".director/organizer.json", SHA256: hash(preview.ownership)},
			{Path: "README.md", SHA256: hash(preview.readme)},
			{Path: "paseo-director.json", SHA256: hash(preview.configurationJSON)},
		}
		for _, reference := range preview.references {
			preview.Files = append(preview.Files, File{Path: reference.path, SHA256: hash(reference.content)})
		}
		preview.Operations = []string{
			"create owned Organizer directory", "write exact Organizer files and explicit references",
			"initialize local Git repository", "create initial Organizer commit",
			"activate exact revision in TaskStore",
		}
	}
	if observeTarget && len(preview.Issues) == 0 {
		absent, err := service.repository.CreateTargetAbsent(ctx, request.RepositoryPath)
		if err != nil {
			if errors.Is(err, repoport.ErrInvalidPath) {
				preview.Issues = append(preview.Issues, issue("repository.path_invalid", "repositoryPath", "Organizer path cannot be safely resolved"))
			} else {
				return Preview{}, err
			}
		} else if !absent {
			preview.Issues = append(preview.Issues, issue("repository.target_exists", "repositoryPath", "Create requires an absent Organizer target"))
		}
	}
	preview.Valid = len(preview.Issues) == 0
	if err := assignPreviewID(&preview); err != nil {
		return Preview{}, err
	}
	return preview, nil
}

// PreviewCreate validates and describes Create without filesystem or TaskStore mutation.
func (service *Service) PreviewCreate(ctx context.Context, request CreateRequest) (Preview, error) {
	return service.buildCreatePreview(ctx, request, true)
}

func repositoryIssue(err error) Issue {
	switch {
	case errors.Is(err, repoport.ErrRepositoryDirty):
		return issue("repository.dirty", "repositoryPath", "Organizer repository must be clean")
	case errors.Is(err, repoport.ErrMarkerMissing):
		return issue("repository.marker_missing", "repositoryPath", "Organizer repository has no paseo-director.json marker")
	case errors.Is(err, repoport.ErrRevisionMissing):
		return issue("repository.revision_missing", "repositoryPath", "Organizer repository has no committed revision")
	case errors.Is(err, repoport.ErrTargetMissing):
		return issue("repository.missing", "repositoryPath", "Organizer repository does not exist")
	default:
		return issue("repository.invalid", "repositoryPath", "Organizer repository is not an exact readable Git root")
	}
}

// PreviewAdopt reads and validates an existing Organizer without mutating it or TaskStore.
func (service *Service) PreviewAdopt(ctx context.Context, request AdoptRequest) (Preview, error) {
	preview := basePreview("adopt", request.RequestID, request.ProjectID, request.ProjectName, request.RepositoryPath)
	if len(preview.Issues) == 0 {
		snapshot, err := service.repository.Read(ctx, request.RepositoryPath)
		if err != nil {
			preview.Issues = append(preview.Issues, repositoryIssue(err))
		} else {
			preview.OrganizerRevision = snapshot.Revision
			document, valid := service.validateDocument(ctx, &preview, snapshot.ConfigurationJSON, true)
			if valid {
				preview.ConfigurationSHA256 = document.SHA256()
				preview.configurationJSON = document.CanonicalJSON()
				preview.Operations = []string{
					"adopt existing clean Organizer repository", "activate exact revision in TaskStore",
				}
			}
		}
	}
	preview.Valid = len(preview.Issues) == 0
	if err := assignPreviewID(&preview); err != nil {
		return Preview{}, err
	}
	return preview, nil
}

func projectProjection(project domain.Project) (Projection, error) {
	if project.Organizer == nil {
		return Projection{}, ErrOrganizerNotActive
	}
	organizer := project.Organizer
	return Projection{
		ProjectID: project.ID, ProjectName: project.Name, State: project.State, Version: project.Version,
		Mode: organizer.Mode, Phase: organizer.Phase, RepositoryPath: organizer.RepositoryPath,
		OrganizerRevision:   organizer.OrganizerRevision,
		ConfigurationSHA256: organizer.ConfigurationSHA256,
	}, nil
}

func (service *Service) runHook(boundary Boundary) error {
	if service.after == nil {
		return nil
	}
	return service.after(boundary)
}

func commandPayload(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func phaseCommand(requestID string, project domain.Project, phase domain.OrganizerPhase) (domain.CommandRequest, domain.Event) {
	key := "organizer." + requestID + "." + string(phase)
	payload := commandPayload(struct {
		Phase               domain.OrganizerPhase `json:"phase"`
		PreviewID           string                `json:"previewId"`
		OrganizerRevision   string                `json:"organizerRevision,omitempty"`
		ConfigurationSHA256 string                `json:"configurationSha256"`
	}{
		Phase: phase, PreviewID: project.Organizer.PreviewID,
		OrganizerRevision:   project.Organizer.OrganizerRevision,
		ConfigurationSHA256: project.Organizer.ConfigurationSHA256,
	})
	return domain.CommandRequest{
			IdempotencyKey: key, Type: "organizer.phase", AggregateID: project.ID,
			ExpectedVersion: project.Version - 1, Payload: payload,
		}, domain.Event{
			ID: key, Sequence: project.Version + 1, AggregateID: project.ID,
			AggregateVersion: project.Version, Type: "organizer.phase", Payload: payload,
		}
}

func (service *Service) savePhase(
	ctx context.Context,
	project domain.Project,
	requestID string,
	phase domain.OrganizerPhase,
	revision string,
) (domain.Project, error) {
	next := cloneProject(project)
	next.Version++
	next.Organizer.Phase = phase
	if revision != "" {
		next.Organizer.OrganizerRevision = revision
	}
	if phase == domain.OrganizerPhaseActive {
		next.State = "active"
		next.Organizer.PendingConfiguration = nil
	}
	command, event := phaseCommand(requestID, next, phase)
	result, err := service.store.UpdateProject(ctx, command, next, event)
	if err != nil {
		return domain.Project{}, err
	}
	if result.Outcome != domain.CommandApplied || result.ObservedVersion != next.Version {
		return domain.Project{}, ErrVersionConflict
	}
	return next, nil
}

func cloneProject(project domain.Project) domain.Project {
	if project.Organizer != nil {
		organizer := *project.Organizer
		organizer.PendingConfiguration = slices.Clone(organizer.PendingConfiguration)
		project.Organizer = &organizer
	}
	if project.Lease != nil {
		lease := *project.Lease
		project.Lease = &lease
	}
	if project.LeaseObservation != nil {
		observation := *project.LeaseObservation
		project.LeaseObservation = &observation
	}
	return project
}

func initialCreateProject(command ApplyCreateCommand, preview Preview) domain.Project {
	return domain.Project{
		ID: command.Request.ProjectID, Name: command.Request.ProjectName,
		State: "paused", Version: 0,
		Organizer: &domain.Organizer{
			ID:   domain.OrganizerID(command.Request.ProjectID),
			Mode: domain.OrganizerModeCreate, Phase: domain.OrganizerPhaseIntentRecorded,
			RepositoryPath: command.Request.RepositoryPath, PreviewID: preview.ID,
			OperationID: command.RequestID, HumanActorID: command.Confirmation.ActorID,
			ConfigurationSHA256:  preview.ConfigurationSHA256,
			PendingConfiguration: slices.Clone(preview.configurationJSON),
		},
	}
}

func (service *Service) createIntent(
	ctx context.Context,
	command ApplyCreateCommand,
	preview Preview,
) (domain.Project, error) {
	project := initialCreateProject(command, preview)
	payload := commandPayload(struct {
		PreviewID           string `json:"previewId"`
		RepositoryPath      string `json:"repositoryPath"`
		ConfigurationSHA256 string `json:"configurationSha256"`
		HumanActorID        string `json:"humanActorId"`
	}{
		PreviewID: preview.ID, RepositoryPath: command.Request.RepositoryPath,
		ConfigurationSHA256: preview.ConfigurationSHA256,
		HumanActorID:        command.Confirmation.ActorID,
	})
	request := domain.CommandRequest{
		IdempotencyKey: "organizer." + command.RequestID + ".intent",
		Type:           "organizer.create", AggregateID: project.ID, ExpectedVersion: 0, Payload: payload,
	}
	event := domain.Event{
		ID: request.IdempotencyKey, Sequence: 1, AggregateID: project.ID,
		AggregateVersion: 0, Type: "organizer.intent", Payload: payload,
	}
	result, err := service.store.CreateProject(ctx, request, project, preview.workspaces, event)
	if err != nil {
		return domain.Project{}, err
	}
	if result.Outcome != domain.CommandApplied || result.ObservedVersion != 0 {
		return domain.Project{}, ErrVersionConflict
	}
	return project, nil
}

func validateCreateReplay(project domain.Project, command ApplyCreateCommand, preview Preview) error {
	if project.Organizer == nil || project.Organizer.Mode != domain.OrganizerModeCreate ||
		project.ID != command.Request.ProjectID || project.Name != command.Request.ProjectName ||
		project.Organizer.RepositoryPath != command.Request.RepositoryPath ||
		project.Organizer.PreviewID != command.PreviewID || project.Organizer.PreviewID != preview.ID ||
		project.Organizer.OperationID != command.RequestID ||
		project.Organizer.HumanActorID != command.Confirmation.ActorID ||
		project.Organizer.ConfigurationSHA256 != preview.ConfigurationSHA256 {
		return ErrOperationConflict
	}
	if project.Organizer.Phase != domain.OrganizerPhaseActive &&
		!bytes.Equal(project.Organizer.PendingConfiguration, preview.configurationJSON) {
		return ErrOperationConflict
	}
	return nil
}

// ApplyCreate persists human-approved intent before any external effect and
// runs the recoverable idempotent Create saga.
func (service *Service) ApplyCreate(ctx context.Context, command ApplyCreateCommand) (Projection, error) {
	if !validRequestIdentity(command.RequestID) {
		return Projection{}, ErrOperationConflict
	}
	if command.RequestID != command.Request.RequestID {
		return Projection{}, ErrOperationConflict
	}
	if !validHuman(command.Confirmation) {
		return Projection{}, ErrHumanApprovalRequired
	}
	specPreview, err := service.buildCreatePreview(ctx, command.Request, false)
	if err != nil {
		return Projection{}, err
	}
	if !specPreview.Valid {
		return Projection{}, ErrPreviewInvalid
	}
	if command.PreviewID == "" || command.PreviewID != specPreview.ID {
		return Projection{}, ErrPreviewMismatch
	}
	project, err := service.store.Project(ctx, command.Request.ProjectID)
	if errors.Is(err, storeport.ErrNotFound) {
		observed, observeErr := service.PreviewCreate(ctx, command.Request)
		if observeErr != nil {
			return Projection{}, observeErr
		}
		if !observed.Valid {
			return Projection{}, ErrPreviewInvalid
		}
		if observed.ID != command.PreviewID {
			return Projection{}, ErrPreviewMismatch
		}
		project, err = service.createIntent(ctx, command, observed)
		if err != nil {
			return Projection{}, err
		}
		if err := service.runHook(BoundaryIntentPersisted); err != nil {
			return Projection{}, err
		}
	} else if err != nil {
		return Projection{}, err
	} else if err := validateCreateReplay(project, command, specPreview); err != nil {
		return Projection{}, err
	}
	return service.recoverCreate(ctx, project)
}

func (service *Service) recoverCreate(ctx context.Context, project domain.Project) (Projection, error) {
	if project.Organizer == nil || project.Organizer.Mode != domain.OrganizerModeCreate {
		return Projection{}, ErrOperationConflict
	}
	requestID := project.Organizer.OperationID
	configurationJSON := slices.Clone(project.Organizer.PendingConfiguration)
	var references []referenceFile
	tracked := []string{".director/organizer.json", "README.md", "paseo-director.json"}
	if project.Organizer.Phase != domain.OrganizerPhaseActive {
		document, err := domainconfig.Parse(configurationJSON)
		if err != nil || document.SHA256() != project.Organizer.ConfigurationSHA256 {
			return Projection{}, ErrOperationConflict
		}
		references = generatedReferences(document.Configuration())
		for _, reference := range references {
			tracked = append(tracked, reference.path)
		}
	}
	readme := generatedReadme(project.Name)
	marker := ownershipMarker(project.ID, requestID, project.Organizer.ConfigurationSHA256)
	for {
		switch project.Organizer.Phase {
		case domain.OrganizerPhaseIntentRecorded:
			if err := service.repository.EnsureRoot(ctx, repoport.RootSpec{
				Path: project.Organizer.RepositoryPath, Marker: marker,
			}); err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryRepositoryPrepared); err != nil {
				return Projection{}, err
			}
			var err error
			project, err = service.savePhase(ctx, project, requestID, domain.OrganizerPhaseRepositoryPrepared, "")
			if err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryRepositoryRecorded); err != nil {
				return Projection{}, err
			}
		case domain.OrganizerPhaseRepositoryPrepared:
			if err := service.repository.EnsureFile(ctx, project.Organizer.RepositoryPath, "paseo-director.json", configurationJSON); err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryConfigurationWritten); err != nil {
				return Projection{}, err
			}
			var err error
			project, err = service.savePhase(ctx, project, requestID, domain.OrganizerPhaseConfigurationWritten, "")
			if err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryConfigurationRecorded); err != nil {
				return Projection{}, err
			}
		case domain.OrganizerPhaseConfigurationWritten:
			if err := service.repository.EnsureFile(ctx, project.Organizer.RepositoryPath, "README.md", readme); err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryReadmeWritten); err != nil {
				return Projection{}, err
			}
			var err error
			project, err = service.savePhase(ctx, project, requestID, domain.OrganizerPhaseReadmeWritten, "")
			if err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryReadmeRecorded); err != nil {
				return Projection{}, err
			}
		case domain.OrganizerPhaseReadmeWritten:
			for _, reference := range references {
				if err := service.repository.EnsureFile(ctx, project.Organizer.RepositoryPath, reference.path, reference.content); err != nil {
					return Projection{}, err
				}
			}
			if err := service.runHook(BoundaryReferencesWritten); err != nil {
				return Projection{}, err
			}
			var err error
			project, err = service.savePhase(ctx, project, requestID, domain.OrganizerPhaseReferencesWritten, "")
			if err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryReferencesRecorded); err != nil {
				return Projection{}, err
			}
		case domain.OrganizerPhaseReferencesWritten:
			if err := service.repository.EnsureInitialized(ctx, project.Organizer.RepositoryPath); err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryRepositoryInitialized); err != nil {
				return Projection{}, err
			}
			var err error
			project, err = service.savePhase(ctx, project, requestID, domain.OrganizerPhaseRepositoryInitialized, "")
			if err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryInitializationRecorded); err != nil {
				return Projection{}, err
			}
		case domain.OrganizerPhaseRepositoryInitialized:
			if err := service.repository.VerifyMarker(ctx, repoport.RootSpec{
				Path: project.Organizer.RepositoryPath, Marker: marker,
			}); err != nil {
				return Projection{}, err
			}
			for _, exactFile := range []struct {
				path    string
				content []byte
			}{
				{path: "README.md", content: readme},
				{path: "paseo-director.json", content: configurationJSON},
			} {
				if err := service.repository.EnsureFile(ctx, project.Organizer.RepositoryPath, exactFile.path, exactFile.content); err != nil {
					return Projection{}, err
				}
			}
			for _, reference := range references {
				if err := service.repository.EnsureFile(ctx, project.Organizer.RepositoryPath, reference.path, reference.content); err != nil {
					return Projection{}, err
				}
			}
			revision, err := service.repository.EnsureCommit(
				ctx, project.Organizer.RepositoryPath,
				tracked,
				"Initialize Director Organizer",
			)
			if err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryRevisionCommitted); err != nil {
				return Projection{}, err
			}
			project, err = service.savePhase(ctx, project, requestID, domain.OrganizerPhaseRevisionCommitted, revision)
			if err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryRevisionRecorded); err != nil {
				return Projection{}, err
			}
		case domain.OrganizerPhaseRevisionCommitted:
			if err := service.verifyActiveRepository(ctx, project); err != nil {
				return Projection{}, err
			}
			var err error
			project, err = service.savePhase(ctx, project, requestID, domain.OrganizerPhaseActive, "")
			if err != nil {
				return Projection{}, err
			}
			if err := service.runHook(BoundaryActivationPersisted); err != nil {
				return Projection{}, err
			}
		case domain.OrganizerPhaseActive:
			return service.Open(ctx, project.ID)
		default:
			return Projection{}, ErrOperationConflict
		}
	}
}

// ApplyAdopt validates the exact Preview again and atomically records one
// active Project. It never mutates the adopted or product repositories.
func (service *Service) ApplyAdopt(ctx context.Context, command ApplyAdoptCommand) (Projection, error) {
	if !validRequestIdentity(command.RequestID) {
		return Projection{}, ErrOperationConflict
	}
	if command.RequestID != command.Request.RequestID {
		return Projection{}, ErrOperationConflict
	}
	if !validHuman(command.Confirmation) {
		return Projection{}, ErrHumanApprovalRequired
	}
	existing, err := service.store.Project(ctx, command.Request.ProjectID)
	if err == nil {
		if existing.Organizer == nil || existing.Organizer.Mode != domain.OrganizerModeAdopt ||
			existing.Organizer.OperationID != command.RequestID || existing.Organizer.PreviewID != command.PreviewID ||
			existing.Organizer.HumanActorID != command.Confirmation.ActorID ||
			existing.Name != command.Request.ProjectName || existing.Organizer.RepositoryPath != command.Request.RepositoryPath {
			return Projection{}, ErrOperationConflict
		}
		return service.Open(ctx, existing.ID)
	}
	if !errors.Is(err, storeport.ErrNotFound) {
		return Projection{}, err
	}
	preview, err := service.PreviewAdopt(ctx, command.Request)
	if err != nil {
		return Projection{}, err
	}
	if !preview.Valid {
		return Projection{}, ErrPreviewInvalid
	}
	if command.PreviewID == "" || command.PreviewID != preview.ID {
		return Projection{}, ErrPreviewMismatch
	}
	project := domain.Project{
		ID: command.Request.ProjectID, Name: command.Request.ProjectName, State: "active", Version: 0,
		Organizer: &domain.Organizer{
			ID:   domain.OrganizerID(command.Request.ProjectID),
			Mode: domain.OrganizerModeAdopt, Phase: domain.OrganizerPhaseActive,
			RepositoryPath: command.Request.RepositoryPath, PreviewID: preview.ID,
			OperationID: command.RequestID, HumanActorID: command.Confirmation.ActorID,
			ConfigurationSHA256: preview.ConfigurationSHA256,
			OrganizerRevision:   preview.OrganizerRevision,
		},
	}
	payload := commandPayload(struct {
		PreviewID           string `json:"previewId"`
		RepositoryPath      string `json:"repositoryPath"`
		OrganizerRevision   string `json:"organizerRevision"`
		ConfigurationSHA256 string `json:"configurationSha256"`
		HumanActorID        string `json:"humanActorId"`
	}{
		PreviewID: preview.ID, RepositoryPath: preview.RepositoryPath,
		OrganizerRevision:   preview.OrganizerRevision,
		ConfigurationSHA256: preview.ConfigurationSHA256,
		HumanActorID:        command.Confirmation.ActorID,
	})
	request := domain.CommandRequest{
		IdempotencyKey: "organizer." + command.RequestID + ".adopt",
		Type:           "organizer.adopt", AggregateID: project.ID, ExpectedVersion: 0, Payload: payload,
	}
	event := domain.Event{
		ID: request.IdempotencyKey, Sequence: 1, AggregateID: project.ID,
		AggregateVersion: 0, Type: "organizer.adopted", Payload: payload,
	}
	result, err := service.store.CreateProject(ctx, request, project, preview.workspaces, event)
	if err != nil {
		return Projection{}, err
	}
	if result.Outcome != domain.CommandApplied || result.ObservedVersion != 0 {
		return Projection{}, ErrVersionConflict
	}
	return service.Open(ctx, project.ID)
}

// Recover resumes an approved interrupted Create or reopens an active Adopt.
func (service *Service) Recover(ctx context.Context, projectID string) (Projection, error) {
	project, err := service.store.Project(ctx, projectID)
	if err != nil {
		return Projection{}, err
	}
	if project.Organizer == nil {
		return Projection{}, ErrOrganizerNotActive
	}
	if project.Organizer.Mode == domain.OrganizerModeCreate && project.Organizer.Phase != domain.OrganizerPhaseActive {
		return service.recoverCreate(ctx, project)
	}
	return service.Open(ctx, projectID)
}

// Open verifies durable active state against the exact clean Organizer and
// every configured product Workspace using read-only observations.
func (service *Service) Open(ctx context.Context, projectID string) (Projection, error) {
	project, err := service.store.Project(ctx, projectID)
	if err != nil {
		return Projection{}, err
	}
	if project.State != "active" || project.Organizer == nil ||
		project.Organizer.Phase != domain.OrganizerPhaseActive {
		return Projection{}, ErrOrganizerNotActive
	}
	if err := service.verifyActiveRepository(ctx, project); err != nil {
		return Projection{}, err
	}
	return projectProjection(project)
}

func (service *Service) verifyActiveRepository(ctx context.Context, project domain.Project) error {
	snapshot, err := service.repository.Read(ctx, project.Organizer.RepositoryPath)
	if err != nil || snapshot.Revision != project.Organizer.OrganizerRevision {
		return ErrOrganizerDrift
	}
	preview := basePreview("open", project.Organizer.OperationID, project.ID, project.Name, project.Organizer.RepositoryPath)
	document, valid := service.validateDocument(ctx, &preview, snapshot.ConfigurationJSON, true)
	if !valid || document.SHA256() != project.Organizer.ConfigurationSHA256 {
		return ErrOrganizerDrift
	}
	durable, err := service.store.Workspaces(ctx, project.ID)
	if err != nil || len(durable) != len(preview.workspaces) {
		return ErrOrganizerDrift
	}
	slices.SortFunc(durable, func(left, right domain.Workspace) int { return strings.Compare(left.ID, right.ID) })
	slices.SortFunc(preview.workspaces, func(left, right domain.Workspace) int { return strings.Compare(left.ID, right.ID) })
	for index := range durable {
		durable[index].Version = 0
		if durable[index] != preview.workspaces[index] {
			return ErrOrganizerDrift
		}
	}
	return nil
}
