// SPDX-License-Identifier: Apache-2.0
// Server-only Paseo registrations. Keeping this session behind one direct
// plugin.handle call lets Paseo's split compiler erase it from the client bundle.

import type { PluginContext, PluginHandlerContext } from "@getpaseo/plugin";

import { loadDirectorWorkers } from "./engine-workers.server.ts";
import { PlanningTransportError } from "./engine-planning.server.ts";
import { startInstalledConnectorShell } from "./paseo.server.ts";
import { boardSnapshotRpc } from "../rpc/board.shared.ts";
import {
  doctorQueryRpc,
  homeQueryRpc,
  nativePaseoProjectsRpc,
  operationsMutationRpc,
  operationsQueryRpc,
  organizerBootstrapRpc,
  planningMutationRpc,
  planningQueryRpc,
  planningTaskDetailRpc,
  repairProjectRpc,
} from "../rpc/planning.shared.ts";
import { directorWorkersRpc } from "../rpc/workers.shared.ts";
import type { HomeQueryFailure } from "../rpc/home-diagnostics.shared.ts";
import { recreateProjectAdminSessionRpc } from "../rpc/project-admin.shared.ts";

type PaseoApi = PluginHandlerContext["paseo"];
type InstalledConnector = ReturnType<typeof startInstalledConnectorShell>;

function boundedErrorCode(error: unknown): string {
  const objectCode = error && typeof error === "object" ? Reflect.get(error, "code") : undefined;
  const message = error instanceof Error ? error.message : "";
  for (const candidate of [objectCode, message]) {
    if (typeof candidate === "string" && /^[A-Z][A-Z0-9_]{2,95}$/u.test(candidate)) return candidate;
  }
  return "DIRECTOR_HOME_QUERY_FAILED";
}

async function homeResult(
  connector: InstalledConnector,
  input: Parameters<InstalledConnector["queryHome"]>[0],
): Promise<Awaited<ReturnType<InstalledConnector["queryHome"]>> | HomeQueryFailure> {
  try {
    return await connector.queryHome(input);
  } catch (error) {
    const code = boundedErrorCode(error);
    const diagnosis = error instanceof PlanningTransportError ? error.diagnosis : null;
    console.error(JSON.stringify({ code, lifecycle: "home-query", result: "failed", ...(diagnosis ?? {}) }));
    return { status: "rejected", code, diagnosis };
  }
}

export function registerInstalledConnectorHandlers(
  plugin: PluginContext,
  setCleanup: (cleanup: () => Promise<void>) => void,
) {
  let connector: InstalledConnector | null = null;
  let paseoAuthority: PaseoApi | null = null;
  const startConnector = (paseo: PaseoApi): InstalledConnector => {
    if (paseoAuthority !== null && paseoAuthority !== paseo) {
      throw new Error("HOST_PUBLIC_PLUGIN_AUTHORITY_CHANGED");
    }
    paseoAuthority = paseo;
    connector ??= startInstalledConnectorShell({ paseo });
    setCleanup(async () => {
      const active = connector;
      connector = null;
      paseoAuthority = null;
      await active?.close();
    });
    return connector;
  };

  plugin.handle(boardSnapshotRpc, (_input, { paseo }) => startConnector(paseo).loadBoard());
  plugin.handle(directorWorkersRpc, ({ rootWorkspaceId }, { paseo }) =>
    loadDirectorWorkers(
      (query) => paseo.agents.list(query),
      rootWorkspaceId,
    ),
  );
  plugin.handle(planningQueryRpc, (input, { paseo }) => startConnector(paseo).queryPlanning(input));
  plugin.handle(homeQueryRpc, (input, { paseo }) => homeResult(startConnector(paseo), input));
  plugin.handle(doctorQueryRpc, (input, { paseo }) => startConnector(paseo).queryDoctor(input));
  plugin.handle(operationsQueryRpc, (input, { paseo }) => startConnector(paseo).queryOperations(input));
  plugin.handle(operationsMutationRpc, (input, { paseo }) => startConnector(paseo).mutateOperations(input));
  plugin.handle(repairProjectRpc, (input, { paseo }) => startConnector(paseo).repairProject(input));
  plugin.handle(organizerBootstrapRpc, (input, { paseo }) => startConnector(paseo).bootstrapOrganizer(input));
  plugin.handle(nativePaseoProjectsRpc, (input, { paseo }) => startConnector(paseo).queryNativePaseoProjects(input));
  plugin.handle(planningTaskDetailRpc, (input, { paseo }) => startConnector(paseo).queryPlanningTask(input));
  plugin.handle(planningMutationRpc, (input, { paseo }) => startConnector(paseo).mutatePlanning(input));
  plugin.handle(recreateProjectAdminSessionRpc, (input, { paseo }) =>
    startConnector(paseo).recreateProjectAdminSession(input));

  return (_input: unknown, { paseo }: PluginHandlerContext) => startConnector(paseo).status();
}
