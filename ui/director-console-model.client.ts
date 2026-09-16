// SPDX-License-Identifier: Apache-2.0
// Pure Director Console model: its tab list, each tab's enablement, and the
// root-Workspace fan-out the Workers tab renders one view per.
//
// The Console is a global surface, so the host supplies it no Workspace. Every
// Workspace-bound tab therefore takes its target from Director's own Home
// snapshot, and states the reason when that snapshot carries none instead of
// rendering a dead tab.

import type { HomeSnapshot } from "../generated/planning-contract.shared.ts";

export type DirectorConsoleTabId = "overview" | "board" | "workers";

export type DirectorConsoleTab = {
  readonly id: DirectorConsoleTabId;
  readonly label: string;
  readonly icon: string;
};

/**
 * Every tab the Console ships, in presentation order. `Administration` and
 * `Layout` (PLAN section 16.6) extend this list in the Tasks that implement
 * them, so no tab is ever registered here before it can render.
 */
export const directorConsoleTabs: readonly DirectorConsoleTab[] = [
  { id: "overview", label: "Overview", icon: "LayoutDashboard" },
  { id: "board", label: "Board", icon: "Columns3" },
  { id: "workers", label: "Workers", icon: "UsersRound" },
];

/** One Workers view target: an exact native Workspace and its heading. */
export type DirectorConsoleWorkerTarget = {
  readonly workspaceId: string;
  readonly heading: string;
};

export type DirectorConsoleTabState = DirectorConsoleTab & {
  readonly enabled: boolean;
  readonly unavailableReason: string | null;
};

/**
 * Fans the Workers aggregate over the native Workspaces this host's Projects
 * project. A Workspace the engine cannot bind to an exact native Workspace
 * carries a null `paseoWorkspaceId` and is skipped rather than guessed, and a
 * Workspace repeated across snapshot pages yields one view.
 */
export function directorConsoleWorkerTargets(
  pages: readonly HomeSnapshot[] | undefined,
): readonly DirectorConsoleWorkerTarget[] {
  const targets: DirectorConsoleWorkerTarget[] = [];
  const seen = new Set<string>();
  for (const page of pages ?? []) {
    for (const project of page.page.projects) {
      for (const workspace of project.workspaces) {
        const workspaceId = workspace.paseoWorkspaceId;
        if (workspaceId === null || seen.has(workspaceId)) continue;
        seen.add(workspaceId);
        targets.push({
          workspaceId,
          heading: `${project.name} · ${workspace.name}`,
        });
      }
    }
  }
  return targets;
}

/**
 * States each tab. Overview and Board own their own loading, error and empty
 * states and are always reachable; Board queries every Project with no
 * Workspace scope at all. Workers needs a native Workspace, so it is the one
 * tab whose reason is stated here.
 */
export function directorConsoleTabStates(input: {
  readonly workerTargets: readonly DirectorConsoleWorkerTarget[];
  readonly snapshotResolved: boolean;
}): readonly DirectorConsoleTabState[] {
  return directorConsoleTabs.map((tab) => {
    if (tab.id !== "workers" || input.workerTargets.length > 0) {
      return { ...tab, enabled: true, unavailableReason: null };
    }
    return {
      ...tab,
      enabled: false,
      unavailableReason: input.snapshotResolved
        ? "No Director Project on this host projects a native Paseo Workspace to read workers from."
        : "Director Workers becomes available once this host's Project facts resolve.",
    };
  });
}

/**
 * Keeps the visible tab on a tab that can render. A disabled tab falls back to
 * the first enabled one, which is why Overview is listed first.
 */
export function directorConsoleVisibleTab(
  tabs: readonly DirectorConsoleTabState[],
  requested: DirectorConsoleTabId,
): DirectorConsoleTabId {
  const selected = tabs.find((tab) => tab.id === requested);
  if (selected?.enabled === true) return requested;
  return tabs.find((tab) => tab.enabled)?.id ?? requested;
}
