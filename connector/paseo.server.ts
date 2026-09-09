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
import type {
  PlanningMutationInput,
  PlanningMutationResult,
  PlanningQueryInput,
  PlanningSnapshot,
  TaskDetailQueryInput,
  TaskDetailSnapshot,
} from "../generated/planning-contract.shared.ts";
import type { ConnectorStartupStatus } from "../rpc/startup.shared.ts";
import { loadConnectorCredential } from "./credential.server.ts";
import {
  createBoardTransport,
  type BoardTransport,
} from "./engine-board.server.ts";
import { engineBoundaryPaths } from "./engine-distribution.server.ts";
import {
  selectEngine,
  type EngineSelection,
} from "./engine-selection.server.ts";

export type ConnectorClient = Pick<PaseoClient, "close">;

export type ConnectorDependencies = {
  createClient?: (configuration: PaseoClientConfig) => ConnectorClient;
  boardTransport?: BoardTransport;
};

export class PaseoHostConnector implements DirectorHost {
  readonly #client: ConnectorClient;
  readonly #selection: EngineSelection;
  readonly #boardTransport: BoardTransport;

  constructor(
    client: ConnectorClient,
    selection: EngineSelection,
    boardTransport: BoardTransport,
  ) {
    this.#client = client;
    this.#selection = selection;
    this.#boardTransport = boardTransport;
    assertHostDescriptor(EXPECTED_HOST_DESCRIPTOR);
  }

  async describe(): Promise<HostDescriptor> {
    return EXPECTED_HOST_DESCRIPTOR;
  }

  async invoke(_command: HostCommand): Promise<HostObservation> {
    throw new Error(
      "HOST_CAPABILITY_NOT_IMPLEMENTED: the Board/List slice has no lifecycle host behavior",
    );
  }

  status(): ConnectorStartupStatus {
    return {
      state: "board-ready",
      engineMode: this.#selection.mode,
      productBehavior: true,
      descriptor: {
        ...EXPECTED_HOST_DESCRIPTOR,
        capabilities: [...HOST_CAPABILITIES],
      },
    };
  }

  async loadBoard() {
    return this.#boardTransport.load();
  }

  async queryPlanning(_input: PlanningQueryInput): Promise<PlanningSnapshot> {
    throw new Error(
      "PLANNING_SURFACE_NOT_WIRED: runtime planning queries are owned by later M2 Tasks",
    );
  }

  async queryPlanningTask(
    _input: TaskDetailQueryInput,
  ): Promise<TaskDetailSnapshot> {
    throw new Error(
      "PLANNING_SURFACE_NOT_WIRED: runtime task-detail queries are owned by later M2 Tasks",
    );
  }

  async mutatePlanning(
    _input: PlanningMutationInput,
  ): Promise<PlanningMutationResult> {
    throw new Error(
      "PLANNING_SURFACE_NOT_WIRED: runtime planning mutations are owned by later M2 Tasks",
    );
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
  const boardTransport =
    options.dependencies?.boardTransport ??
    createBoardTransport({
      baseUrl: options.environment.DIRECTOR_ENGINE_URL,
    });
  const createClient = options.dependencies?.createClient ?? createPaseoClient;
  const client = createClient({
    url,
    password: credential,
    clientId: `director-connector-${process.pid}`,
    reconnect: { enabled: false },
  });
  return new PaseoHostConnector(client, selection, boardTransport);
}

export function startConnectorShellFromEnvironment(): PaseoHostConnector {
  return startConnectorShell({
    environment: process.env,
    checkoutRoot: resolve(dirname(fileURLToPath(import.meta.url)), ".."),
  });
}
