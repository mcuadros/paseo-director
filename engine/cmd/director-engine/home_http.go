// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	homeapp "github.com/mcuadros/director-engine/application/home"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type homeReader interface {
	Query(context.Context, planningport.HomeQueryInput) (planningport.HomeSnapshot, error)
}

type homeHandler struct {
	reader          homeReader
	contractVersion string
	contractHash    string
}

func newHomeHandler(reader homeReader) http.Handler {
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		panic(err)
	}
	return &homeHandler{reader: reader, contractVersion: definition.ContractVersion, contractHash: hash}
}

func decodeHomeQuery(request *http.Request) (planningport.HomeQueryInput, error) {
	if request.ContentLength > planningport.MaximumRequestBytes {
		return planningport.HomeQueryInput{}, planningport.ErrQueryInvalid
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, planningport.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > planningport.MaximumRequestBytes {
		return planningport.HomeQueryInput{}, planningport.ErrQueryInvalid
	}
	content, err = jsondocument.CanonicalWithNormalizedNumbersLimit(content, planningport.MaximumRequestBytes)
	if err != nil {
		return planningport.HomeQueryInput{}, planningport.ErrQueryInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var input planningport.HomeQueryInput
	if decoder.Decode(&input) != nil {
		return planningport.HomeQueryInput{}, planningport.ErrQueryInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return planningport.HomeQueryInput{}, planningport.ErrQueryInvalid
	}
	return input, planningport.ValidateHomeQuery(input)
}

func (handler *homeHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.HomeQueryPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writePlanningError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		writePlanningError(response, http.StatusBadRequest, "HOME_INPUT_INVALID")
		return
	}
	if request.Header.Get(contractVersionHeader) != handler.contractVersion ||
		request.Header.Get(contractHashHeader) != handler.contractHash {
		writePlanningError(response, http.StatusConflict, "CONTRACT_MISMATCH")
		return
	}
	input, err := decodeHomeQuery(request)
	if err != nil {
		writePlanningError(response, http.StatusBadRequest, "HOME_INPUT_INVALID")
		return
	}
	snapshot, err := handler.reader.Query(request.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, homeapp.ErrHostMismatch):
			writePlanningError(response, http.StatusConflict, "HOME_HOST_MISMATCH")
		case errors.Is(err, homeapp.ErrCursorInvalid):
			writePlanningError(response, http.StatusConflict, "HOME_CURSOR_INVALIDATED")
		case errors.Is(err, planningport.ErrQueryInvalid):
			writePlanningError(response, http.StatusBadRequest, "HOME_INPUT_INVALID")
		default:
			writePlanningError(response, http.StatusServiceUnavailable, "HOME_UNAVAILABLE")
		}
		return
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(snapshot) != nil || encoded.Len() > planningport.MaximumResponseBytes {
		writePlanningError(response, http.StatusServiceUnavailable, "HOME_UNAVAILABLE")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set(contractVersionHeader, handler.contractVersion)
	response.Header().Set(contractHashHeader, handler.contractHash)
	_, _ = response.Write(encoded.Bytes())
}
