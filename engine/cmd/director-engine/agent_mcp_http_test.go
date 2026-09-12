// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"testing"

	bridgeapp "github.com/mcuadros/director-engine/application/agentbridge"
)

func TestMCPInitializationRetriesOnlyAConcurrentExpectedStateChange(t *testing.T) {
	if !retryableMCPBindingError(&bridgeapp.Failure{Code: bridgeapp.CodeExpectedState}) {
		t.Fatal("concurrent expected-state change was not retryable")
	}
	for _, failure := range []error{
		&bridgeapp.Failure{Code: bridgeapp.CodeSessionInvalid},
		&bridgeapp.Failure{Code: bridgeapp.CodeScopeMismatch},
		&bridgeapp.Failure{Code: bridgeapp.CodeProviderPreflight},
		errors.New("transport failed"),
	} {
		if retryableMCPBindingError(failure) {
			t.Fatalf("unsafe MCP error became retryable: %T", failure)
		}
	}
}
