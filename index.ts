// SPDX-License-Identifier: Apache-2.0

import type { PluginContext } from "@getpaseo/plugin";

import { TaskInspector } from "./ui/task-inspector.client";
import { DirectorHome } from "./ui/director-home.client";
import { ProjectBoard } from "./ui/planning-surface.client";
import { DirectorWorkers } from "./ui/director-workers-panel.client";
import { contributeTaskNavigation } from "./ui/task-navigation.client";
import { startInstalledConnectorShell } from "./connector/paseo.server";
import { ConnectorCredentialError } from "./connector/credential.server";
import { RuntimeConfigurationError } from "./connector/runtime-configuration.server.mjs";
import { connectorStartupStatus } from "./rpc/startup.shared";
import { boardSnapshotRpc } from "./rpc/board.shared";
import { directorWorkersRpc } from "./rpc/workers.shared";
import { loadDirectorWorkers } from "./connector/engine-workers.server";
import {
  planningMutationRpc,
  doctorQueryRpc,
  homeQueryRpc,
  organizerBootstrapRpc,
  operationsMutationRpc,
  operationsQueryRpc,
  planningQueryRpc,
  planningTaskDetailRpc,
  repairProjectRpc,
  nativePaseoProjectsRpc,
} from "./rpc/planning.shared";

export default function contribute(plugin: PluginContext) {
  type InstalledConnector = ReturnType<typeof startInstalledConnectorShell>;
  let connector: InstalledConnector | null = null;
  const startConnector = (): InstalledConnector => {
    connector ??= startInstalledConnectorShell();
    return connector;
  };
  try {
    startConnector();
  } catch (error) {
    const configurationAbsent = error instanceof RuntimeConfigurationError &&
      error.code === "DIRECTOR_RUNTIME_CONFIG_REQUIRED" && error.state === "absent";
    const credentialAbsent = error instanceof ConnectorCredentialError &&
      error.code === "CONNECTOR_CREDENTIAL_REQUIRED" && error.state === "absent";
    if (!configurationAbsent && !credentialAbsent) {
      throw error;
    }
    console.error(JSON.stringify({
      code: error.code,
      lifecycle: "plugin-reload",
      result: "configuration-required",
    }));
  }

  plugin.addSurface("home", DirectorHome);
  plugin.addSidebarItem({
    id: "home",
    title: "Director",
    icon: "PanelsTopLeft",
    surface: "home",
  });
  plugin.addWorkspacePanel({
    id: "director-workers",
    title: "Director Workers",
    icon: "UsersRound",
    context: "workspace",
    locations: ["workspace", "explorer"],
    Component: DirectorWorkers,
  });
  plugin.addWorkspacePanel({
    id: "project-board",
    title: "Director Board",
    icon: "Columns3",
    context: "workspace",
    Component: ProjectBoard,
  });
  plugin.addWorkspacePanel({
    id: "task-inspector",
    title: "Task Inspector",
    icon: "ListChecks",
    context: "agent",
    locations: ["workspace", "explorer"],
    Component: TaskInspector,
  });
  plugin.addCommandCenterItem({
    id: "open-director-workers",
    title: "Open Director Workers",
    icon: "UsersRound",
    context: "workspace",
    onSelect({ openPanel }) {
      openPanel("director-workers");
    },
  });
  plugin.addCommandCenterItem({
    id: "return-to-director-board",
    title: "Return to Director Board",
    icon: "Columns3",
    keywords: ["task", "planning", "back"],
    context: "agent",
    onSelect({ openPanel }) {
      openPanel("project-board");
    },
  });
  plugin.addCommandCenterItem({
    id: "open-project-board",
    title: "Open Director Board",
    icon: "Columns3",
    context: "workspace",
    onSelect({ openPanel }) {
      openPanel("project-board");
    },
  });
  plugin.addCommandCenterItem({
    id: "open-task-inspector",
    title: "Open Task Inspector",
    icon: "ListChecks",
    context: "agent",
    onSelect({ openPanel }) {
      openPanel("task-inspector");
    },
  });
  plugin.handle(connectorStartupStatus, () => startConnector().status());
  plugin.handle(boardSnapshotRpc, () => startConnector().loadBoard());
  plugin.handle(directorWorkersRpc, ({ rootWorkspaceId }, { paseo }) =>
    loadDirectorWorkers(
      (query) => paseo.agents.list(query),
      rootWorkspaceId,
    ),
  );
  plugin.handle(planningQueryRpc, (input) => startConnector().queryPlanning(input));
  plugin.handle(homeQueryRpc, (input) => startConnector().queryHome(input));
  plugin.handle(doctorQueryRpc, (input) => startConnector().queryDoctor(input));
  plugin.handle(operationsQueryRpc, (input) => startConnector().queryOperations(input));
  plugin.handle(operationsMutationRpc, (input) => startConnector().mutateOperations(input));
  plugin.handle(repairProjectRpc, (input) => startConnector().repairProject(input));
  plugin.handle(organizerBootstrapRpc, (input) => startConnector().bootstrapOrganizer(input));
  plugin.handle(nativePaseoProjectsRpc, (input) => startConnector().queryNativePaseoProjects(input));
  plugin.handle(planningTaskDetailRpc, (input) =>
    startConnector().queryPlanningTask(input),
  );
  plugin.handle(planningMutationRpc, (input) => startConnector().mutatePlanning(input));
  plugin.addClientSide(contributeTaskNavigation);

  return () => connector?.close();
}
