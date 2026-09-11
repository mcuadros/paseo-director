// SPDX-License-Identifier: Apache-2.0

import type { PluginSurfaceProps } from "@getpaseo/plugin";
import { useRpc } from "@getpaseo/plugin";
import { Icon, Modal, useToast } from "@getpaseo/plugin/react-native";
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import React, { useEffect, useMemo, useState } from "react";
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from "react-native";

import {
  bindPlanningMutation,
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  PLANNING_SCHEMA_VERSION,
  type HomeAction,
  type HomeActionKind,
  type HomeProject,
  type HomeSnapshot,
  type PlanningMutationInput,
  type PlanningMutationIntent,
  type OrganizerBootstrapInput,
} from "../generated/planning-contract.shared.ts";
import {
  homeQueryRpc,
  planningMutationRpc,
  organizerBootstrapRpc,
} from "../rpc/planning.shared.ts";
import {
  directorHomeScene,
  homeActionEnabled,
  homeProjectKey,
} from "./director-home-model.client.ts";
import { shellMetrics } from "./shell-layout.client.ts";

const HOME_PAGE_SIZE = 25;

type OrganizerEntry = {
  hostId: string;
  mode: "create" | "adopt";
  requestId: string | null;
  projectId: string;
  projectName: string;
  repositoryPath: string;
  configurationJson: string;
};

function organizerRequestId(): string | null {
  return typeof globalThis.crypto?.randomUUID === "function" ? globalThis.crypto.randomUUID() : null;
}

const healthLabels: Record<HomeProject["health"], string> = {
  healthy: "Healthy",
  degraded: "Degraded",
  paused: "Paused",
  needs_you: "Needs you",
  disconnected: "Disconnected",
  stale: "Stale",
  unknown: "Unknown",
};

const actionIcons: Record<HomeActionKind, string> = {
  create_project: "FolderPlus",
  adopt_organizer: "FolderSearch",
  open_board: "Columns3",
  open_organizer: "FolderCog",
  open_needs_you: "CircleAlert",
  sync_now: "RefreshCw",
  reconcile_now: "ScanSearch",
  doctor: "Stethoscope",
  repair: "Wrench",
  pause: "Pause",
  resume: "Play",
  emergency_stop: "OctagonX",
};

function readableCode(value: string): string {
  const text = value.replaceAll("_", " ");
  return text.length === 0 ? "Unavailable" : text[0]!.toUpperCase() + text.slice(1);
}

function mutationIntent(action: HomeAction): PlanningMutationIntent | null {
  if (!action.command || !action.projectId) return null;
  switch (action.command.kind) {
    case "project.pause":
      return { type: "project.pause", projectId: action.projectId };
    case "project.resume":
      return { type: "project.resume", projectId: action.projectId };
    case "project.emergency-stop.prepare":
      return { type: "project.emergency-stop.prepare", projectId: action.projectId };
    case "project.emergency-stop.confirm":
      return { type: "project.emergency-stop.confirm", projectId: action.projectId };
    default:
      return null;
  }
}

