// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	homeapp "github.com/mcuadros/director-engine/application/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type operationsHTTPStub struct {
	queryInput    planningport.OperationsQueryInput
	mutationInput planningport.OperationsMutationInput
	actor         homeapp.AuthenticatedOperationsActor
}

func (stub *operationsHTTPStub) Operations(_ context.Context, input planningport.OperationsQueryInput) (planningport.OperationsReport, error) {
	stub.queryInput = input
	version, hash := planningContractIdentity()
	return planningport.OperationsReport{SchemaVersion: 1, ContractVersion: version, ContractHash: hash, Cursor: "4", HostID: input.HostID,
		HostInstanceID: "engine-a", ProjectID: input.ProjectID, ProjectName: "Project", ProjectVersion: input.ExpectedProjectVersion,
		ObservationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedAt: "2026-09-12T01:00:00Z", MaximumAgeMillis: "30000", Status: "healthy"}, nil
}

func (stub *operationsHTTPStub) Mutate(_ context.Context, input planningport.OperationsMutationInput, actor homeapp.AuthenticatedOperationsActor) (planningport.OperationsMutationResult, error) {
	stub.mutationInput, stub.actor = input, actor
	version, hash := planningContractIdentity()
	return planningport.OperationsMutationResult{SchemaVersion: 1, ContractVersion: version, ContractHash: hash, HostID: input.HostID,
		ProjectID: input.ProjectID, Cursor: "5", RequestID: input.RequestID, Status: "refused", Message: "Refused", RefusalCode: func() *string { value := "test_refusal"; return &value }()}, nil
}

func operationsRequest(t *testing.T, path string, value any) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	version, hash := planningContractIdentity()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(contractVersionHeader, version)
	request.Header.Set(contractHashHeader, hash)
	return request
}

func TestOperationsHTTPBindsExactContractAndServerAuthenticatedHuman(t *testing.T) {
	stub := &operationsHTTPStub{}
	query := planningport.OperationsQueryInput{HostID: "host-a", ProjectID: "project-a", ExpectedProjectVersion: "3"}
	response := httptest.NewRecorder()
	newOperationsHandler(stub).ServeHTTP(response, operationsRequest(t, planningport.OperationsQueryPath, query))
	if response.Code != http.StatusOK || stub.queryInput != query || response.Header().Get(contractHashHeader) == "" {
		t.Fatalf("query response=%d input=%#v headers=%v body=%s", response.Code, stub.queryInput, response.Header(), response.Body.String())
	}

	hash, _ := planningport.SchemaSHA256()
	mutation := planningport.OperationsMutationInput{SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash, HostID: "host-a",
		RequestID: "operations-request-0001", Kind: "reconcile.now", ProjectID: "project-a", ExpectedProjectVersion: "3", Confirmed: true}
	response = httptest.NewRecorder()
	newOperationsMutationHandler(stub).ServeHTTP(response, operationsRequest(t, planningport.OperationsMutationPath, mutation))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated mutation status=%d body=%s", response.Code, response.Body.String())
	}
	request := operationsRequest(t, planningport.OperationsMutationPath, mutation)
	request.Header.Set("x-director-actor-kind", "human")
	request.Header.Set("x-director-actor-id", "owner")
	request.Header.Set("x-director-actor-session", "session")
	response = httptest.NewRecorder()
	newOperationsMutationHandler(stub).ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.mutationInput != mutation || !stub.actor.Authenticated || stub.actor.ID != "owner" {
		t.Fatalf("authenticated mutation status=%d input=%#v actor=%#v body=%s", response.Code, stub.mutationInput, stub.actor, response.Body.String())
	}
}

func TestOperationsHTTPRejectsUnknownFieldsAndWrongContractBeforeService(t *testing.T) {
	stub := &operationsHTTPStub{}
	request := operationsRequest(t, planningport.OperationsQueryPath, map[string]any{"hostId": "host-a", "projectId": "project-a", "expectedProjectVersion": "3", "path": "/home/private"})
	response := httptest.NewRecorder()
	newOperationsHandler(stub).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || stub.queryInput.HostID != "" {
		t.Fatalf("unknown-field response=%d input=%#v", response.Code, stub.queryInput)
	}
	request = operationsRequest(t, planningport.OperationsQueryPath, planningport.OperationsQueryInput{HostID: "host-a", ProjectID: "project-a", ExpectedProjectVersion: "3"})
	request.Header.Set(contractHashHeader, "wrong")
	response = httptest.NewRecorder()
	newOperationsHandler(stub).ServeHTTP(response, request)
	if response.Code != http.StatusConflict || stub.queryInput.HostID != "" {
		t.Fatalf("contract response=%d input=%#v", response.Code, stub.queryInput)
	}
}
