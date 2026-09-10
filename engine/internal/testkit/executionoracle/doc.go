// SPDX-License-Identifier: Apache-2.0

// Package executionoracle is a Go-only, test-only reference model for the M3
// execution fault contract. It deliberately restates a smaller closed
// vocabulary instead of importing Director production reducers, domain types,
// ports, adapters, or runtimes.
//
// Product packages may consume this package only from Go test sources. The
// repository architecture checks also reject imports from another testkit so
// that neither side can become a disguised implementation dependency.
package executionoracle
