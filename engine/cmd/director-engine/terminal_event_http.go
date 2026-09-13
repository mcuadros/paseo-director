// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/ports/host"
)

const terminalEventPath = "/v1/host/terminal-event"

type terminalEventStore interface {
	Run(context.Context, string) (domain.Run, error)
}

type terminalEventInput struct {
	SchemaVersion    int                                 `json:"schemaVersion"`
	EventID          string                              `json:"eventId"`
	Kind             domainexecution.CompletionEventKind `json:"kind"`
	RunID            string                              `json:"runId"`
	AgentID          string                              `json:"agentId"`
	Cursor           uint64                              `json:"cursor"`
	ObservedAtMillis int64                               `json:"observedAtMillis"`
}

type terminalEventHandler struct {
	store      terminalEventStore
	controller *executionapp.Controller
	production *Production
	version    string
	hash       string
	now        func() int64
}

func newTerminalEventHandler(store terminalEventStore, controller *executionapp.Controller, production *Production) http.Handler {
	definition, err := host.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	hash, err := host.SchemaSHA256()
	if err != nil {
		panic(err)
	}
	return &terminalEventHandler{store: store, controller: controller, production: production, version: definition.ContractVersion,
		hash: hash, now: func() int64 { return time.Now().UnixMilli() }}
}

func decodeTerminalEvent(request *http.Request) (terminalEventInput, error) {
	if request.ContentLength > 16*1024 {
		return terminalEventInput{}, errors.New("terminal event is invalid")
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, 16*1024+1))
	if err != nil || len(content) == 0 || len(content) > 16*1024 {
		return terminalEventInput{}, errors.New("terminal event is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var input terminalEventInput
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.SchemaVersion != 1 ||
		input.EventID == "" || input.RunID == "" || input.AgentID == "" || input.Cursor == 0 || input.ObservedAtMillis < 0 {
		return terminalEventInput{}, errors.New("terminal event is invalid")
	}
	return input, nil
}

func (handler *terminalEventHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != terminalEventPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" ||
		request.Header.Get("X-Director-Contract-Version") != handler.version || request.Header.Get("X-Director-Contract-Hash") != handler.hash {
		writePlanningError(response, http.StatusConflict, "TERMINAL_EVENT_CONTRACT_MISMATCH")
		return
	}
	input, err := decodeTerminalEvent(request)
	if err != nil {
		writePlanningError(response, http.StatusBadRequest, "TERMINAL_EVENT_INVALID")
		return
	}
	run, err := handler.store.Run(request.Context(), input.RunID)
	if err != nil {
		writePlanningError(response, http.StatusConflict, "TERMINAL_EVENT_MISBOUND")
		return
	}
	dispatchEffectID, bindingHash, external := run.Execution.AgentPrompt.ID, run.Execution.RepositoryBindingHash, ""
	if run.Execution.Agent.ExternalID != input.AgentID {
		if run.Execution.Review == nil || run.Execution.Review.ReviewerUUID != input.AgentID || run.Execution.Review.Prompt.ID == "" {
			writePlanningError(response, http.StatusConflict, "TERMINAL_EVENT_MISBOUND")
			return
		}
		dispatchEffectID, bindingHash, external = run.Execution.Review.Prompt.ID, run.Execution.Review.Binding.BindingSHA256, "review"
	} else if run.Execution.Correction != nil && run.Execution.Correction.Phase == domaincorrection.PhasePrompt &&
		len(run.Execution.Correction.Attempts) > 0 {
		attempt := run.Execution.Correction.Attempts[len(run.Execution.Correction.Attempts)-1]
		batch, ok := domaincorrection.CurrentBatch(*run.Execution.Correction)
		if ok && attempt.Prompt.Phase == domaincorrection.EffectDispatching {
			dispatchEffectID, bindingHash, external = attempt.Prompt.ID, batch.SHA256, "correction"
		}
	}
	if run.Execution.Agent.ExternalID == input.AgentID && run.Execution.Correction != nil {
		for _, receipt := range run.Execution.CompletionEventReceipts {
			if receipt.EventID != input.EventID || receipt.Event.AgentID != input.AgentID {
				continue
			}
			for _, attempt := range run.Execution.Correction.Attempts {
				if attempt.Prompt.ID == receipt.Event.DispatchEffectID {
					dispatchEffectID, bindingHash, external = receipt.Event.DispatchEffectID, receipt.Event.BindingHash, "correction"
				}
			}
		}
	}
	event := domainexecution.CompletionEvent{ID: input.EventID, Kind: input.Kind, AgentID: input.AgentID,
		DispatchEffectID: dispatchEffectID, Cursor: input.Cursor, ObservedAtMillis: input.ObservedAtMillis,
		BindingHash: bindingHash}
	event.FactHash = domainexecution.CompletionEventHash(event)
	if external == "review" {
		err = handler.production.RecordReviewCompletionEvent(request.Context(), run.ID, event, handler.now())
	} else if external == "correction" {
		err = handler.production.RecordCorrectionCompletionEvent(request.Context(), run.ID, event, handler.now())
	} else {
		err = handler.controller.RecordCompletionEvent(request.Context(), run.ID, event, handler.now())
	}
	if err != nil {
		writePlanningError(response, http.StatusConflict, "TERMINAL_EVENT_REFUSED")
		return
	}
	writePlanningJSON(response, handler.version, handler.hash, struct {
		EventID string `json:"eventId"`
		Status  string `json:"status"`
	}{EventID: event.ID, Status: "enqueued"}, true)
}
