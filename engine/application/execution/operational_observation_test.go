// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"testing"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
)

func TestOperationalFactRequestsReobservationWithoutMaskingMalformedOrHardLimits(t *testing.T) {
	run := domain.Run{Version: 7}
	run.Execution.OperationalPolicy.MaximumObservationAgeMillis = 30_000
	run.Execution.OperationalObservation = &domainexecution.OperationalObservation{ID: "operational-1", ObservedAtMillis: 1_000}
	run.Execution.OperationalObservationRunVersion = run.Version
	if id := operationalObservationID(run, domainexecution.Admission{Kind: domainexecution.AdmissionPark,
		Code: domainexecution.NeedOperationalFactMissing}, 1_001); id != "operational-1" {
		t.Fatalf("malformed current fact identity = %q", id)
	}
	if id := operationalObservationID(run, domainexecution.Admission{Kind: domainexecution.AdmissionAllow}, 1_001); id != "operational-1" {
		t.Fatalf("current fact ID = %q", id)
	}
	if id := operationalObservationID(run, domainexecution.Admission{Kind: domainexecution.AdmissionPark,
		Code: domainexecution.NeedMemoryLimit}, 1_001); id != "operational-1" {
		t.Fatalf("hard-limit fact ID = %q", id)
	}
	if id := operationalObservationID(run, domainexecution.Admission{Kind: domainexecution.AdmissionPark,
		Code: domainexecution.NeedOperationalFactMissing}, 31_001); id != "" {
		t.Fatalf("stale fact remained authoritative as %q", id)
	}
	run.Execution.OperationalObservationRunVersion--
	if id := operationalObservationID(run, domainexecution.Admission{Kind: domainexecution.AdmissionAllow}, 1_001); id != "" {
		t.Fatalf("wrong-version fact remained authoritative as %q", id)
	}
}
