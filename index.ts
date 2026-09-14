// SPDX-License-Identifier: Apache-2.0

import type { PluginContext } from "@getpaseo/plugin";

import { TaskInspector } from "./ui/task-inspector.client";
import { DirectorHome } from "./ui/director-home.client";
import { ProjectBoard } from "./ui/planning-surface.client";
import { DirectorWorkers } from "./ui/director-workers-panel.client";
import { contributeTaskNavigation } from "./ui/task-navigation.client";
import { registerInstalledConnectorHandlers } from "./connector/contributions.server";
import { connectorStartupStatus } from "./rpc/startup.shared";
import { recreateProjectAdminSessionRpc } from "./rpc/project-admin.shared";

export default function contribute(plugin: PluginContext) {
  let serverCleanup: () => void | Promise<void> = () => {};

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
    id: "create-director-administration-session",
    title: "Create Director administration session",
    icon: "ShieldCheck",
    keywords: ["director", "admin", "mcp", "reconnect"],
    context: "agent",
    async onSelect({ rpc, workspace, agent }) {
      await rpc(recreateProjectAdminSessionRpc, {
        requestId: `admin-open-${Date.now().toString(36)}-${agent.id.slice(0, 24)}`,
        sourceWorkspaceId: workspace.id,
        sourceAgentId: agent.id,
      });
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
  if (
    typeof plugin.handle === "function" &&
    typeof registerInstalledConnectorHandlers === "function"
  ) {
    plugin.handle(
      connectorStartupStatus,
      registerInstalledConnectorHandlers(plugin, (cleanup) => { serverCleanup = cleanup; }),
    );
  }
  if (typeof plugin.addClientSide === "function") {
    // 0.7.2 catalog evaluation does not guarantee this optional capability.
    // Core Director catalog contributions above remain registered if it is
    // unavailable or rejects the optional client-side navigation enhancer.
    try { plugin.addClientSide(contributeTaskNavigation); } catch { /* optional */ }
  }

  return () => serverCleanup();
}
