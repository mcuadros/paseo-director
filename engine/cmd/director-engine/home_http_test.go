// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	homeapp "github.com/mcuadros/director-engine/application/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type homeReaderFunc func(context.Context, planningport.HomeQueryInput) (planningport.HomeSnapshot, error)

func (function homeReaderFunc) Query(ctx context.Context, input planningport.HomeQueryInput) (planningport.HomeSnapshot, error) {
	return function(ctx, input)
}

func homeHTTPRequest(t *testing.T, handler http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, planningport.HomeQueryPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	definition, _ := planningport.EmbeddedDefinition()
	hash, _ := planningport.SchemaSHA256()
	request.Header.Set(contractVersionHeader, definition.ContractVersion)
	request.Header.Set(contractHashHeader, hash)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestHomeHTTPBindsTheClosedEngineQuery(t *testing.T) {
	definition, _ := planningport.EmbeddedDefinition()
	hash, _ := planningport.SchemaSHA256()
	want := planningport.HomeSnapshot{SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion,
		ContractHash: hash, Cursor: "7", Page: planningport.HomePage{Host: planningport.HomeHost{ID: "host-a", Label: "Host A", InstanceID: "engine-a", State: "current", ObservedAt: "2026-09-11T16:00:00Z", MaximumAgeMillis: "30000"}, Projects: []planningport.HomeProject{}, SurfaceActions: []planningport.HomeAction{}, TotalProjects: "0"}}
	handler := newHomeHandler(homeReaderFunc(func(_ context.Context, input planningport.HomeQueryInput) (planningport.HomeSnapshot, error) {
		if input.HostID != "host-a" || input.PageSize != 25 || input.Cursor != nil {
			t.Fatalf("Home input = %#v", input)
		}
		return want, nil
	}))
	body, _ := json.Marshal(planningport.HomeQueryInput{HostID: "host-a", PageSize: 25})
	response := homeHTTPRequest(t, handler, body)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get(contractHashHeader) != hash {
		t.Fatalf("Home response = %d %#v %s", response.Code, response.Header(), response.Body.String())
	}
	var got planningport.HomeSnapshot
	if json.Unmarshal(response.Body.Bytes(), &got) != nil || got.Page.Host.ID != want.Page.Host.ID || got.Cursor != want.Cursor {
		t.Fatalf("Home response body = %#v", got)
	}
}

func TestHomeHTTPRejectsMalformedHostCursorAndContractWithoutFallback(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{homeapp.ErrHostMismatch, http.StatusConflict, "HOME_HOST_MISMATCH"},
		{homeapp.ErrCursorInvalid, http.StatusConflict, "HOME_CURSOR_INVALIDATED"},
		{planningport.ErrQueryInvalid, http.StatusBadRequest, "HOME_INPUT_INVALID"},
		{errors.New("offline"), http.StatusServiceUnavailable, "HOME_UNAVAILABLE"},
	} {
		handler := newHomeHandler(homeReaderFunc(func(context.Context, planningport.HomeQueryInput) (planningport.HomeSnapshot, error) {
			return planningport.HomeSnapshot{}, test.err
		}))
		response := homeHTTPRequest(t, handler, []byte(`{"hostId":"host-a","cursor":null,"pageSize":25}`))
		if response.Code != test.status || response.Body.String() != `{"code":"`+test.code+`"}`+"\n" {
			t.Fatalf("Home failure = %d %s", response.Code, response.Body.String())
		}
	}

	handler := newHomeHandler(homeReaderFunc(func(context.Context, planningport.HomeQueryInput) (planningport.HomeSnapshot, error) {
		t.Fatal("malformed Home reached reader")
		return planningport.HomeSnapshot{}, nil
	}))
	for _, body := range [][]byte{
		[]byte(`{"hostId":"host-a","cursor":null,"pageSize":25,"other":true}`),
		[]byte(`{"hostId":"host-a","hostId":"host-b","cursor":null,"pageSize":25}`),
		[]byte(`{"hostId":"host-a","cursor":null,"pageSize":51}`),
	} {
		if response := homeHTTPRequest(t, handler, body); response.Code != http.StatusBadRequest {
			t.Fatalf("malformed Home status = %d %s", response.Code, response.Body.String())
		}
	}
}
