// SPDX-License-Identifier: Apache-2.0

// Package projectionoracle is a test-only reference model for the Task
// Board/List projection. It deliberately owns a small fact vocabulary instead
// of importing production domain, reducer, projection, query, or store types.
//
// Runtime packages must not import this package. Its tests enforce that
// boundary statically so black-box tests can compare production output with an
// independent expected result without making this oracle product authority.
package projectionoracle
