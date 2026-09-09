// SPDX-License-Identifier: Apache-2.0

// Package configoracle provides an independent, test-only model of Director
// configuration inheritance and activation. It deliberately does not import
// production configuration, reducer, persistence, host, or connector code.
//
// The oracle models one scalar configuration dimension. Tests can apply the
// same closed model independently to every concrete production field without
// making this package product authority.
package configoracle
