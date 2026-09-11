// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type taskDetailReaderStub struct {
	calls    int
	input    planningport.TaskDetailQueryInput
	snapshot planningport.TaskDetailSnapshot
}

func (reader *taskDetailReaderStub) TaskDetail(_ context.Context, input planningport.TaskDetailQueryInput) (planningport.TaskDetailSnapshot, error) {
	reader.calls++
	reader.input = input
	return reader.snapshot, nil
}

func taskDetailRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, planningport.TaskDetailQueryPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(contractVersionHeader, definition.ContractVersion)
	request.Header.Set(contractHashHeader, hash)
	return request
}

const validTaskDetailQuery = `{"hostId":"host-a","context":"board","taskId":"task-1","paseoWorkspaceId":null,"paseoAgentId":null,"afterCursor":null}`

func TestTaskDetailHandlerBindsTheConfiguredHost(t *testing.T) {
	reader := &taskDetailReaderStub{snapshot: planningport.TaskDetailSnapshot{
		SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: strings.Repeat("a", 64), HostID: "host-a", Cursor: "7",
	}}
	recorder := httptest.NewRecorder()
	newTaskDetailHandler(reader, "host-a").ServeHTTP(recorder, taskDetailRequest(t, validTaskDetailQuery))
	if recorder.Code != http.StatusOK || reader.calls != 1 || reader.input.HostID != "host-a" || reader.input.TaskID == nil || *reader.input.TaskID != "task-1" {
		t.Fatalf("Task detail response status=%d calls=%d input=%#v", recorder.Code, reader.calls, reader.input)
	}
	var snapshot planningport.TaskDetailSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil || snapshot.Cursor != "7" {
		t.Fatalf("Task detail response = %#v, %v", snapshot, err)
	}
}

func TestTaskDetailHandlerRejectsWrongHostAndShapeBeforeReading(t *testing.T) {
	reader := &taskDetailReaderStub{}
	for _, body := range []string{
		strings.Replace(validTaskDetailQuery, `"host-a"`, `"host-b"`, 1),
		strings.Replace(validTaskDetailQuery, `"taskId":"task-1"`, `"taskId":null`, 1),
		strings.Replace(validTaskDetailQuery, `"afterCursor":null`, `"afterCursor":null,"extra":true`, 1),
	} {
		recorder := httptest.NewRecorder()
		newTaskDetailHandler(reader, "host-a").ServeHTTP(recorder, taskDetailRequest(t, body))
		if recorder.Code == http.StatusOK {
			t.Fatalf("invalid Task detail query succeeded: %s", body)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("Task detail reader called %d times", reader.calls)
	}
}
