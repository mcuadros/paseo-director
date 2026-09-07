// SPDX-License-Identifier: Apache-2.0

import type { PluginContext } from "@getpaseo/plugin";

import {
  DirectorHome,
  ProjectBoard,
  TaskInspector,
} from "./client/shells.client";
import { startConnectorShellFromEnvironment } from "./server/connector.server";
import { connectorStartupStatus } from "./shared/startup.shared";

export default function contribute(plugin: PluginContext) {
  const connector = startConnectorShellFromEnvironment();

  plugin.addSurface("home", DirectorHome);
  plugin.addSidebarItem({
    id: "home",
    title: "Director",
    icon: "PanelsTopLeft",
    surface: "home",
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
    Component: TaskInspector,
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
  plugin.handle(connectorStartupStatus, () => connector.status());

  return () => connector.close();
}
