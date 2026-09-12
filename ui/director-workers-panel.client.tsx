// SPDX-License-Identifier: Apache-2.0
// Owner-visible Director Workers aggregate for one root workspace.

import type { PluginWorkspacePanelProps } from "@getpaseo/plugin";
import { usePaseo, useRpc } from "@getpaseo/plugin";
import { Icon } from "@getpaseo/plugin/react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useMemo } from "react";
import {
  ActivityIndicator,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from "react-native";

import { directorWorkersRpc } from "../rpc/workers.shared.ts";
import {
  formatWorkerCandidate,
  formatWorkerDuration,
  orderWorkers,
  workerQueryKey,
  workerRoleLabel,
} from "./director-workers-model.client.ts";
import { shellMetrics } from "./shell-layout.client.ts";
import {
  AccessibilityProvider,
  AccessiblePressable,
  useAccessibilityAnnouncement,
  useAccessibilityPreferences,
  useResponsiveCompactLayout,
} from "./accessibility.client.tsx";

export function DirectorWorkers({
  theme,
  layout,
  navigation,
  workspaceId,
}: PluginWorkspacePanelProps) {
  const accessibilityPreferences = useAccessibilityPreferences();
  const loadWorkers = useRpc(directorWorkersRpc);
  const queryClient = useQueryClient();
  const paseo = usePaseo();
  const compact = useResponsiveCompactLayout(layout.compact);
  const metrics = shellMetrics(compact);
  const queryKey = workerQueryKey(workspaceId);
  const workers = useQuery({
    queryKey,
    queryFn: () => loadWorkers({ rootWorkspaceId: workspaceId }),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
  useAccessibilityAnnouncement(
    workers.isPending
      ? "Loading Director workers"
      : workers.isError
        ? "Worker visibility unavailable"
        : workers.data.workers.length === 0
          ? "No registered Director workers"
          : `${workers.data.workers.length} Director workers shown`,
  );

  // Director re-reads the aggregate when Paseo reports agent movement. Nothing
  // here polls on a timer and no worker reports its own liveness: that belongs
  // to the daemon's event stream, and the PLAN multi-source stall predicate
  // remains the only watchdog over a quiet worker.
  useEffect(
    () =>
      paseo.agents.subscribe(() => {
        void queryClient.invalidateQueries({
          queryKey: workerQueryKey(workspaceId),
        });
      }),
    [paseo, queryClient, workspaceId],
  );

  const styles = useMemo(
    () =>
      StyleSheet.create({
        screen: {
          flexGrow: 1,
          padding: metrics.padding,
          gap: metrics.gap,
          backgroundColor: theme.colors.surface0,
        },
        title: {
          color: theme.colors.foreground,
          fontSize: metrics.titleSize,
          fontWeight: "700",
        },
        body: { color: theme.colors.foregroundMuted, lineHeight: 20 },
        liveState: {
          minHeight: 120,
          alignItems: "center",
          justifyContent: "center",
          gap: 10,
          padding: 20,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderRadius: 10,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        worker: {
          padding: 14,
          gap: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderRadius: 10,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        workerHeader: {
          flexDirection: compact ? "column" : "row",
          justifyContent: "space-between",
          gap: 8,
        },
        workerTitle: { color: theme.colors.foreground, fontWeight: "700" },
        status: { color: theme.colors.foregroundMuted, fontWeight: "600" },
        metadata: { flexDirection: "row", flexWrap: "wrap", gap: 10 },
        label: { color: theme.colors.foregroundMuted, fontSize: 12 },
        value: { color: theme.colors.foreground, fontSize: 12 },
        action: {
          minHeight: 44,
          minWidth: 44,
          alignSelf: compact ? "stretch" : "flex-start",
          paddingHorizontal: 14,
          paddingVertical: 10,
          borderRadius: 8,
          backgroundColor: theme.colors.accent,
        },
        actionDisabled: { backgroundColor: theme.colors.surface2 },
        actionText: {
          color: theme.colors.accentForeground,
          fontWeight: "600",
          textAlign: "center",
        },
        actionTextDisabled: { color: theme.colors.foregroundMuted },
      }),
    [accessibilityPreferences.highContrast, compact, metrics, theme],
  );

  return (
    <AccessibilityProvider focusColor={theme.colors.accent} preferences={accessibilityPreferences}>
    <ScrollView contentContainerStyle={styles.screen}>
      <Text accessibilityRole="header" style={styles.title}>
        Director Workers
      </Text>
      <Text style={styles.body}>
        Live top-level Task Agents and Reviewers registered to this root
        workspace, including workers whose checkout workspace was restored.
      </Text>
      {workers.isPending ? (
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          {accessibilityPreferences.reduceMotion ? (
            <Icon color={theme.colors.accent} name="Clock3" size={24} />
          ) : (
            <ActivityIndicator color={theme.colors.accent} />
          )}
          <Text style={styles.body}>Loading Director workers</Text>
        </View>
      ) : workers.isError ? (
        <View accessibilityLiveRegion="assertive" style={styles.liveState}>
          <Text style={styles.workerTitle}>Worker visibility unavailable</Text>
          <Text style={styles.body}>
            Director refused an incomplete or malformed launch registration.
          </Text>
        </View>
      ) : workers.data.workers.length === 0 ? (
        <View style={styles.liveState}>
          <Text style={styles.workerTitle}>No registered workers</Text>
          <Text style={styles.body}>
            Launch is refused when Director cannot attach the root-workspace
            registry labels before a worker starts.
          </Text>
        </View>
      ) : (
        orderWorkers(workers.data.workers).map((worker) => {
          const canOpen = navigation !== undefined;
          return (
            <View
              accessible
              accessibilityLabel={`${worker.taskId}, ${workerRoleLabel(worker.role)}, ${worker.phase}, ${worker.status}`}
              key={worker.agentId}
              style={styles.worker}
            >
              <View style={styles.workerHeader}>
                <Text style={styles.workerTitle}>{worker.title}</Text>
                <Text style={styles.status}>{worker.status}</Text>
              </View>
              <View style={styles.metadata}>
                <Text style={styles.label}>
                  Role{" "}
                  <Text style={styles.value}>{workerRoleLabel(worker.role)}</Text>
                </Text>
                <Text style={styles.label}>
                  Task <Text style={styles.value}>{worker.taskId}</Text>
                </Text>
                <Text style={styles.label}>
                  Phase <Text style={styles.value}>{worker.phase}</Text>
                </Text>
                <Text style={styles.label}>
                  Duration{" "}
                  <Text style={styles.value}>
                    {formatWorkerDuration(worker.startedAt, Date.now())}
                  </Text>
                </Text>
                <Text style={styles.label}>
                  Candidate{" "}
                  <Text style={styles.value}>
                    {formatWorkerCandidate(worker.candidate)}
                  </Text>
                </Text>
              </View>
              <AccessiblePressable
                accessibilityHint={canOpen
                  ? "Opens the exact Paseo agent registered for this Task"
                  : "Native Paseo agent navigation is unavailable"}
                accessibilityLabel={`Open agent for ${worker.taskId}`}
                accessibilityRole="button"
                accessibilityState={{ disabled: !canOpen }}
                disabled={!canOpen}
                onPress={() => navigation?.openAgent({ agentId: worker.agentId })}
                style={[styles.action, !canOpen && styles.actionDisabled]}
              >
                <Text
                  style={[
                    styles.actionText,
                    !canOpen && styles.actionTextDisabled,
                  ]}
                >
                  Open agent
                </Text>
              </AccessiblePressable>
            </View>
          );
        })
      )}
    </ScrollView>
    </AccessibilityProvider>
  );
}