export function DirectorHome({ theme, layout, host, navigation }: PluginSurfaceProps) {
  const loadHome = useRpc(homeQueryRpc);
  const mutatePlanning = useRpc(planningMutationRpc);
  const bootstrapOrganizer = useRpc(organizerBootstrapRpc);
  const toast = useToast();
  const queryClient = useQueryClient();
  const [entry, setEntry] = useState<OrganizerEntry | null>(null);
  const [inspectedProject, setInspectedProject] = useState<string | null>(null);
  const home = useInfiniteQuery({
    queryKey: ["director", "home", host.id],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) => loadHome({ hostId: host.id, cursor: pageParam, pageSize: HOME_PAGE_SIZE }),
    getNextPageParam: (lastPage: HomeSnapshot) => lastPage.page.nextCursor,
    retry: false,
    staleTime: 0,
    refetchOnMount: "always",
    refetchOnReconnect: true,
    refetchInterval: 30_000,
  });
  const scene = directorHomeScene({
    pages: home.data?.pages,
    expectedHostId: host.id,
    isPending: home.isPending,
    isError: home.isError || home.isRefetchError || home.isFetchNextPageError,
    error: home.error,
  });
  const control = useMutation({
    mutationFn: (input: PlanningMutationInput) => mutatePlanning(input),
    onSuccess: async (result) => {
      toast.show(result.message, { variant: result.status === "accepted" ? "success" : "warning" });
      await queryClient.invalidateQueries({ queryKey: ["director", "home", host.id], exact: true });
    },
    onError: () => toast.error("The operational action was rejected by current engine facts."),
  });
  const organizer = useMutation({
    mutationFn: (input: OrganizerBootstrapInput) => bootstrapOrganizer(input),
    onSuccess: async (result) => {
      if (result.status === "applied") {
        toast.show(result.message, { variant: "success" });
        setEntry(null);
        await queryClient.resetQueries({ queryKey: ["director", "home", host.id], exact: true });
      } else if (result.status === "rejected") {
        toast.show(result.message, { variant: "warning" });
      }
    },
    onError: () => toast.error("Organizer Preview/Apply is unavailable on this exact host."),
  });
  useEffect(() => {
    setEntry(null);
    setInspectedProject(null);
    organizer.reset();
    control.reset();
  }, [host.id]);
  const snapshot = "snapshot" in scene ? scene.snapshot : null;
  const stale = scene.kind === "stale";
  const metrics = shellMetrics(layout.compact);
  const styles = useMemo(
    () => StyleSheet.create({
      screen: {
        flexGrow: 1,
        padding: metrics.padding,
        gap: metrics.gap,
        backgroundColor: theme.colors.surface0,
      },
      toolbar: {
        flexDirection: layout.compact ? "column" : "row",
        justifyContent: "space-between",
        alignItems: layout.compact ? "flex-start" : "center",
        gap: metrics.gap,
      },
      heading: { color: theme.colors.foreground, fontSize: layout.compact ? 18 : 20, fontWeight: "700" },
      body: { color: theme.colors.foregroundMuted, lineHeight: 20 },
      operationalLine: { color: theme.colors.foregroundMuted, fontSize: 11 },
      banner: {
        paddingHorizontal: 14,
        paddingVertical: 10,
        gap: 4,
        borderLeftWidth: 3,
        borderLeftColor: theme.colors.statusWarning,
        backgroundColor: theme.colors.surface1,
      },
      bannerDanger: { borderLeftColor: theme.colors.statusDanger },
      bannerTitle: { color: theme.colors.foreground, fontWeight: "700" },
      actions: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
      action: {
        minHeight: 44,
        flexDirection: "row",
        alignItems: "center",
        justifyContent: "center",
        gap: 7,
        paddingHorizontal: 14,
        paddingVertical: 10,
        borderRadius: 9,
        borderWidth: 1,
        borderColor: theme.colors.border,
        backgroundColor: theme.colors.surface2,
      },
      primaryAction: { backgroundColor: theme.colors.accent, borderColor: theme.colors.accent },
      dangerAction: { borderColor: theme.colors.statusDanger },
      disabledAction: { opacity: 0.5 },
      actionText: { color: theme.colors.foreground, fontWeight: "600" },
      primaryActionText: { color: theme.colors.accentForeground },
      totals: {
        flexDirection: "row",
        flexWrap: "wrap",
        gap: layout.compact ? 12 : 24,
        paddingVertical: 10,
        borderTopWidth: 1,
        borderBottomWidth: 1,
        borderColor: theme.colors.border,
      },
      total: {
        minWidth: layout.compact ? "28%" : 90,
        gap: 2,
      },
      totalValue: { color: theme.colors.foreground, fontSize: 18, fontWeight: "700" },
      totalLabel: { color: theme.colors.foregroundMuted, fontSize: 12 },
      grid: {
        borderWidth: 1,
        borderColor: theme.colors.border,
        borderRadius: 12,
        overflow: "hidden",
        backgroundColor: theme.colors.surface1,
      },
      card: {
        width: "100%",
        padding: 16,
        gap: 9,
        borderBottomWidth: 1,
        borderBottomColor: theme.colors.border,
      },
      cardHeader: { flexDirection: "row", justifyContent: "space-between", alignItems: "flex-start", gap: 8 },
      cardTitle: { flex: 1, color: theme.colors.foreground, fontSize: 17, fontWeight: "700" },
      health: { paddingHorizontal: 9, paddingVertical: 5, borderRadius: 999, backgroundColor: theme.colors.surface2 },
      healthText: { color: theme.colors.foreground, fontSize: 12, fontWeight: "600" },
      warningText: { color: theme.colors.statusWarning },
      dangerText: { color: theme.colors.statusDanger },
      successText: { color: theme.colors.statusSuccess },
      facts: { flexDirection: "row", flexWrap: "wrap", gap: 12 },
      fact: { color: theme.colors.foregroundMuted, fontSize: 12 },
      projectDetails: { flexDirection: layout.compact ? "column" : "row", gap: layout.compact ? 8 : 24 },
      detailGroup: { flex: 1, gap: 3 },
      sectionTitle: { color: theme.colors.foreground, fontWeight: "600" },
      detailLabel: { color: theme.colors.foregroundMuted, fontSize: 11, fontWeight: "600", textTransform: "uppercase" },
      chips: { flexDirection: "row", flexWrap: "wrap", gap: 6 },
      chip: { paddingHorizontal: 8, paddingVertical: 5, borderRadius: 7, backgroundColor: theme.colors.surface2 },
      chipText: { color: theme.colors.foregroundMuted, fontSize: 12 },
      panel: {
        gap: 9,
      },
      modalContent: { padding: layout.compact ? 16 : 20, gap: 12 },
      previewPanel: { padding: 12, gap: 7, borderLeftWidth: 3, borderLeftColor: theme.colors.accent, backgroundColor: theme.colors.surface1 },
      field: { gap: 5 },
      fieldLabel: { color: theme.colors.foreground, fontSize: 12, fontWeight: "600" },
      input: {
        minHeight: 44,
        paddingHorizontal: 12,
        paddingVertical: 10,
        borderRadius: 8,
        borderWidth: 1,
        borderColor: theme.colors.border,
        color: theme.colors.foreground,
        backgroundColor: theme.colors.surface2,
      },
      configurationInput: { minHeight: 150, textAlignVertical: "top" },
      liveState: {
        minHeight: 150,
        alignItems: "center",
        justifyContent: "center",
        gap: 10,
        padding: 22,
        borderTopWidth: 1,
        borderBottomWidth: 1,
        borderColor: theme.colors.border,
        backgroundColor: theme.colors.surface1,
      },
      centered: { textAlign: "center" },
      more: { alignSelf: "center" },
    }),
    [layout.compact, metrics, theme],
  );

  function actionStyle(action: HomeAction, enabled: boolean) {
    return [
      styles.action,
      action.emphasis === "primary" && styles.primaryAction,
      action.emphasis === "danger" && styles.dangerAction,
      !enabled && styles.disabledAction,
    ];
  }

  function openOrganizerEntry(mode: "create" | "adopt"): void {
    organizer.reset();
    setEntry({
      mode,
      hostId: host.id,
      requestId: organizerRequestId(),
      projectId: "",
      projectName: "",
      repositoryPath: "",
      configurationJson: "{}",
    });
  }

  function updateOrganizerEntry(field: "projectId" | "projectName" | "repositoryPath" | "configurationJson", value: string): void {
    organizer.reset();
    setEntry((current) => current ? { ...current, [field]: value } : null);
  }

  function submitOrganizerEntry(apply: boolean): void {
    if (!entry?.requestId || entry.hostId !== host.id || stale) return;
    const preview = organizer.data?.status === "preview" ? organizer.data.preview : null;
    if (apply && (!preview || !preview.valid)) return;
    organizer.mutate({
      schemaVersion: PLANNING_SCHEMA_VERSION,
      contractVersion: PLANNING_CONTRACT_VERSION,
      contractHash: PLANNING_CONTRACT_SHA256,
      hostId: host.id,
      requestId: entry.requestId,
      kind: `${entry.mode}.${apply ? "apply" : "preview"}`,
      projectId: entry.projectId,
      projectName: entry.projectName,
      repositoryPath: entry.repositoryPath,
      configurationJson: entry.mode === "create" ? entry.configurationJson : null,
      previewId: apply ? preview!.id : null,
    });
  }

  function runAction(action: HomeAction): void {
    const enabled = homeActionEnabled({ action, expectedHostId: host.id, stale, navigationAvailable: navigation !== undefined });
    if (!enabled) return;
    if (action.kind === "create_project") {
      openOrganizerEntry("create");
      return;
    }
    if (action.kind === "adopt_organizer") {
      openOrganizerEntry("adopt");
      return;
    }
    if ((action.kind === "open_board" || action.kind === "open_organizer") && action.paseoWorkspaceId) {
      navigation?.openWorkspace({ workspaceId: action.paseoWorkspaceId });
      return;
    }
    if (action.kind === "doctor" || action.kind === "open_needs_you") {
      setInspectedProject(action.projectId);
      return;
    }
    const intent = mutationIntent(action);
    if (intent && action.command) {
      control.mutate(bindPlanningMutation(action.command, intent));
    }
  }

  function actionButton(action: HomeAction) {
    const enabled = homeActionEnabled({ action, expectedHostId: host.id, stale, navigationAvailable: navigation !== undefined }) && !control.isPending;
    const hint = action.unavailableReason?.message ?? (navigation === undefined && (action.kind === "open_board" || action.kind === "open_organizer")
      ? "This Paseo host does not expose native navigation"
      : undefined);
    const iconColor = action.emphasis === "primary"
      ? theme.colors.accentForeground
      : action.emphasis === "danger"
        ? theme.colors.statusDanger
        : theme.colors.foreground;
    return (
      <Pressable
        accessibilityHint={hint}
        accessibilityRole="button"
        accessibilityState={{ disabled: !enabled }}
        disabled={!enabled}
        key={`${action.hostId}:${action.projectId ?? "home"}:${action.kind}`}
        onPress={() => runAction(action)}
        style={actionStyle(action, enabled)}
      >
        <Icon color={iconColor} name={actionIcons[action.kind]} size={16} />
        <Text style={[styles.actionText, action.emphasis === "primary" && styles.primaryActionText]}>{action.label}</Text>
      </Pressable>
    );
  }

  function healthTextStyle(project: HomeProject) {
    if (project.health === "healthy") return styles.successText;
    if (project.health === "needs_you" || project.health === "disconnected") return styles.dangerText;
    return styles.warningText;
  }

  function projectCard(project: HomeProject) {
    return (
      <View key={homeProjectKey(host.id, project.id)} style={styles.card}>
        <View style={styles.cardHeader}>
          <Text accessibilityRole="header" style={styles.cardTitle}>{project.name}</Text>
          <View accessibilityLabel={`Project health: ${healthLabels[project.health]}`} style={styles.health}>
            <Text style={[styles.healthText, healthTextStyle(project)]}>{healthLabels[project.health]}</Text>
          </View>
        </View>
        <View style={styles.facts}>
          <Text style={styles.fact}>{project.tasks.open} open</Text>
          <Text style={styles.fact}>{project.activeWork.total} active</Text>
          <Text style={styles.fact}>{project.tasks.needsYou} need you</Text>
        </View>
        <View style={styles.projectDetails}>
          <View style={styles.detailGroup}>
            <Text style={styles.detailLabel}>Work</Text>
            <Text style={styles.body}>
              {project.activeWork.building} building · {project.activeWork.validating} validating · {project.activeWork.inReview} review · {project.activeWork.ready} ready
            </Text>
          </View>
          <View style={styles.detailGroup}>
            <Text style={styles.detailLabel}>Organizer</Text>
            <Text style={styles.body}>
              {project.organizer.mode} · {project.organizer.configurationState} · {project.organizer.organizerRevision?.slice(0, 7) ?? "revision unavailable"}
            </Text>
          </View>
          <View style={styles.detailGroup}>
            <Text style={styles.detailLabel}>Operations</Text>
            <Text style={styles.body}>
              Lease {project.lease.state} · Git {project.sync.git} · Data {project.sync.dynamicState}
            </Text>
          </View>
        </View>
        {project.healthReasons[0] ? <Text style={healthTextStyle(project)}>{project.healthReasons[0].message}</Text> : null}
        <View style={styles.chips}>
          {project.workspaces.map((workspace) => (
            <View key={`${host.id}:${project.id}:${workspace.id}`} style={styles.chip}>
              <Text style={styles.chipText}>{workspace.name} · {healthLabels[workspace.health]}</Text>
            </View>
          ))}
        </View>
        <View style={styles.actions}>{project.actions.map(actionButton)}</View>
      </View>
    );
  }

  const entryPreviewDisabled = entry === null || entry.hostId !== host.id || stale || entry.requestId === null ||
    entry.projectId === "" || entry.projectName === "" || entry.repositoryPath === "" || organizer.isPending;
  const entryApplyDisabled = entryPreviewDisabled || organizer.data?.status !== "preview" || organizer.data.preview?.valid !== true;
  const inspected = snapshot?.page.projects.find((project) => project.id === inspectedProject) ?? null;

  const errorCopy = scene.kind === "error"
    ? scene.code === "contract"
      ? ["Director contract changed", "The response was rejected before any Project or action could be displayed."]
      : scene.code === "host"
        ? ["Director host identity changed", "The response did not match this exact Paseo host. No other host was selected."]
        : ["Director host is offline", "No current Home snapshot is available for this exact host."]
    : null;

  return (
    <>
      <ScrollView contentContainerStyle={styles.screen}>
        <View style={styles.toolbar}>
          <View style={styles.panel}>
            <Text accessibilityRole="header" style={styles.heading}>Project health</Text>
            <Text style={styles.body}>Current work and attention across this host’s Director Projects.</Text>
          </View>
          {snapshot ? <View style={styles.actions}>{snapshot.page.surfaceActions.map(actionButton)}</View> : (
            <View style={styles.actions}>
              {(["Create Project", "Adopt Organizer"] as const).map((label) => (
                <Pressable accessibilityRole="button" accessibilityState={{ disabled: true }} disabled key={label} style={[styles.action, styles.disabledAction]}>
                  <Text style={styles.actionText}>{label}</Text>
                </Pressable>
              ))}
            </View>
          )}
        </View>
        {snapshot ? (
          <Text selectable style={styles.operationalLine}>Engine {snapshot.page.host.instanceId} · host ID {snapshot.page.host.id}</Text>
        ) : null}

      {scene.kind === "loading" ? (
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          <ActivityIndicator color={theme.colors.accent} />
          <Text style={styles.cardTitle}>Loading Director Home</Text>
          <Text style={[styles.body, styles.centered]}>Reading this exact host’s engine projection.</Text>
        </View>
      ) : null}
      {scene.kind === "error" && errorCopy ? (
        <View accessibilityLiveRegion="polite" style={[styles.liveState, styles.bannerDanger]}>
          <Text style={styles.cardTitle}>{errorCopy[0]}</Text>
          <Text style={[styles.body, styles.centered]}>{errorCopy[1]}</Text>
          <Pressable accessibilityRole="button" onPress={() => void home.refetch()} style={[styles.action, styles.primaryAction]}>
            <Text style={[styles.actionText, styles.primaryActionText]}>Try again</Text>
          </Pressable>
        </View>
      ) : null}
      {scene.kind === "stale" ? (
        <View accessibilityLiveRegion="polite" style={[styles.banner, styles.bannerDanger]}>
          <Text style={styles.bannerTitle}>Host offline or snapshot stale</Text>
          <Text style={styles.body}>Showing last known facts for {scene.snapshot.page.host.id}. Every action is disabled until this exact host returns current data.</Text>
          <Pressable
            accessibilityRole="button"
            onPress={() => void queryClient.resetQueries({ queryKey: ["director", "home", host.id], exact: true })}
            style={[styles.action, styles.more]}
          >
            <Text style={styles.actionText}>Refresh exact host</Text>
          </Pressable>
        </View>
      ) : null}
      {scene.kind === "partial_sync" ? (
        <View accessibilityLiveRegion="polite" style={styles.banner}>
          <Text style={styles.bannerTitle}>Partial sync</Text>
          <Text style={styles.body}>Organizer Git and dynamic-state sync are separate effects. The successful stream remains recorded while the failed stream stays visible and retryable.</Text>
        </View>
      ) : null}
      {scene.kind === "needs_you" ? (
        <View accessibilityLiveRegion="polite" style={[styles.banner, styles.bannerDanger]}>
          <Text style={styles.bannerTitle}>Needs your attention</Text>
          <Text style={styles.body}>One or more Projects have an exact engine-routed human decision. Open Needs you for bounded reasons.</Text>
        </View>
      ) : null}
      {scene.kind === "degraded" ? (
        <View accessibilityLiveRegion="polite" style={styles.banner}>
          <Text style={styles.bannerTitle}>Director is degraded</Text>
          <Text style={styles.body}>Current bounded facts are shown below. Missing or contradictory health is never displayed as healthy.</Text>
        </View>
      ) : null}
      {snapshot ? (
        <>
          <View style={styles.totals}>
            {[
              [snapshot.page.totals.projects, "Projects"],
              [snapshot.page.totals.healthy, "Healthy"],
              [snapshot.page.totals.degraded, "Degraded"],
              [snapshot.page.totals.paused, "Paused"],
              [snapshot.page.totals.needsYou, "Need you"],
              [snapshot.page.totals.activeWork, "Active work"],
            ].map(([value, label]) => (
              <View accessible accessibilityLabel={`${label}: ${value}`} key={label} style={styles.total}>
                <Text style={styles.totalValue}>{value}</Text>
                <Text style={styles.totalLabel}>{label}</Text>
              </View>
            ))}
          </View>
          {scene.kind === "empty" ? (
            <View accessibilityLiveRegion="polite" style={styles.liveState}>
              <Text style={styles.cardTitle}>No Projects on this host</Text>
              <Text style={[styles.body, styles.centered]}>Create a Project or adopt an Organizer on {snapshot.page.host.label}.</Text>
            </View>
          ) : (
            <View style={styles.grid}>{snapshot.page.projects.map(projectCard)}</View>
          )}
          {home.hasNextPage ? (
            <Pressable
              accessibilityRole="button"
              accessibilityState={{ busy: home.isFetchingNextPage, disabled: home.isFetchingNextPage || stale }}
              disabled={home.isFetchingNextPage || stale}
              onPress={() => void home.fetchNextPage()}
              style={[styles.action, styles.primaryAction, styles.more, (home.isFetchingNextPage || stale) && styles.disabledAction]}
            >
              <Text style={[styles.actionText, styles.primaryActionText]}>{home.isFetchingNextPage ? "Loading…" : "Load more Projects"}</Text>
            </Pressable>
          ) : null}
        </>
      ) : null}
      </ScrollView>
      <Modal
        icon={<Icon color={theme.colors.foreground} name={entry?.mode === "adopt" ? "FolderSearch" : "FolderPlus"} size={18} />}
        onOpenChange={(open) => {
          if (!open) {
            organizer.reset();
            setEntry(null);
          }
        }}
        open={entry !== null}
        title={entry?.mode === "adopt" ? "Adopt Organizer" : "Create Project"}
      >
        <Modal.Content>
          <ScrollView contentContainerStyle={styles.modalContent}>
            {entry ? (
              <>
                <Text style={styles.body}>
                  {entry.mode === "create"
                    ? "Start with an Organizer repository and one or more exact Workspace identities. Director Engine will present the complete filesystem, Git, dynamic state, provider, and security Preview before Apply."
                    : "Select an existing Organizer on this daemon. Director Engine will validate its marker, schema, Git state, dynamic state, and every Workspace identity before Apply."}
                </Text>
                {entry.requestId === null ? (
                  <Text style={styles.dangerText}>This client cannot mint the required secure request identity. Preview and Apply remain disabled.</Text>
                ) : null}
                <View style={styles.field}>
                  <Text style={styles.fieldLabel}>Project ID</Text>
                  <TextInput accessibilityLabel="Project ID" autoCapitalize="none" autoCorrect={false} onChangeText={(value) => updateOrganizerEntry("projectId", value)} style={styles.input} value={entry.projectId} />
                </View>
                <View style={styles.field}>
                  <Text style={styles.fieldLabel}>Project name</Text>
                  <TextInput accessibilityLabel="Project name" onChangeText={(value) => updateOrganizerEntry("projectName", value)} style={styles.input} value={entry.projectName} />
                </View>
                <View style={styles.field}>
                  <Text style={styles.fieldLabel}>Organizer path on this host</Text>
                  <TextInput accessibilityLabel="Organizer path on this host" autoCapitalize="none" autoCorrect={false} onChangeText={(value) => updateOrganizerEntry("repositoryPath", value)} style={styles.input} value={entry.repositoryPath} />
                </View>
                {entry.mode === "create" ? (
                  <View style={styles.field}>
                    <Text style={styles.fieldLabel}>Exact paseo-director.json</Text>
                    <TextInput accessibilityLabel="Exact paseo-director.json" autoCapitalize="none" autoCorrect={false} multiline onChangeText={(value) => updateOrganizerEntry("configurationJson", value)} style={[styles.input, styles.configurationInput]} value={entry.configurationJson} />
                  </View>
                ) : null}
                <Text style={styles.body}>Nothing is applied without a fresh server-authenticated human confirmation of the exact Preview.</Text>
                {organizer.data?.status === "preview" && organizer.data.preview ? (
                  <View accessibilityLiveRegion="polite" style={styles.previewPanel}>
                    <Text style={styles.sectionTitle}>{organizer.data.preview.valid ? "Preview ready" : "Preview blocked"}</Text>
                    <Text selectable style={styles.body}>{organizer.data.preview.repositoryPath}</Text>
                    {organizer.data.preview.operations.map((operation) => <Text key={operation} style={styles.body}>• {operation}</Text>)}
                    {organizer.data.preview.files.map((file) => <Text key={file.path} style={styles.body}>{file.path} · {file.sha256.slice(0, 12)}</Text>)}
                    {organizer.data.preview.issues.map((issue) => <Text key={`${issue.code}:${issue.field}`} style={styles.dangerText}>{issue.field}: {issue.message}</Text>)}
                  </View>
                ) : null}
                {organizer.isError || organizer.data?.status === "rejected" ? (
                  <Text style={styles.dangerText}>{organizer.data?.message ?? "Organizer Preview/Apply is unavailable on this exact host."}</Text>
                ) : null}
                <View style={styles.actions}>
                  <Pressable
                    accessibilityRole="button"
                    accessibilityState={{ disabled: entryPreviewDisabled }}
                    disabled={entryPreviewDisabled}
                    onPress={() => submitOrganizerEntry(false)}
                    style={[styles.action, styles.primaryAction, entryPreviewDisabled && styles.disabledAction]}
                  >
                    <Icon color={theme.colors.accentForeground} name="ScanSearch" size={16} />
                    <Text style={[styles.actionText, styles.primaryActionText]}>{organizer.isPending ? "Checking…" : "Preview"}</Text>
                  </Pressable>
                  <Pressable
                    accessibilityRole="button"
                    accessibilityState={{ disabled: entryApplyDisabled }}
                    disabled={entryApplyDisabled}
                    onPress={() => submitOrganizerEntry(true)}
                    style={[styles.action, organizer.data?.status === "preview" && organizer.data.preview?.valid === true ? styles.dangerAction : styles.disabledAction]}
                  >
                    <Icon color={theme.colors.foreground} name="CheckCircle2" size={16} />
                    <Text style={styles.actionText}>Apply exact Preview</Text>
                  </Pressable>
                </View>
              </>
            ) : null}
          </ScrollView>
        </Modal.Content>
      </Modal>
      <Modal
        icon={<Icon color={inspected?.health === "needs_you" ? theme.colors.statusDanger : theme.colors.foreground} name={inspected?.health === "needs_you" ? "CircleAlert" : "Stethoscope"} size={18} />}
        onOpenChange={(open) => { if (!open) setInspectedProject(null); }}
        open={inspected !== null}
        title={inspected?.health === "needs_you" ? `Needs you · ${inspected.name}` : `Doctor · ${inspected?.name ?? "Project"}`}
      >
        <Modal.Content>
          <ScrollView contentContainerStyle={styles.modalContent}>
            {inspected ? (
              <>
                <Text style={styles.body}>Health {healthLabels[inspected.health]} · lease {inspected.lease.state} · sync {inspected.sync.state}</Text>
                {inspected.healthReasons.length === 0 && inspected.needsYouReasons.length === 0 ? (
                  <Text style={styles.body}>No bounded health reason is currently reported.</Text>
                ) : null}
                {inspected.healthReasons.map((reason) => <Text key={reason.code} style={styles.body}>{reason.message}</Text>)}
                {inspected.needsYouReasons.map((reason) => <Text key={reason.code} style={styles.dangerText}>{readableCode(reason.code)} · {reason.count}</Text>)}
              </>
            ) : null}
          </ScrollView>
        </Modal.Content>
      </Modal>
    </>
  );
}
