// SPDX-License-Identifier: Apache-2.0

package correction

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

var (
	//go:embed testdata/m0.4-correction-churn.json
	m04CorrectionChurn []byte
	//go:embed testdata/m0.6-correction-churn.json
	m06CorrectionChurn []byte
)

const (
	testCandidateID  = "candidate-1"
	testCandidateSHA = "1111111111111111111111111111111111111111"
	testAgentUUID    = "11111111-1111-4111-8111-111111111111"
)

type journalFixture struct {
	Fixture         string            `json:"fixture"`
	SourceRevisions map[Source]string `json:"sourceRevisions"`
	Findings        []struct {
		ID           string       `json:"id"`
		Source       Source       `json:"source"`
		Class        string       `json:"class"`
		Severity     Severity     `json:"severity"`
		Summary      string       `json:"summary"`
		EvidenceKind EvidenceKind `json:"evidenceKind"`
		Coverage     []string     `json:"coverage"`
		Blocking     bool         `json:"blocking"`
	} `json:"findings"`
}

func loadFixture(t *testing.T, name string) journalFixture {
	t.Helper()
	data := map[string][]byte{"m0.4": m04CorrectionChurn, "m0.6": m06CorrectionChurn}[name]
	var fixture journalFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(data) > 8*1024 || fixture.Fixture != name {
		t.Fatalf("unbounded or misbound sanitized fixture: %q", name)
	}
	return fixture
}

func fixtureBatch(t *testing.T, name string) (Batch, []string) {
	t.Helper()
	fixture := loadFixture(t, name)
	criteriaSet := map[string]struct{}{}
	inputs := make([]FindingInput, 0, len(fixture.Findings)+1)
	counts := map[Source]uint32{}
	for _, finding := range fixture.Findings {
		counts[finding.Source]++
		for _, criterion := range finding.Coverage {
			criteriaSet[criterion] = struct{}{}
		}
		inputs = append(inputs, FindingInput{
			ID: finding.ID, Source: finding.Source, Class: finding.Class, Severity: finding.Severity,
			CandidateID: testCandidateID, CandidateSHA: testCandidateSHA, Summary: finding.Summary,
			Evidence:           []Evidence{{ID: "evidence-" + finding.ID, Kind: finding.EvidenceKind, SHA256: strings.Repeat(string('1'+rune(len(inputs)%6)), 64)}},
			AcceptanceCoverage: finding.Coverage, Blocking: finding.Blocking,
		})
	}
	criteria := make([]string, 0, len(criteriaSet))
	for criterion := range criteriaSet {
		criteria = append(criteria, criterion)
	}
	slices.Sort(criteria)
	snapshots := make([]SourceSnapshot, 0, len(sources))
	for _, source := range sources {
		snapshots = append(snapshots, SourceSnapshot{Source: source, Revision: fixture.SourceRevisions[source], Count: counts[source]})
	}
	batch, ok := Canonicalize(testCandidateID, testCandidateSHA, criteria, snapshots, inputs)
	if !ok {
		t.Fatalf("fixture %s did not canonicalize", name)
	}
	return batch, criteria
}

func TestSanitizedJournalFixturesProduceStableWholeBatches(t *testing.T) {
	for _, name := range []string{"m0.4", "m0.6"} {
		t.Run(name, func(t *testing.T) {
			batch, criteria := fixtureBatch(t, name)
			if !ValidBatch(batch, criteria) || len(batch.Sources) != 4 || len(batch.Findings) == 0 || batch.SHA256 == "" ||
				batch.FindingFingerprint == batch.ClassFingerprint || batch.CandidateFingerprint == batch.CoverageFingerprint {
				t.Fatalf("batch = %#v", batch)
			}
			inputs := make([]FindingInput, len(batch.Findings))
			for index := range batch.Findings {
				inputs[len(inputs)-1-index] = batch.Findings[index].FindingInput
			}
			snapshots := slices.Clone(batch.Sources)
			slices.Reverse(snapshots)
			reordered, ok := Canonicalize(batch.CandidateID, batch.CandidateSHA, criteria, snapshots, inputs)
			if !ok || reordered.SHA256 != batch.SHA256 || !slices.Equal(FindingIDs(reordered), FindingIDs(batch)) {
				t.Fatalf("reordered batch drifted: %#v", reordered)
			}
		})
	}
}

