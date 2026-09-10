// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	planningport "github.com/mcuadros/director-engine/ports/planning"
	"github.com/mcuadros/director-engine/projection"
)

type planningReaderStub struct {
	snapshot planningport.Snapshot
	err      error
	calls    int
	input    planningport.QueryInput
}

func (reader *planningReaderStub) Query(_ context.Context, input planningport.QueryInput) (planningport.Snapshot, error) {
	reader.calls++
	reader.input = input
	return reader.snapshot, reader.err
}

func planningRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, planningport.QueryPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(contractVersionHeader, definition.ContractVersion)
	request.Header.Set(contractHashHeader, hash)
	return request
}

const validPlanningQuery = `{"projectId":null,"workspaceIds":[],"epicIds":[],"states":[],"priorities":[],"labels":[],"attention":[],"search":null,"sort":"scheduler_order","cursor":null,"pageSize":100}`

func TestPlanningHandlerReturnsOnlyContractBoundEnginePage(t *testing.T) {
	reader := &planningReaderStub{snapshot: planningport.Snapshot{
		SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: strings.Repeat("a", 64),
		Cursor: "7", Page: planningport.Page{
			SelectedWorkspaceIDs: []string{}, SelectedEpicIDs: []string{},
			AppliedQuery: planningport.QueryInput{
				WorkspaceIDs: []string{}, EpicIDs: []string{}, States: []string{}, Priorities: []string{},
				Labels: []string{}, Attention: []string{},
			},
			AvailableSorts: []string{"scheduler_order"}, AvailableLabels: []string{}, Projects: []planningport.ProjectSummary{},
			Workspaces: []planningport.WorkspaceSummary{}, Epics: []planningport.EpicSummary{}, Tasks: []planningport.TaskSummary{},
			SurfaceActions: []planningport.AllowedAction{}, TotalTasks: "0",
		},
	}}
	recorder := httptest.NewRecorder()
	newPlanningHandler(reader).ServeHTTP(recorder, planningRequest(t, validPlanningQuery))
	if recorder.Code != http.StatusOK || reader.calls != 1 || reader.input.PageSize != 100 {
		t.Fatalf("planning response status=%d calls=%d input=%#v", recorder.Code, reader.calls, reader.input)
	}
	var snapshot planningport.Snapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil || snapshot.Cursor != "7" {
		t.Fatalf("planning response = %#v, %v", snapshot, err)
	}
}

func TestPlanningHandlerRejectsDriftBeforeReading(t *testing.T) {
	reader := &planningReaderStub{}
	requests := []*http.Request{
		planningRequest(t, `{}`),
		planningRequest(t, strings.Replace(validPlanningQuery, `"pageSize":100`, `"pageSize":100,"extra":true`, 1)),
		planningRequest(t, strings.Replace(validPlanningQuery, `"pageSize":100`, `"pageSize":100,"pageSize":99`, 1)),
		planningRequest(t, strings.Replace(validPlanningQuery, `"workspaceIds":[]`, `"workspaceIds":null`, 1)),
		planningRequest(t, validPlanningQuery),
		planningRequest(t, validPlanningQuery),
	}
	requests[4].Method = http.MethodGet
	requests[5].Header.Set(contractHashHeader, strings.Repeat("0", 64))
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		newPlanningHandler(reader).ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK {
			t.Fatalf("drift request unexpectedly succeeded: %s", recorder.Body.String())
		}
	}
	if reader.calls != 0 {
		t.Fatalf("planning reader called %d times", reader.calls)
	}
}

func TestPlanningHandlerClassifiesCursorAndRedactsFailures(t *testing.T) {
	for _, scenario := range []struct {
		err    error
		status int
		body   string
	}{
		{projection.ErrTaskQueryCursorSnapshot, http.StatusConflict, "{\"code\":\"PLANNING_CURSOR_INVALIDATED\"}\n"},
		{errors.New("password=do-not-leak"), http.StatusServiceUnavailable, "{\"code\":\"PLANNING_UNAVAILABLE\"}\n"},
	} {
		reader := &planningReaderStub{err: scenario.err}
		recorder := httptest.NewRecorder()
		newPlanningHandler(reader).ServeHTTP(recorder, planningRequest(t, validPlanningQuery))
		if recorder.Code != scenario.status || recorder.Body.String() != scenario.body {
			t.Fatalf("planning failure = %d %q", recorder.Code, recorder.Body.String())
		}
	}

	reader := &planningReaderStub{snapshot: planningport.Snapshot{
		Page: planningport.Page{Projects: []planningport.ProjectSummary{{Name: strings.Repeat("x", planningport.MaximumResponseBytes)}}},
	}}
	recorder := httptest.NewRecorder()
	newPlanningHandler(reader).ServeHTTP(recorder, planningRequest(t, validPlanningQuery))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("oversize planning response status = %d", recorder.Code)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte(strings.Repeat("x", 128))) {
		t.Fatal("oversize planning response leaked its body")
	}
}
