// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/ports/host"
	"github.com/mcuadros/director-engine/projection"
)

type boardReaderStub struct {
	snapshot projection.Board
	err      error
	calls    int
}

func (reader *boardReaderStub) Read(context.Context) (projection.Board, error) {
	reader.calls++
	return reader.snapshot, reader.err
}

func boardRequest(t *testing.T, method, path string) *http.Request {
	t.Helper()
	descriptor, err := host.ExpectedDescriptor()
	if err != nil {
		t.Fatalf("ExpectedDescriptor() error = %v", err)
	}
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set(contractVersionHeader, descriptor.ContractVersion)
	request.Header.Set(contractHashHeader, descriptor.ContractHash)
	return request
}

func TestBoardHandlerReturnsOnlyEngineSnapshot(t *testing.T) {
	reader := &boardReaderStub{snapshot: projection.Board{
		SchemaVersion: 1,
		Cursor:        "7",
		Tasks: []projection.BoardTask{{
			ID: "task-1", ProjectID: "project-1", ProjectName: "Director",
			Title: "Build board", State: projection.StateQueued,
		}},
	}}
	recorder := httptest.NewRecorder()
	newBoardHandler(reader).ServeHTTP(recorder, boardRequest(t, http.MethodGet, boardQueryPath))

	if recorder.Code != http.StatusOK || reader.calls != 1 {
		t.Fatalf("response status = %d, reader calls = %d", recorder.Code, reader.calls)
	}
	descriptor, _ := host.ExpectedDescriptor()
	if recorder.Header().Get(contractVersionHeader) != descriptor.ContractVersion || recorder.Header().Get(contractHashHeader) != descriptor.ContractHash {
		t.Fatalf("response contract headers = %#v", recorder.Header())
	}
	var snapshot projection.Board
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].State != projection.StateQueued {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestBoardHandlerFailsClosedBeforeReadingOnContractDrift(t *testing.T) {
	reader := &boardReaderStub{}
	for _, request := range []*http.Request{
		boardRequest(t, http.MethodPost, boardQueryPath),
		boardRequest(t, http.MethodGet, "/wrong"),
		boardRequest(t, http.MethodGet, boardQueryPath+"?project=client-owned"),
		boardRequest(t, http.MethodGet, boardQueryPath),
	} {
		if request.URL.Path == boardQueryPath && request.Method == http.MethodGet {
			request.Header.Set(contractHashHeader, "0")
		}
		recorder := httptest.NewRecorder()
		newBoardHandler(reader).ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK {
			t.Fatalf("request unexpectedly succeeded: %s %s", request.Method, request.URL.Path)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("reader called %d times", reader.calls)
	}
}

func TestBoardHandlerRedactsStoreFailure(t *testing.T) {
	reader := &boardReaderStub{err: errors.New("password=do-not-leak")}
	recorder := httptest.NewRecorder()
	newBoardHandler(reader).ServeHTTP(recorder, boardRequest(t, http.MethodGet, boardQueryPath))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("response status = %d", recorder.Code)
	}
	if recorder.Body.String() != "{\"code\":\"BOARD_UNAVAILABLE\"}\n" {
		t.Fatalf("response body = %q", recorder.Body.String())
	}
}

func TestBoardHandlerSuppressesUnsafeProjectionText(t *testing.T) {
	canary := "Authorization: Bearer abcdefghijklmnop"
	reader := &boardReaderStub{snapshot: projection.Board{SchemaVersion: 1, Cursor: "1", Tasks: []projection.BoardTask{{
		ID: "task-1", ProjectID: "project-1", ProjectName: "Director", Title: canary, State: projection.StateQueued,
	}}}}
	recorder := httptest.NewRecorder()
	newBoardHandler(reader).ServeHTTP(recorder, boardRequest(t, http.MethodGet, boardQueryPath))
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), canary) {
		t.Fatalf("unsafe Board output = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestBoardHandlerFitsWorstCaseAdmittedTextWithinTheContract(t *testing.T) {
	tasks := make([]projection.BoardTask, projection.MaximumBoardTasks)
	escapableText := strings.Repeat("<>&", 170) + "<>"
	for index := range tasks {
		tasks[index] = projection.BoardTask{
			ID: fmt.Sprintf("task-%04d", index), ProjectID: "project-1",
			ProjectName: escapableText, Title: escapableText,
			State: projection.StateQueued,
		}
	}
	recorder := httptest.NewRecorder()
	newBoardHandler(&boardReaderStub{snapshot: projection.Board{
		SchemaVersion: projection.BoardSchemaVersion, Cursor: "1", Tasks: tasks,
	}}).ServeHTTP(recorder, boardRequest(t, http.MethodGet, boardQueryPath))
	if recorder.Code != http.StatusOK {
		t.Fatalf("worst-case response status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() > projection.MaximumBoardBytes {
		t.Fatalf("worst-case response bytes = %d", recorder.Body.Len())
	}
	if strings.Contains(recorder.Body.String(), `\u003c`) ||
		strings.Contains(recorder.Body.String(), `\u003e`) ||
		strings.Contains(recorder.Body.String(), `\u0026`) {
		t.Fatal("Board encoder retained HTML escaping")
	}
}
