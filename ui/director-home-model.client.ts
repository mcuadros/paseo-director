// SPDX-License-Identifier: Apache-2.0

import type {
  HomeAction,
  HomeSnapshot,
} from "../generated/planning-contract.shared.ts";

export type DirectorHomeScene =
  | { kind: "loading" }
  | { kind: "error"; code: "offline" | "contract" | "host" | "invalid" }
  | { kind: "empty"; snapshot: HomeSnapshot; stale: boolean }
  | { kind: "stale"; snapshot: HomeSnapshot }
  | { kind: "needs_you"; snapshot: HomeSnapshot; stale: false }
  | { kind: "partial_sync"; snapshot: HomeSnapshot; stale: false }
  | { kind: "degraded"; snapshot: HomeSnapshot; stale: false }
  | { kind: "data"; snapshot: HomeSnapshot; stale: false };

type DirectorHomeErrorCode = "offline" | "contract" | "host" | "invalid";

function sameTotals(left: HomeSnapshot, right: HomeSnapshot): boolean {
  return JSON.stringify(left.page.totals) === JSON.stringify(right.page.totals) &&
    left.page.totalProjects === right.page.totalProjects;
}

export function combineHomePages(
  pages: readonly HomeSnapshot[],
  expectedHostId: string,
): HomeSnapshot {
  if (pages.length === 0) throw new Error("HOME_PAGES_EMPTY");
  const first = pages[0]!;
  if (first.page.host.id !== expectedHostId) throw new Error("HOME_HOST_MISMATCH");
  const projects = [] as HomeSnapshot["page"]["projects"][number][];
  const identities = new Set<string>();
  for (const page of pages) {
    if (
      page.page.host.id !== expectedHostId ||
      page.page.host.instanceId !== first.page.host.instanceId ||
      page.cursor !== first.cursor ||
      page.contractVersion !== first.contractVersion ||
      page.contractHash !== first.contractHash ||
      !sameTotals(first, page)
    ) {
      throw new Error("HOME_PAGE_BINDING_MISMATCH");
    }
    for (const project of page.page.projects) {
      const identity = `${expectedHostId}\u001f${project.id}`;
      if (identities.has(identity)) throw new Error("HOME_PROJECT_DUPLICATE");
      identities.add(identity);
      projects.push(project);
    }
  }
  if (projects.length > Number(first.page.totalProjects)) {
    throw new Error("HOME_PROJECT_COUNT_INVALID");
  }
  const last = pages[pages.length - 1]!;
  return {
    ...first,
    page: { ...first.page, projects, nextCursor: last.page.nextCursor },
  };
}

function transportCode(error: unknown): DirectorHomeErrorCode {
  const code = typeof error === "object" && error !== null && "code" in error
    ? String((error as { code: unknown }).code)
    : "";
  if (code.includes("CONTRACT")) return "contract";
  if (code.includes("HOST_MISMATCH") || code.includes("ORIGIN")) return "host";
  if (code.includes("UNAVAILABLE")) return "offline";
  return "invalid";
}

export function directorHomeScene(input: {
  pages: readonly HomeSnapshot[] | undefined;
  expectedHostId: string;
  isPending: boolean;
  isError: boolean;
  error: unknown;
}): DirectorHomeScene {
  if (!input.pages && input.isPending) return { kind: "loading" };
  if (!input.pages) return { kind: "error", code: transportCode(input.error) };
  let snapshot: HomeSnapshot;
  try {
    snapshot = combineHomePages(input.pages, input.expectedHostId);
  } catch {
    return { kind: "error", code: "host" };
  }
  if (input.isError || snapshot.page.host.state === "disconnected" || snapshot.page.host.state === "stale") {
    return { kind: "stale", snapshot };
  }
  if (snapshot.page.projects.length === 0) return { kind: "empty", snapshot, stale: false };
  if (snapshot.page.projects.some((project) => project.health === "needs_you")) {
    return { kind: "needs_you", snapshot, stale: false };
  }
  if (snapshot.page.projects.some((project) => project.sync.state === "partial")) {
    return { kind: "partial_sync", snapshot, stale: false };
  }
  if (
    snapshot.page.host.state === "degraded" ||
    snapshot.page.projects.some((project) => project.health !== "healthy")
  ) {
    return { kind: "degraded", snapshot, stale: false };
  }
  return { kind: "data", snapshot, stale: false };
}

export function homeProjectKey(hostId: string, projectId: string): string {
  return `${hostId}:${projectId}`;
}

export function homeActionEnabled(input: {
  action: HomeAction;
  expectedHostId: string;
  stale: boolean;
  navigationAvailable: boolean;
}): boolean {
  if (input.stale || input.action.hostId !== input.expectedHostId || !input.action.enabled) return false;
  if (
    (input.action.kind === "open_board" || input.action.kind === "open_organizer") &&
    (!input.navigationAvailable || input.action.paseoWorkspaceId === null)
  ) {
    return false;
  }
  return true;
}
