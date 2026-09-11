// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mcuadros/director-engine/adapters/organizergit"
	organizerapp "github.com/mcuadros/director-engine/application/organizer"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type organizerHTTPStore struct{ *memoryProjectStore }

func (store *organizerHTTPStore) LatestEventSequence(context.Context) (uint64, error) {
	return uint64(len(store.commands)), nil
}

func organizerHTTPRequest(t *testing.T, handler http.Handler, input planningport.OrganizerBootstrapInput, authenticated bool) (planningport.OrganizerBootstrapResult, int) {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, planningport.OrganizerMutationPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	definition, _ := planningport.EmbeddedDefinition()
	request.Header.Set(contractVersionHeader, definition.ContractVersion)
	request.Header.Set(contractHashHeader, input.ContractHash)
	if authenticated {
		request.Header.Set(definition.MutationActorHeaders.Kind, "human")
		request.Header.Set(definition.MutationActorHeaders.ID, "server-owner")
		request.Header.Set(definition.MutationActorHeaders.Session, "server-session")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result planningport.OrganizerBootstrapResult
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result, response.Code
}

func organizerHTTPInput(t *testing.T, hostID, projectID, projectName, organizerPath, productPath, remote string) planningport.OrganizerBootstrapInput {
	t.Helper()
	definition, _ := planningport.EmbeddedDefinition()
	hash, _ := planningport.SchemaSHA256()
	configuration := string(configurationJSON(projectID, projectName, productPath, remote))
	return planningport.OrganizerBootstrapInput{SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion,
		ContractHash: hash, HostID: hostID, RequestID: "request-home-" + projectID, Kind: "create.preview",
		ProjectID: projectID, ProjectName: projectName, RepositoryPath: organizerPath, ConfigurationJSON: &configuration}
}

func TestOrganizerBootstrapHTTPPreviewsThenAppliesExactCreate(t *testing.T) {
	root := privateTempDir(t)
	productPath, remote, _ := newProductRepository(t, root)
	organizerPath := filepath.Join(root, "organizer")
	store := &organizerHTTPStore{newMemoryProjectStore()}
	service := organizerapp.New(store, organizergit.New(), nil)
	handler := newOrganizerBootstrapHandler(service, store, "host-a")
	input := organizerHTTPInput(t, "host-a", "project-home-create", "Home Create", organizerPath, productPath, remote)

	previewResult, status := organizerHTTPRequest(t, handler, input, true)
	if status != http.StatusOK || previewResult.Status != "preview" || previewResult.Preview == nil || !previewResult.Preview.Valid || previewResult.Preview.ID == "" || previewResult.HostID != "host-a" {
		t.Fatalf("Preview response = %d %#v", status, previewResult)
	}
	if _, err := os.Stat(organizerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Preview mutated Organizer target: %v", err)
	}
	if _, err := store.Project(context.Background(), input.ProjectID); err == nil {
		t.Fatal("Preview persisted a Project")
	}

	input.Kind, input.PreviewID = "create.apply", &previewResult.Preview.ID
	applied, status := organizerHTTPRequest(t, handler, input, true)
	if status != http.StatusOK || applied.Status != "applied" || applied.ProjectVersion == nil || applied.Preview != nil {
		t.Fatalf("Apply response = %d %#v", status, applied)
	}
	project, err := store.Project(context.Background(), input.ProjectID)
	if err != nil || project.Organizer == nil || project.Organizer.Phase != "active" || project.Organizer.HumanActorID != "server-owner" {
		t.Fatalf("applied Project = %#v, %v", project, err)
	}
	replay, status := organizerHTTPRequest(t, handler, input, true)
	if status != http.StatusOK || replay.Status != "applied" || replay.ProjectVersion == nil || *replay.ProjectVersion != *applied.ProjectVersion {
		t.Fatalf("Apply replay = %d %#v", status, replay)
	}
}

func TestOrganizerBootstrapHTTPRejectsMissingAuthAndAnotherHostBeforeMutation(t *testing.T) {
	root := privateTempDir(t)
	productPath, remote, _ := newProductRepository(t, root)
	organizerPath := filepath.Join(root, "organizer")
	store := &organizerHTTPStore{newMemoryProjectStore()}
	handler := newOrganizerBootstrapHandler(organizerapp.New(store, organizergit.New(), nil), store, "host-a")
	input := organizerHTTPInput(t, "host-a", "project-home-refuse", "Home Refuse", organizerPath, productPath, remote)
	if _, status := organizerHTTPRequest(t, handler, input, false); status != http.StatusUnauthorized {
		t.Fatalf("missing-auth status = %d", status)
	}
	input.HostID = "host-b"
	if _, status := organizerHTTPRequest(t, handler, input, true); status != http.StatusConflict {
		t.Fatalf("host-mismatch status = %d", status)
	}
	if _, err := os.Stat(organizerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused request mutated Organizer target: %v", err)
	}
	if len(store.projects) != 0 || len(store.commands) != 0 {
		t.Fatalf("refused request reached TaskStore: projects=%#v commands=%#v", store.projects, store.commands)
	}
}
