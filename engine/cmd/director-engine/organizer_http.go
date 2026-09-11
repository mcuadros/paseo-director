// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	organizerapp "github.com/mcuadros/director-engine/application/organizer"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type organizerCursorStore interface {
	LatestEventSequence(context.Context) (uint64, error)
}

type organizerBootstrapHandler struct {
	service         *organizerapp.Service
	store           organizerCursorStore
	hostID          string
	contractVersion string
	contractHash    string
}

func stringPointer(value string) *string { return &value }

func newOrganizerBootstrapHandler(service *organizerapp.Service, store organizerCursorStore, hostID string) http.Handler {
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		panic(err)
	}
	return &organizerBootstrapHandler{service: service, store: store, hostID: hostID, contractVersion: definition.ContractVersion, contractHash: hash}
}

func decodeOrganizerBootstrap(request *http.Request) (planningport.OrganizerBootstrapInput, error) {
	if request.ContentLength > planningport.MaximumRequestBytes {
		return planningport.OrganizerBootstrapInput{}, planningport.ErrQueryInvalid
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, planningport.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > planningport.MaximumRequestBytes {
		return planningport.OrganizerBootstrapInput{}, planningport.ErrQueryInvalid
	}
	content, err = jsondocument.CanonicalWithNormalizedNumbersLimit(content, planningport.MaximumRequestBytes)
	if err != nil {
		return planningport.OrganizerBootstrapInput{}, planningport.ErrQueryInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var input planningport.OrganizerBootstrapInput
	if decoder.Decode(&input) != nil {
		return planningport.OrganizerBootstrapInput{}, planningport.ErrQueryInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return planningport.OrganizerBootstrapInput{}, planningport.ErrQueryInvalid
	}
	return input, planningport.ValidateOrganizerBootstrap(input)
}

func organizerPreview(value organizerapp.Preview) *planningport.OrganizerBootstrapPreview {
	files := make([]planningport.OrganizerPreviewFile, 0, len(value.Files))
	for _, file := range value.Files {
		files = append(files, planningport.OrganizerPreviewFile{Path: file.Path, SHA256: file.SHA256})
	}
	issues := make([]planningport.OrganizerPreviewIssue, 0, len(value.Issues))
	for _, issue := range value.Issues {
		issues = append(issues, planningport.OrganizerPreviewIssue{Code: issue.Code, Field: issue.Path, Message: issue.Message})
	}
	var revision, configuration *string
	if value.OrganizerRevision != "" {
		revision = stringPointer(value.OrganizerRevision)
	}
	if value.ConfigurationSHA256 != "" {
		configuration = stringPointer(value.ConfigurationSHA256)
	}
	return &planningport.OrganizerBootstrapPreview{ID: value.ID, Kind: value.Kind, RequestID: value.RequestID,
		ProjectID: value.ProjectID, ProjectName: value.ProjectName, RepositoryPath: value.RepositoryPath,
		OrganizerRevision: revision, ConfigurationSHA256: configuration, Files: files,
		Operations: append([]string{}, value.Operations...), Valid: value.Valid, Issues: issues}
}

func (handler *organizerBootstrapHandler) result(ctx context.Context, input planningport.OrganizerBootstrapInput, status, message string, preview *planningport.OrganizerBootstrapPreview, version *uint64) (planningport.OrganizerBootstrapResult, error) {
	cursor, err := handler.store.LatestEventSequence(ctx)
	if err != nil {
		return planningport.OrganizerBootstrapResult{}, err
	}
	var versionText *string
	if version != nil {
		value := strconv.FormatUint(*version, 10)
		versionText = &value
	}
	return planningport.OrganizerBootstrapResult{SchemaVersion: 1, ContractVersion: handler.contractVersion,
		ContractHash: handler.contractHash, HostID: handler.hostID, Cursor: strconv.FormatUint(cursor, 10),
		RequestID: input.RequestID, Status: status, Message: message, Preview: preview, ProjectVersion: versionText}, nil
}

func (handler *organizerBootstrapHandler) execute(ctx context.Context, input planningport.OrganizerBootstrapInput, actorID string) (planningport.OrganizerBootstrapResult, error) {
	if input.HostID != handler.hostID {
		return planningport.OrganizerBootstrapResult{}, homeHostMismatchError{}
	}
	createRequest := organizerapp.CreateRequest{RequestID: input.RequestID, ProjectID: input.ProjectID,
		ProjectName: input.ProjectName, RepositoryPath: input.RepositoryPath}
	adoptRequest := organizerapp.AdoptRequest{RequestID: input.RequestID, ProjectID: input.ProjectID,
		ProjectName: input.ProjectName, RepositoryPath: input.RepositoryPath}
	if input.ConfigurationJSON != nil {
		createRequest.ConfigurationJSON = []byte(*input.ConfigurationJSON)
	}
	switch input.Kind {
	case "create.preview":
		preview, err := handler.service.PreviewCreate(ctx, createRequest)
		if err != nil {
			return planningport.OrganizerBootstrapResult{}, err
		}
		message := "Create Preview is ready for explicit human confirmation"
		if !preview.Valid {
			message = "Create Preview contains blocking issues and cannot be applied"
		}
		return handler.result(ctx, input, "preview", message, organizerPreview(preview), nil)
	case "adopt.preview":
		preview, err := handler.service.PreviewAdopt(ctx, adoptRequest)
		if err != nil {
			return planningport.OrganizerBootstrapResult{}, err
		}
		message := "Adopt Preview is ready for explicit human confirmation"
		if !preview.Valid {
			message = "Adopt Preview contains blocking issues and cannot be applied"
		}
		return handler.result(ctx, input, "preview", message, organizerPreview(preview), nil)
	case "create.apply":
		projection, err := handler.service.ApplyCreate(ctx, organizerapp.ApplyCreateCommand{RequestID: input.RequestID,
			PreviewID: *input.PreviewID, Request: createRequest, Confirmation: organizerapp.HumanConfirmation{ActorID: actorID, Confirmed: true}})
		if err != nil {
			if errors.Is(err, storeport.ErrUnhealthy) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return planningport.OrganizerBootstrapResult{}, err
			}
			return handler.result(ctx, input, "rejected", "Create Apply was rejected by current authoritative facts", nil, nil)
		}
		return handler.result(ctx, input, "applied", "Project and Organizer were applied at the exact Preview", nil, &projection.Version)
	case "adopt.apply":
		projection, err := handler.service.ApplyAdopt(ctx, organizerapp.ApplyAdoptCommand{RequestID: input.RequestID,
			PreviewID: *input.PreviewID, Request: adoptRequest, Confirmation: organizerapp.HumanConfirmation{ActorID: actorID, Confirmed: true}})
		if err != nil {
			if errors.Is(err, storeport.ErrUnhealthy) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return planningport.OrganizerBootstrapResult{}, err
			}
			return handler.result(ctx, input, "rejected", "Adopt Apply was rejected by current authoritative facts", nil, nil)
		}
		return handler.result(ctx, input, "applied", "Project and Organizer were adopted at the exact Preview", nil, &projection.Version)
	default:
		return planningport.OrganizerBootstrapResult{}, planningport.ErrQueryInvalid
	}
}

