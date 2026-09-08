// SPDX-License-Identifier: Apache-2.0

import type { PluginContext, PluginSurfaceProps } from "@getpaseo/plugin";
import type { ComponentType } from "react";

import { loadMeasuredBoard } from "./measurement.server";
import { boardSnapshotRpc } from "./rpc/board.shared";
import { ProjectBoard } from "./ui/shells.client";

export default function contribute(plugin: PluginContext) {
  plugin.handle(boardSnapshotRpc, loadMeasuredBoard);
  plugin.addSurface(
    "board",
    ProjectBoard as unknown as ComponentType<PluginSurfaceProps>,
  );
  plugin.addSidebarItem({
    id: "board",
    title: "Board measurement",
    icon: "Columns3",
    surface: "board",
  });
  return () => {};
}