func TestCanonicalizeDeduplicatesExactRowsAndRejectsConflictsOrUnboundedEvidence(t *testing.T) {
	batch, criteria := fixtureBatch(t, "m0.4")
	inputs := make([]FindingInput, len(batch.Findings))
	for index := range batch.Findings {
		inputs[index] = batch.Findings[index].FindingInput
	}
	inputs = append(inputs, inputs[0])
	snapshots := slices.Clone(batch.Sources)
	duplicate, ok := Canonicalize(batch.CandidateID, batch.CandidateSHA, criteria, snapshots, inputs)
	if !ok || len(duplicate.Findings) != len(batch.Findings) || duplicate.SHA256 != batch.SHA256 {
		t.Fatalf("exact duplicate was not canonicalized")
	}
	inputs[len(inputs)-1].Summary = "Conflicting content"
	if _, ok := Canonicalize(batch.CandidateID, batch.CandidateSHA, criteria, snapshots, inputs); ok {
		t.Fatal("conflicting duplicate admitted")
	}
	inputs = inputs[:len(inputs)-1]
	inputs[0].Summary = "token=secret-shaped-value-that-must-stop"
	if _, ok := Canonicalize(batch.CandidateID, batch.CandidateSHA, criteria, snapshots, inputs); ok {
		t.Fatal("secret-shaped summary admitted")
	}
	inputs[0] = batch.Findings[0].FindingInput
	inputs[0].Evidence = make([]Evidence, MaximumEvidence+1)
	if _, ok := Canonicalize(batch.CandidateID, batch.CandidateSHA, criteria, snapshots, inputs); ok {
		t.Fatal("unbounded evidence admitted")
	}
}

func TestCoverageDeltaAndLineageKeysAreDeterministic(t *testing.T) {
	delta := Delta([]string{"criterion-a", "criterion-b"}, []string{"criterion-b", "criterion-c"})
	if !slices.Equal(delta.Added, []string{"criterion-c"}) || !slices.Equal(delta.Removed, []string{"criterion-a"}) {
		t.Fatalf("delta = %#v", delta)
	}
	lineage := LineageKey("task-1", "run-1", testCandidateID, testCandidateSHA, testAgentUUID)
	first := AttemptKey(lineage, strings.Repeat("a", 64), 1, ReasonInitial)
	if first != AttemptKey(lineage, strings.Repeat("a", 64), 1, ReasonInitial) || first == AttemptKey(lineage, strings.Repeat("b", 64), 1, ReasonInitial) ||
		PromptEffectID(first) == PromptEffectID(AttemptKey(lineage, strings.Repeat("a", 64), 2, ReasonFindingsChanged)) {
		t.Fatal("correction keys are not deterministic and binding-sensitive")
	}
}

func TestStateRequiresExactlyThreeAttemptPolicyAndTamperProofBatch(t *testing.T) {
	batch, criteria := fixtureBatch(t, "m0.4")
	state, ok := NewState("task-1", "run-1", testCandidateID, testCandidateSHA, testAgentUUID, criteria,
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64),
		Policy{AutoFixCIFailures: true, AutoFixReviewFeedback: true, AttemptLimit: AttemptLimit}, batch)
	if !ok || !ValidState(state) {
		t.Fatalf("state = %#v", state)
	}
	state.Policy.AttemptLimit = 4
	if ValidState(state) {
		t.Fatal("non-three correction limit admitted")
	}
	state.Policy.AttemptLimit = AttemptLimit
	state.Batches[0].Findings[0].Summary = "changed"
	if ValidState(state) {
		t.Fatal("tampered batch admitted")
	}
}
