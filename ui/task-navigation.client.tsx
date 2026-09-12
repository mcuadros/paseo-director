// SPDX-License-Identifier: Apache-2.0
// Paseo-owned composer pill contribution for exact Director Task Agents.

import {
  type PluginClientContext,
  type PluginComposerPillProps,
  useAgent,
} from "@getpaseo/plugin";
import { Icon } from "@getpaseo/plugin/react-native";
import React from "react";
import { Text } from "react-native";

import { WORKER_LABEL } from "../generated/host-contract.shared.ts";

const identityPattern = /^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,199}$/u;
const parentLabel = "paseo.parent-agent-id";

type ComposerAgent = {
  readonly id: string;
  readonly workspaceId?: string;
  readonly labels: Readonly<Record<string, string>>;
};

function exactTaskAgent(agent: ComposerAgent): { taskId: string; workspaceId: string } | null {
  const workspaceId = agent.workspaceId;
  const labels = agent.labels;
  const required = [
    labels[WORKER_LABEL.project],
    labels[WORKER_LABEL.rootWorkspace],
    labels[WORKER_LABEL.workspace],
    labels[WORKER_LABEL.executionWorkspace],
    labels[WORKER_LABEL.task],
    labels[WORKER_LABEL.run],
  ];
  if (
    !workspaceId ||
    !identityPattern.test(agent.id) ||
    !identityPattern.test(workspaceId) ||
    required.some((value) => value === undefined || !identityPattern.test(value)) ||
    labels[WORKER_LABEL.executionWorkspace] !== workspaceId ||
    labels[WORKER_LABEL.role] !== "task-agent" ||
    (labels[parentLabel] !== undefined && labels[parentLabel] !== "")
  ) {
    return null;
  }
  return { taskId: labels[WORKER_LABEL.task]!, workspaceId };
}

export function DirectorTaskPill({ theme, agentId }: PluginComposerPillProps) {
  const agent = useAgent(agentId, ({ title, labels }) => ({ title, labels }));
  const taskId = agent?.labels[WORKER_LABEL.task];
  return (
    <>
      <Icon color={theme.colors.foregroundMuted} name="ListChecks" size={14} />
      <Text
        numberOfLines={1}
        style={{ color: theme.colors.foregroundMuted, flexShrink: 1 }}
      >
        {agent?.title ?? taskId ?? "Director Task"}
      </Text>
    </>
  );
}

export function contributeTaskNavigation(client: PluginClientContext) {
  const pills = new Map<string, () => void>();
  let active = true;

  function remove(agentId: string) {
    pills.get(agentId)?.();
    pills.delete(agentId);
  }

  function updatePill(agent: ComposerAgent) {
    remove(agent.id);
    if (!active) return;
    const binding = exactTaskAgent(agent);
    if (!binding) return;
    pills.set(
      agent.id,
      client.addComposerPill({
        id: "director-task",
        title: `Open Director Task ${binding.taskId}`,
        workspaceId: binding.workspaceId,
        agentId: agent.id,
        Component: DirectorTaskPill,
        onPress() {
          client.openPanel("task-inspector", {
            workspaceId: binding.workspaceId,
            agentId: agent.id,
            location: "explorer",
          });
        },
      }),
    );
  }

  const unsubscribe = client.paseo.agents.subscribe((update) => {
    if (update.kind === "remove") {
      remove(update.agentId);
      return;
    }
    updatePill(update.agent as ComposerAgent);
  });

  void (async () => {
    let cursor: string | undefined;
    do {
      const page = await client.paseo.agents.list({
        scope: "active",
        filter: {
          labels: { [WORKER_LABEL.role]: "task-agent" },
          includeArchived: false,
        },
        sort: [{ key: "created_at", direction: "asc" }],
        page: { limit: 100, ...(cursor ? { cursor } : {}) },
      });
      if (!active) return;
      for (const entry of page.entries) updatePill(entry.agent as ComposerAgent);
      if (!page.pageInfo.hasMore) return;
      if (!page.pageInfo.nextCursor) return;
      cursor = page.pageInfo.nextCursor;
    } while (active);
  })().catch(() => {
    // The selected host may be unavailable. Live updates can restore pills;
    // no other host is queried as a fallback.
  });

  return () => {
    active = false;
    unsubscribe();
    for (const removePill of pills.values()) removePill();
    pills.clear();
  };
}
