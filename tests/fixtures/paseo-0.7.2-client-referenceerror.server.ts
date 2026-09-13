// SPDX-License-Identifier: Apache-2.0

export class RuntimeConfigurationError extends Error {}

export function startInstalledConnectorShell(): void {
  throw new RuntimeConfigurationError("fixture server startup failed");
}
