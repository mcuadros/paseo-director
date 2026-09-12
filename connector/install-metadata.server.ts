// SPDX-License-Identifier: Apache-2.0
// Paseo's declared candidate preparation replaces this fail-closed placeholder.

export const INSTALLED_CONNECTOR_METADATA = {
  schemaVersion: 1,
  state: "unprepared",
  connectorCommit: "0000000000000000000000000000000000000000",
  releaseMetadata: {
    schemaVersion: 2,
    state: "unpublished",
    version: "0.0.0-scaffold",
    target: "linux-amd64",
  },
} as const;
