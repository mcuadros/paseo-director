// SPDX-License-Identifier: Apache-2.0

package reducer

import (
	"slices"
	"testing"
)

func TestClosedReducerVocabulary(t *testing.T) {
	expected := []Kind{Eligibility, Launch, Retry, Escalation, Routing, Closure}
	if actual := Kinds(); !slices.Equal(actual, expected) {
		t.Fatalf("Kinds() = %v, want %v", actual, expected)
	}
	actual := Kinds()
	actual[0] = Kind("other")
	if Kinds()[0] != Eligibility {
		t.Fatal("Kinds returned mutable package state")
	}
}
