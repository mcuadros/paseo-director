// SPDX-License-Identifier: Apache-2.0

import {
  RuntimeConfigurationError,
  startInstalledConnectorShell,
} from "./paseo-0.7.2-client-referenceerror.server.ts";

export default function contribute(plugin: { addSurface(id: string, surface: unknown): void }) {
  try {
    startInstalledConnectorShell();
  } catch (error) {
    if (error instanceof RuntimeConfigurationError) return;
    throw error;
  }
  plugin.addSurface("home", null);
}
