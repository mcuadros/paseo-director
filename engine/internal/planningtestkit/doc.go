// SPDX-License-Identifier: Apache-2.0

// Package planningtestkit provides a test-only, structurally independent
// reference model for the PLAN v0.4 M2 planning invariants.
//
// The package deliberately defines its own closed vocabulary and imports no
// Director production package. Product source must not import it; the package's
// architecture test enforces that boundary. Tests for planning aggregates may
// import it from anywhere below the Director Engine module because it lives in
// internal.
package planningtestkit