type homeHostMismatchError struct{}

func (homeHostMismatchError) Error() string { return "Home host mismatch" }

func (handler *organizerBootstrapHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.OrganizerMutationPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writePlanningError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if request.Header.Get("Content-Type") != "application/json" || request.Header.Get(contractVersionHeader) != handler.contractVersion || request.Header.Get(contractHashHeader) != handler.contractHash {
		writePlanningError(response, http.StatusConflict, "CONTRACT_MISMATCH")
		return
	}
	input, err := decodeOrganizerBootstrap(request)
	if err != nil {
		writePlanningError(response, http.StatusBadRequest, "ORGANIZER_INPUT_INVALID")
		return
	}
	actor, authenticated := planningMutationActor(request)
	if !authenticated {
		writePlanningError(response, http.StatusUnauthorized, "PLANNING_HUMAN_AUTH_REQUIRED")
		return
	}
	result, err := handler.execute(request.Context(), input, actor.ID)
	if err != nil {
		var mismatch homeHostMismatchError
		if errors.As(err, &mismatch) {
			writePlanningError(response, http.StatusConflict, "HOME_HOST_MISMATCH")
		} else {
			writePlanningError(response, http.StatusServiceUnavailable, "ORGANIZER_UNAVAILABLE")
		}
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set(contractVersionHeader, handler.contractVersion)
	response.Header().Set(contractHashHeader, handler.contractHash)
	_ = json.NewEncoder(response).Encode(result)
}
