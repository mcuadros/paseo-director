// SPDX-License-Identifier: Apache-2.0
// Paseo-specific adapter for the engine-owned host port.

import {
  createPaseoClient,
  type PaseoClient,
  type PaseoClientConfig,
} from "@getpaseo/client";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  assertHostDescriptor,
  EXPECTED_HOST_DESCRIPTOR,
  HOST_CAPABILITIES,
  type DirectorHost,
  type HostCommand,
  type HostDescriptor,
  type HostObservation,
} from "../generated/host-contract.shared.ts";
import type { ConnectorStartupStatus } from "../rpc/startup.shared.ts";
import { loadConnectorCredential } from "./credential.server.ts";
import { engineBoundaryPaths } from "./engine-distribution.server.ts";
import {
  selectEngine,
  type EngineSelection,
} from "./engine-selection.server.ts";

export type ConnectorClient = Pick<PaseoClient, "close">;

export type ConnectorDependencies = {
  createClient?: (configuration: PaseoClientConfig) => ConnectorClient;
};

export class PaseoHostConnector implements DirectorHost {
  readonly #client: ConnectorClient;
  readonly #selection: EngineSelection;

  constructor(client: ConnectorClient, selection: EngineSelection) {
    this.#client = client;
    this.#selection = selection;
    assertHostDescriptor(EXPECTED_HOST_DESCRIPTOR);
  }

  async describe(): Promise<HostDescriptor> {
    return EXPECTED_HOST_DESCRIPTOR;
  }

  async invoke(_command: HostCommand): Promise<HostObservation> {
    throw new Error(
      "HOST_CAPABILITY_NOT_IMPLEMENTED: the connector scaffold has no product behavior",
    );
  }

  status(): ConnectorStartupStatus {
    return {
      state: "scaffold-ready",
      engineMode: this.#selection.mode,
      productBehavior: false,
      descriptor: {
        ...EXPECTED_HOST_DESCRIPTOR,
        capabilities: [...HOST_CAPABILITIES],
      },
    };
  }

  async close(): Promise<void> {
    await this.#client.close();
  }
}

export function startConnectorShell(options: {
  environment: NodeJS.ProcessEnv;
  checkoutRoot: string;
  dependencies?: ConnectorDependencies;
}): PaseoHostConnector {
  const selection = selectEngine(options.environment, options.checkoutRoot);
  const credential = loadConnectorCredential({
    credentialPath: options.environment.DIRECTOR_PASEO_CREDENTIAL_FILE,
    checkoutRoot: options.checkoutRoot,
    disjointEnginePaths: engineBoundaryPaths(selection),
  });
  const url = options.environment.DIRECTOR_PASEO_URL;
  if (!url) {
    throw new Error("DIRECTOR_PASEO_URL is required");
  }
  const createClient = options.dependencies?.createClient ?? createPaseoClient;
  const client = createClient({
    url,
    password: credential,
    clientId: `director-connector-${process.pid}`,
    reconnect: { enabled: false },
  });
  return new PaseoHostConnector(client, selection);
}

export function startConnectorShellFromEnvironment(): PaseoHostConnector {
  return startConnectorShell({
    environment: process.env,
    checkoutRoot: resolve(dirname(fileURLToPath(import.meta.url)), ".."),
  });
}
