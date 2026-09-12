// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/internal/testkit/secretfixture"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

func planningControlRequest(t *testing.T, handler http.Handler, input planningport.MutationInput) (planningport.MutationResult, int) {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, planningport.MutationPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(contractVersionHeader, definition.ContractVersion)
	request.Header.Set(contractHashHeader, hash)
	request.Header.Set(definition.MutationActorHeaders.Kind, "human")
	request.Header.Set(definition.MutationActorHeaders.ID, "server-owner")
	request.Header.Set(definition.MutationActorHeaders.Session, "server-session")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result planningport.MutationResult
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result, response.Code
}

func TestPlanningControlHTTPDerivesHumanIdentityAndRequiresFreshEmergencyReference(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, _ := initializeRepository(t, "control-http")
	project, _ := createVerticalRecords(t, store, "control-http", source)
	controller := executionapp.NewController(store, nil, nil, nil)
	handler := newPlanningMutationHandler(store, controller).(*planningMutationHandler)
	now := project.Lease.AcquiredAtMillis + 1
	handler.now = func() int64 { return now }
	definition, _ := planningport.EmbeddedDefinition()
	hash, _ := planningport.SchemaSHA256()
	input := planningport.MutationInput{
		SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion, ContractHash: hash,
		RequestID: "control-http-pause", IdempotencyKey: "control-http-pause",
		ExpectedVersion: strconv.FormatUint(project.Version, 10),
		Intent:          planningport.MutationIntent{Type: "project.pause", ProjectID: project.ID},
	}
	result, status := planningControlRequest(t, handler, input)
	if status != http.StatusOK || result.Status != "accepted" {
		t.Fatalf("pause response = %d %#v", status, result)
	}
	paused, err := store.Project(context.Background(), project.ID)
	if err != nil || paused.Control.Intent.ActorID != "server-owner" ||
		paused.Control.Intent.ActorSessionID != "server-session" || !paused.Control.Intent.Authenticated {
		t.Fatalf("server actor was not derived: %#v, %v", paused.Control.Intent, err)
	}

	input.RequestID, input.IdempotencyKey = "control-http-emergency-prepare", "control-http-emergency-prepare"
	input.ExpectedVersion = strconv.FormatUint(paused.Version, 10)
	input.Intent.Type = "project.emergency-stop.prepare"
	prepared, status := planningControlRequest(t, handler, input)
	if status != http.StatusOK || prepared.Status != "accepted" || prepared.ConfirmationRef == nil {
		t.Fatalf("prepare response = %d %#v", status, prepared)
	}
	input.RequestID, input.IdempotencyKey = "control-http-emergency-confirm", "control-http-emergency-confirm"
	input.ExpectedVersion = *prepared.UpdatedVersion
	input.Intent.Type = "project.emergency-stop.confirm"
	input.HumanApprovalRef = prepared.ConfirmationRef
	confirmed, status := planningControlRequest(t, handler, input)
	if status != http.StatusOK || confirmed.Status != "accepted" {
		t.Fatalf("confirm response = %d %#v", status, confirmed)
	}
	stopped, err := store.Project(context.Background(), project.ID)
	if err != nil || !stopped.Control.EmergencyLatched || stopped.State != "paused" {
		t.Fatalf("emergency state = %#v, %v", stopped.Control, err)
	}
	input.RequestID, input.IdempotencyKey = "control-http-emergency-replay-stale", "control-http-emergency-replay-stale"
	input.ExpectedVersion = strconv.FormatUint(stopped.Version, 10)
	stale, status := planningControlRequest(t, handler, input)
	if status != http.StatusOK || stale.Status != "rejected" {
		t.Fatalf("stale confirmation response = %d %#v", status, stale)
	}
}

func TestPlanningControlHTTPRejectsCallerAuthorshipFields(t *testing.T) {
	handler := newPlanningMutationHandler(nil, nil)
	definition, _ := planningport.EmbeddedDefinition()
	hash, _ := planningport.SchemaSHA256()
	body := []byte(`{"schemaVersion":1,"contractVersion":"` + definition.ContractVersion +
		`","contractHash":"` + hash +
		`","requestId":"control-forged-actor","idempotencyKey":"control-forged-actor","expectedVersion":"1","humanApprovalRef":null,"acknowledgementRevision":null,"actorId":"model","intent":{"type":"project.emergency-stop.confirm","projectId":"project","humanConfirmed":true}}`)
	request := httptest.NewRequest(http.MethodPost, planningport.MutationPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(contractVersionHeader, definition.ContractVersion)
	request.Header.Set(contractHashHeader, hash)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("forged actor response = %d %s", response.Code, response.Body.String())
	}
	validBody, err := json.Marshal(planningport.MutationInput{
		SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion, ContractHash: hash,
		RequestID: "control-missing-server-auth", IdempotencyKey: "control-missing-server-auth", ExpectedVersion: "1",
		Intent: planningport.MutationIntent{Type: "project.pause", ProjectID: "project"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, planningport.MutationPath, bytes.NewReader(validBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(contractVersionHeader, definition.ContractVersion)
	request.Header.Set(contractHashHeader, hash)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing server actor response = %d %s", response.Code, response.Body.String())
	}
	for _, actor := range [][3]string{
		{"model", "server-owner", "server-session"},
		{"human", secretfixture.GitHubFineGrained(), "server-session"},
		{"human", "/home/owner", "server-session"},
		{"human", "server-owner", "server session"},
	} {
		request = httptest.NewRequest(http.MethodPost, planningport.MutationPath, bytes.NewReader(validBody))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(contractVersionHeader, definition.ContractVersion)
		request.Header.Set(contractHashHeader, hash)
		request.Header.Set(definition.MutationActorHeaders.Kind, actor[0])
		request.Header.Set(definition.MutationActorHeaders.ID, actor[1])
		request.Header.Set(definition.MutationActorHeaders.Session, actor[2])
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), actor[1]) {
			t.Fatalf("forged actor response = %d %s", response.Code, response.Body.String())
		}
	}
}
