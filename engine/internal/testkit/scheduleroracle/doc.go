// SPDX-License-Identifier: Apache-2.0

// Package scheduleroracle is a deliberately small, structurally independent
// reference model for scheduler tests. It restates the PLAN's closed scheduling
// vocabulary without importing Director domain, reducer, port, adapter, or
// runtime packages.
//
// The package is test authority only. Repository architecture checks permit it
// in Go test imports and reject it from product runtime imports.
package scheduleroracle
