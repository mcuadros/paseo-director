// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	homeapp "github.com/mcuadros/director-engine/application/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type doctorReaderFunction func(context.Context, planningport.DoctorQueryInput) (planningport.DoctorReport, error)

func (function doctorReaderFunction) Doctor(ctx context.Context, input planningport.DoctorQueryInput) (planningport.DoctorReport, error) {
	return function(ctx, input)
}

type repairServiceFunction func(context.Context, planningport.RepairInput, homeapp.AuthenticatedRepairActor) (planningport.RepairResult, error)

func (function repairServiceFunction) Repair(ctx context.Context, input planningport.RepairInput, actor homeapp.AuthenticatedRepairActor) (planningport.RepairResult, error) {
	return function(ctx, input, actor)
}

func doctorRepairRequest(t *testing.T, method, path string, value any) *http.Request {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	version, hash := planningContractIdentity()
	request.Header.Set(contractVersionHeader, version)
	request.Header.Set(contractHashHeader, hash)
	return request
}

func TestDoctorHTTPIsOneReadOnlyExactHostQuery(t *testing.T) {
	version, hash := planningContractIdentity()
	want := planningport.DoctorReport{SchemaVersion: 1, ContractVersion: version, ContractHash: hash, Cursor: "7",
		HostID: "host-a", HostInstanceID: "engine-a", ProjectID: "project-a", ProjectName: "Project A", ProjectVersion: "3",
		ObservationID: "a5e88ff4d296009fc19844b589bf0320086b426e79b9d088cbb587600d9e1774", ObservedAt: "2026-09-11T16:00:00Z",
		MaximumAgeMillis: "30000", Status: "healthy", ReadOnly: true, Assurance: "Doctor reads exact bounded engine facts and performs no mutation.",
		Checks:        []planningport.DoctorCheck{{ID: "taskstore-read", Category: "dynamic_state", Status: "passed", Code: "taskstore_read_current", Title: "Director TaskStore", Detail: "The exact read-only snapshot is current.", InstallationGuidance: []string{}}},
		BlockingCount: "0", Repair: planningport.DoctorRepairAvailability{Available: false, Reason: &planningport.Explanation{Code: "repair_unavailable", Message: "No exact repair effect is available"}}}
	calls := 0
	handler := newDoctorHandler(doctorReaderFunction(func(_ context.Context, input planningport.DoctorQueryInput) (planningport.DoctorReport, error) {
		calls++
		if input.HostID != "host-a" || input.ProjectID != "project-a" || input.ExpectedProjectVersion != "3" {
			t.Fatalf("Doctor input = %#v", input)
		}
		return want, nil
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, doctorRepairRequest(t, http.MethodPost, planningport.DoctorQueryPath, planningport.DoctorQueryInput{HostID: "host-a", ProjectID: "project-a", ExpectedProjectVersion: "3"}))
	if response.Code != http.StatusOK || calls != 1 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Doctor HTTP code=%d calls=%d headers=%v body=%s", response.Code, calls, response.Header(), response.Body.String())
	}
	var got planningport.DoctorReport
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.ObservationID != want.ObservationID || !got.ReadOnly {
		t.Fatalf("Doctor HTTP result = %#v, %v", got, err)
	}
}

func TestDoctorHTTPMapsStaleAndHostMismatchWithoutFallback(t *testing.T) {
	for name, failure := range map[string]struct {
		err  error
		code int
		body string
	}{
		"host":  {homeapp.ErrHostMismatch, http.StatusConflict, "DOCTOR_HOST_MISMATCH"},
		"stale": {homeapp.ErrDoctorVersionStale, http.StatusConflict, "DOCTOR_FACTS_STALE"},
	} {
		t.Run(name, func(t *testing.T) {
			handler := newDoctorHandler(doctorReaderFunction(func(context.Context, planningport.DoctorQueryInput) (planningport.DoctorReport, error) {
				return planningport.DoctorReport{}, failure.err
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, doctorRepairRequest(t, http.MethodPost, planningport.DoctorQueryPath, planningport.DoctorQueryInput{HostID: "host-a", ProjectID: "project-a", ExpectedProjectVersion: "3"}))
			if response.Code != failure.code || !bytes.Contains(response.Body.Bytes(), []byte(failure.body)) {
				t.Fatalf("Doctor refusal code=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRepairHTTPRequiresAuthenticatedHumanBeforeApply(t *testing.T) {
	hash, _ := planningport.SchemaSHA256()
	previewID := "a5e88ff4d296009fc19844b589bf0320086b426e79b9d088cbb587600d9e1774"
	input := planningport.RepairInput{SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash, HostID: "host-a",
		RequestID: "repair-request-0001", Kind: "repair.apply", ProjectID: "project-a", ExpectedProjectVersion: "3", PreviewID: &previewID, Confirmed: true}
	calls := 0
	handler := newRepairHandler(repairServiceFunction(func(_ context.Context, got planningport.RepairInput, actor homeapp.AuthenticatedRepairActor) (planningport.RepairResult, error) {
		calls++
		if !reflect.DeepEqual(got, input) || !actor.Authenticated || actor.Kind != "human" || actor.ID != "owner" || actor.SessionID != "session" {
			t.Fatalf("Repair binding=%#v actor=%#v", got, actor)
		}
		version := "4"
		return planningport.RepairResult{SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash,
			HostID: input.HostID, ProjectID: input.ProjectID, Cursor: "8", RequestID: input.RequestID, Status: "applied", Message: "Exact Repair applied", ProjectVersion: &version}, nil
	}))

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, doctorRepairRequest(t, http.MethodPost, planningport.RepairMutationPath, input))
	if unauthorized.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("unauthenticated Repair code=%d calls=%d", unauthorized.Code, calls)
	}

	request := doctorRepairRequest(t, http.MethodPost, planningport.RepairMutationPath, input)
	request.Header.Set("x-director-actor-kind", "human")
	request.Header.Set("x-director-actor-id", "owner")
	request.Header.Set("x-director-actor-session", "session")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls != 1 || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"applied"`)) {
		t.Fatalf("authenticated Repair code=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
}

func TestRepairHTTPRefusesUnknownOutcomeWithoutLeakingError(t *testing.T) {
	hash, _ := planningport.SchemaSHA256()
	input := planningport.RepairInput{SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash, HostID: "host-a",
		RequestID: "repair-request-0001", Kind: "repair.preview", ProjectID: "project-a", ExpectedProjectVersion: "3", Confirmed: false}
	handler := newRepairHandler(repairServiceFunction(func(context.Context, planningport.RepairInput, homeapp.AuthenticatedRepairActor) (planningport.RepairResult, error) {
		return planningport.RepairResult{}, errors.New("/private/path token=secret")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, doctorRepairRequest(t, http.MethodPost, planningport.RepairMutationPath, input))
	if response.Code != http.StatusServiceUnavailable || bytes.Contains(response.Body.Bytes(), []byte("private")) || bytes.Contains(response.Body.Bytes(), []byte("secret")) {
		t.Fatalf("Repair error leaked: code=%d body=%s", response.Code, response.Body.String())
	}
}
