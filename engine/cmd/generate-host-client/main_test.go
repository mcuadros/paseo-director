// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"
)

const testBoardQuery = `"boardQuery":{"name":"board.snapshot","method":"GET","path":"/v1/board","schemaVersion":1,"maximumTasks":1000,"maximumBytes":2097152,"states":["needs_you","queued","building","validating","in_review","ready"]}`

const testWorkerRegistry = `"workerRegistry":{"schemaVersion":1,"roles":["task-agent","reviewer"],"labels":{"project":"director.project","rootWorkspace":"director.root-workspace","workspace":"director.workspace","executionWorkspace":"director.execution-workspace","task":"director.task","run":"director.run","role":"director.role","phase":"director.phase","candidate":"director.candidate","base":"director.base","effect":"director.effect","profile":"director.profile","session":"director.session","registeredAt":"director.registered-at","startedAt":"director.started-at"}}`

const testHostVocabularies = `"command":{"arguments":{"effectKinds":["host_view.create","task_agent.create_with_bootstrap","reviewer_agent.create_with_bootstrap","agent.send_prompt","primary_recovery.observe","helper_agent.observe","helper_agent.archive","control_agent.observe_safe_boundary","control_agent.archive","task_agent.archive","reviewer_agent.archive","host_view.archive"]}},"observation":{"result":{"statuses":["desired","absent","owned_present","errored","permission","different","ambiguous","unavailable"]}}`

func TestRenderIsDeterministicAndCarriesContract(t *testing.T) {
	schema := []byte("{\n  \"schemaVersion\": 1,\n  \"contractVersion\": \"test/v1\",\n  \"credentialScope\": \"scope\",\n  \"capabilities\": [\"one\", \"two\"],\n  " + testBoardQuery + ",\n  " + testWorkerRegistry + ",\n  " + testHostVocabularies + "\n}\n")
	first, err := render(schema)
	if err != nil {
		t.Fatal(err)
	}
	second, err := render(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("render output is not deterministic")
	}
	for _, expected := range [][]byte{
		[]byte(`HOST_CONTRACT_VERSION = "test/v1"`),
		[]byte(`"one"`),
		[]byte(`"two"`),
		[]byte(`HOST_CONTRACT_SHA256`),
		[]byte(`interface HostCommandArguments`),
		[]byte(`interface HostObservationResult`),
		[]byte(`BOARD_QUERY_PATH = "/v1/board"`),
		[]byte(`assertBoardSnapshot`),
		[]byte(`WORKER_REGISTRY_SCHEMA_VERSION = 1`),
		[]byte(`HOST_EFFECT_KINDS = [`),
		[]byte(`HOST_OBSERVATION_STATUSES = [`),
		[]byte(`rootWorkspace: "director.root-workspace"`),
		[]byte(`"task-agent"`),
		[]byte(`labels?: Readonly<Record<string, string>>`),
		[]byte(`notifyOnFinish?: boolean`),
		[]byte(`sessionBindingSha256?: string`),
		[]byte(`session: "director.session"`),
		[]byte(`"agent.send_prompt",`),
	} {
		if !bytes.Contains(first, expected) {
			t.Fatalf("render output does not contain %q", expected)
		}
	}
	if bytes.Contains(first, []byte(`arguments: Readonly<Record<string, unknown>>`)) || bytes.Contains(first, []byte(`result: unknown`)) {
		t.Fatal("generated host client retained an untyped command or observation boundary")
	}
}

func TestRenderIgnoresSchemaWhitespaceAndObjectKeyOrder(t *testing.T) {
	first := []byte(`{"schemaVersion":1,"contractVersion":"test/v1","credentialScope":"scope","capabilities":["one","two"],` + testBoardQuery + `,` + testWorkerRegistry + `,` + testHostVocabularies + `}`)
	second := []byte(`{
		"observation": {"result":{"statuses":["desired","absent","owned_present","errored","permission","different","ambiguous","unavailable"]}},
		"command": {"arguments":{"effectKinds":["host_view.create","task_agent.create_with_bootstrap","reviewer_agent.create_with_bootstrap","agent.send_prompt","primary_recovery.observe","helper_agent.observe","helper_agent.archive","control_agent.observe_safe_boundary","control_agent.archive","task_agent.archive","reviewer_agent.archive","host_view.archive"]}},
		"workerRegistry": {"labels":{"startedAt":"director.started-at","registeredAt":"director.registered-at","session":"director.session","profile":"director.profile","effect":"director.effect","base":"director.base","candidate":"director.candidate","phase":"director.phase","role":"director.role","run":"director.run","task":"director.task","executionWorkspace":"director.execution-workspace","workspace":"director.workspace","rootWorkspace":"director.root-workspace","project":"director.project"},"roles":["task-agent","reviewer"],"schemaVersion":1},
		"boardQuery": {"states":["needs_you","queued","building","validating","in_review","ready"],"maximumBytes":2097152,"maximumTasks":1000,"schemaVersion":1,"path":"/v1/board","method":"GET","name":"board.snapshot"},
		"capabilities": ["one", "two"],
		"credentialScope": "scope",
		"contractVersion": "test/v1",
		"schemaVersion": 1
	}`)
	firstOutput, err := render(first)
	if err != nil {
		t.Fatal(err)
	}
	secondOutput, err := render(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstOutput, secondOutput) {
		t.Fatal("generated client changed for equivalent schema formatting")
	}
}

func TestRenderRejectsDuplicateSchemaKeys(t *testing.T) {
	schema := []byte(`{"schemaVersion":1,"schemaVersion":1,"contractVersion":"test/v1","credentialScope":"scope","capabilities":["one"],` + testBoardQuery + `,` + testWorkerRegistry + `,` + testHostVocabularies + `}`)
	if _, err := render(schema); err == nil {
		t.Fatal("render() accepted duplicate schema keys")
	}
}

func TestRenderRejectsSchemaVocabularyDriftFromGo(t *testing.T) {
	base := []byte(`{"schemaVersion":1,"contractVersion":"test/v1","credentialScope":"scope","capabilities":["one"],` + testBoardQuery + `,` + testWorkerRegistry + `,` + testHostVocabularies + `}`)
	for name, changed := range map[string][]byte{
		"effect kind":        bytes.Replace(base, []byte(`"agent.send_prompt"`), []byte(`"agent.send_changed_prompt"`), 1),
		"observation status": bytes.Replace(base, []byte(`"unavailable"`), []byte(`"unknown"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := render(changed); err == nil {
				t.Fatal("render() accepted schema vocabulary drift from Go")
			}
		})
	}
}
