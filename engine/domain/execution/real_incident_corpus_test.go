// SPDX-License-Identifier: Apache-2.0

package execution

import (
	_ "embed"
	"encoding/json"
	"testing"
)

//go:embed testdata/real-incident-corpus.v1.json
var realIncidentCorpusJSON []byte

type realIncidentCorpus struct {
	SchemaVersion string                `json:"schemaVersion"`
	Fixtures      []realIncidentFixture `json:"fixtures"`
	Findings      []struct {
		Code            string `json:"code"`
		BehaviorChanged bool   `json:"behaviorChanged"`
	} `json:"divergenceFindings"`
}

type realIncidentFixture struct {
	ID              string `json:"id"`
	OccurrenceCount int    `json:"occurrenceCount"`
	ClassifierProbe *struct {
		ExpectedConnectorSignals []ProviderFailureSignal `json:"expectedConnectorSignals"`
		ExpectedEngineClass      ProviderFailureClass    `json:"expectedEngineClass"`
	} `json:"classifierProbe"`
	RecoveryScenarios []struct {
		Name                       string                     `json:"name"`
		AgentStatus                string                     `json:"agentStatus"`
		ArchivedAtPresent          bool                       `json:"archivedAtPresent"`
		OriginalAgentProcessAbsent bool                       `json:"originalAgentProcessAbsent"`
		ControlBlocksDispatch      bool                       `json:"controlBlocksDispatch"`
		ExpectedDisposition        PrimaryRecoveryDisposition `json:"expectedDisposition"`
		ExpectedFailureClass       ProviderFailureClass       `json:"expectedFailureClass"`
		ExpectedCode               NeedCode                   `json:"expectedCode"`
	} `json:"recoveryScenarios"`
}

func loadRealIncidentCorpus(t *testing.T) realIncidentCorpus {
	t.Helper()
	var corpus realIncidentCorpus
	if err := json.Unmarshal(realIncidentCorpusJSON, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.SchemaVersion != "director.real-incident-corpus/v1" {
		t.Fatalf("unexpected corpus schema %q", corpus.SchemaVersion)
	}
	return corpus
}

func rehashRealIncidentFacts(facts *PrimaryRecoveryFacts) {
	facts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(
		facts.Recovery.Observation.Runtime,
	)
	facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
	facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)
}

func TestRealIncidentCorpusDrivesProviderClassAndPrimaryRecovery(t *testing.T) {
	corpus := loadRealIncidentCorpus(t)
	exercised := 0
	for _, fixture := range corpus.Fixtures {
		if fixture.ClassifierProbe == nil {
			if len(fixture.RecoveryScenarios) != 0 {
				t.Fatalf("%s has recovery scenarios without a classifier probe", fixture.ID)
			}
			continue
		}
		exercised++
		signals := fixture.ClassifierProbe.ExpectedConnectorSignals
		if got := ClassifyProviderFailure(signals); got != fixture.ClassifierProbe.ExpectedEngineClass {
			t.Fatalf("%s class = %s, want %s", fixture.ID, got, fixture.ClassifierProbe.ExpectedEngineClass)
		}
		for _, scenario := range fixture.RecoveryScenarios {
			facts := recoveryFactsForTest(ProviderFailureUnknown)
			facts.ControlBlocksDispatch = scenario.ControlBlocksDispatch
			agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
			agent.FailureSignals = append([]ProviderFailureSignal(nil), signals...)
			agent.Status = scenario.AgentStatus
			agent.ArchivedAtPresent = scenario.ArchivedAtPresent
			facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = scenario.OriginalAgentProcessAbsent
			rehashRealIncidentFacts(&facts)
			decision := EvaluatePrimaryRecovery(facts)
			if decision.Disposition != scenario.ExpectedDisposition ||
				decision.FailureClass != scenario.ExpectedFailureClass || decision.Code != scenario.ExpectedCode {
				t.Fatalf(
					"%s/%s decision = (%s, %s, %s), want (%s, %s, %s)",
					fixture.ID, scenario.Name, decision.Disposition, decision.FailureClass, decision.Code,
					scenario.ExpectedDisposition, scenario.ExpectedFailureClass, scenario.ExpectedCode,
				)
			}
		}
	}
	if exercised != 6 {
		t.Fatalf("exercised %d fixture probes, want 6", exercised)
	}
}

func TestFindingF002ResolvedTerminalSignalAdoptsResumableSession(t *testing.T) {
	for _, signal := range []ProviderFailureSignal{
		ProviderFailureTerminal,
		ProviderFailurePolicy,
		ProviderFailureAuthentication,
		ProviderFailureConfiguration,
	} {
		facts := recoveryFactsForTest(signal)
		agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
		agent.Status = "running"
		agent.ArchivedAtPresent = false
		facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = false
		rehashRealIncidentFacts(&facts)
		if decision := EvaluatePrimaryRecovery(facts); decision.Disposition != RecoveryDispositionAdoptExisting {
			t.Fatalf("DIR-M5.13-F002 resolved: terminal signal %s want adopt_existing, got %s (%s)", signal, decision.Disposition, decision.Code)
		}
	}
}

func TestRealIncidentDivergencesAreCodedWithF001AndF002Resolution(t *testing.T) {
	corpus := loadRealIncidentCorpus(t)
	want := map[string]bool{"DIR-M5.13-F001": true, "DIR-M5.13-F002": true}
	for _, finding := range corpus.Findings {
		changed, ok := want[finding.Code]
		if !ok {
			t.Fatalf("unexpected divergence finding %s", finding.Code)
		}
		if finding.BehaviorChanged != changed {
			t.Fatalf("%s behaviorChanged = %v, want %v", finding.Code, finding.BehaviorChanged, changed)
		}
		delete(want, finding.Code)
	}
	if len(want) != 0 {
		t.Fatalf("missing divergence findings: %v", want)
	}
}
