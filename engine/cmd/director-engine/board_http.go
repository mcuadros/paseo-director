// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/mcuadros/director-engine/ports/host"
	"github.com/mcuadros/director-engine/projection"
)

const (
	contractVersionHeader = "X-Director-Contract-Version"
	contractHashHeader    = "X-Director-Contract-Hash"
)

var boardContract = func() host.BoardQueryDefinition {
	definition, err := host.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	query := definition.BoardQuery
	states := projection.BoardStates()
	if query.SchemaVersion != projection.BoardSchemaVersion ||
		query.MaximumTasks != projection.MaximumBoardTasks ||
		query.MaximumBytes != projection.MaximumBoardBytes ||
		!slices.EqualFunc(query.States, states, func(contract string, state projection.BoardState) bool {
			return contract == string(state)
		}) {
		panic("Board contract and engine projection differ")
	}
	return query
}()

var boardQueryPath = boardContract.Path

type boardReader interface {
	Read(context.Context) (projection.Board, error)
}

type boardHandler struct {
	reader     boardReader
	descriptor host.Descriptor
}

func newBoardHandler(reader boardReader) http.Handler {
	descriptor, err := host.ExpectedDescriptor()
	if err != nil {
		panic(err)
	}
	return &boardHandler{reader: reader, descriptor: descriptor}
}

func (handler *boardHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != boardQueryPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != boardContract.Method {
		response.Header().Set("Allow", boardContract.Method)
		writeBoardError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if request.ContentLength > 0 || len(request.TransferEncoding) > 0 {
		writeBoardError(response, http.StatusBadRequest, "BOARD_INPUT_INVALID")
		return
	}
	if request.Header.Get(contractVersionHeader) != handler.descriptor.ContractVersion ||
		request.Header.Get(contractHashHeader) != handler.descriptor.ContractHash {
		writeBoardError(response, http.StatusConflict, "CONTRACT_MISMATCH")
		return
	}
	snapshot, err := handler.reader.Read(request.Context())
	if err != nil {
		writeBoardError(response, http.StatusServiceUnavailable, "BOARD_UNAVAILABLE")
		return
	}
	encoded, safe := encodeSafeBoundaryJSON(snapshot, boardContract.MaximumBytes, true)
	if !safe {
		writeBoardError(response, http.StatusServiceUnavailable, "BOARD_UNAVAILABLE")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set(contractVersionHeader, handler.descriptor.ContractVersion)
	response.Header().Set(contractHashHeader, handler.descriptor.ContractHash)
	_, _ = response.Write(encoded)
}

func writeBoardError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(struct {
		Code string `json:"code"`
	}{Code: code})
}
