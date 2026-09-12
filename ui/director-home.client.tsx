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
  type DoctorQueryInput,
  type PlanningMutationInput,
  type PlanningMutationIntent,
  type OrganizerBootstrapInput,
  type RepairInput,
} from "../generated/planning-contract.shared.ts";
import {
  doctorQueryRpc,
  homeQueryRpc,
  planningMutationRpc,
  organizerBootstrapRpc,
  repairProjectRpc,
} from "../rpc/planning.shared.ts";
import {
  AccessibilityProvider,
  AccessiblePressable,
  useAccessibilityAnnouncement,
  useAccessibilityPreferences,
  useResponsiveCompactLayout,
} from "./accessibility.client.tsx";
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
  const accessibilityPreferences = useAccessibilityPreferences();
  const loadHome = useRpc(homeQueryRpc);
  const mutatePlanning = useRpc(planningMutationRpc);
  const bootstrapOrganizer = useRpc(organizerBootstrapRpc);
  const queryDoctor = useRpc(doctorQueryRpc);
  const repairProject = useRpc(repairProjectRpc);
  const toast = useToast();
  const queryClient = useQueryClient();
  const [entry, setEntry] = useState<OrganizerEntry | null>(null);
  const [inspectedProject, setInspectedProject] = useState<string | null>(null);
  const [repairRequest, setRepairRequest] = useState<{
    projectId: string;
    projectVersion: string;
    requestId: string;
  } | null>(null);
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
  const doctor = useMutation({
    mutationFn: (input: DoctorQueryInput) => queryDoctor(input),
  });
  const repair = useMutation({
    mutationFn: (input: RepairInput) => repairProject(input),
    onSuccess: async (result) => {
      if (result.status === "applied") {
        toast.show(result.message, { variant: "success" });
        await queryClient.resetQueries({ queryKey: ["director", "home", host.id], exact: true });
      } else if (result.status === "refused") {
        toast.show(result.message, { variant: "warning" });
      }
    },
  });
  useEffect(() => {
    setEntry(null);
    setInspectedProject(null);
    setRepairRequest(null);
    organizer.reset();
    control.reset();
    doctor.reset();
    repair.reset();
  }, [host.id]);
  const snapshot = "snapshot" in scene ? scene.snapshot : null;
  const stale = scene.kind === "stale";
  const compact = useResponsiveCompactLayout(layout.compact);
  const homeAnnouncement = scene.kind === "loading"
    ? "Loading Director Home"
    : scene.kind === "error"
      ? "Director Home is unavailable for this exact host"
      : scene.kind === "stale"
        ? "Host offline or snapshot stale. Every action is disabled"
        : scene.kind === "empty"
          ? "No Projects on this host"
          : scene.kind === "needs_you"
            ? "One or more Projects need your attention"
            : snapshot
              ? `${snapshot.page.projects.length} Projects shown. ${snapshot.page.totals.activeWork} active work items`
              : null;
  useAccessibilityAnnouncement(homeAnnouncement);
  useAccessibilityAnnouncement(
    organizer.isPending
      ? "Checking Organizer Preview"
      : organizer.data?.message ?? control.data?.message ?? null,
  );
  useAccessibilityAnnouncement(
    doctor.isPending
      ? "Running read-only Doctor"
      : doctor.isError
        ? "Doctor is unavailable for this exact host"
        : doctor.data
          ? `Doctor ${readableCode(doctor.data.status)}. ${doctor.data.blockingCount} blocking checks`
          : null,
  );
  useAccessibilityAnnouncement(
    repair.isPending
      ? repair.variables?.kind === "repair.apply"
        ? "Applying the exact confirmed Repair"
        : "Preparing the exact Repair Preview"
      : repair.isError
        ? "Repair is unavailable for this exact host"
        : repair.data?.message ?? null,
  );
  const metrics = shellMetrics(compact);
  const styles = useMemo(
    () => StyleSheet.create({
      screen: {
        flexGrow: 1,
        padding: metrics.padding,
        gap: metrics.gap,
        backgroundColor: theme.colors.surface0,
      },
      toolbar: {
        flexDirection: compact ? "column" : "row",
        justifyContent: "space-between",
        alignItems: compact ? "flex-start" : "center",
        gap: metrics.gap,
      },
      heading: { color: theme.colors.foreground, fontSize: compact ? 18 : 20, fontWeight: "700" },
      body: { color: theme.colors.foregroundMuted, lineHeight: 20 },
      operationalLine: { color: theme.colors.foregroundMuted, fontSize: 11 },
      banner: {
        paddingHorizontal: 14,
        paddingVertical: 10,
        gap: 4,
        borderLeftWidth: accessibilityPreferences.highContrast ? 5 : 3,
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
        borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
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
        gap: compact ? 12 : 24,
        paddingVertical: 10,
        borderTopWidth: accessibilityPreferences.highContrast ? 2 : 1,
        borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
        borderColor: theme.colors.border,
      },
      total: {
        minWidth: compact ? "28%" : 90,
        gap: 2,
      },
      totalValue: { color: theme.colors.foreground, fontSize: 18, fontWeight: "700" },
      totalLabel: { color: theme.colors.foregroundMuted, fontSize: 12 },
      grid: {
        borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
        borderColor: theme.colors.border,
        borderRadius: 12,
        overflow: "hidden",
        backgroundColor: theme.colors.surface1,
      },
      card: {
        width: "100%",
        padding: 16,
        gap: 9,
        borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
        borderBottomColor: theme.colors.border,
      },
      cardHeader: { flexDirection: "row", flexWrap: "wrap", justifyContent: "space-between", alignItems: "flex-start", gap: 8 },
      cardTitle: { flex: 1, color: theme.colors.foreground, fontSize: 17, fontWeight: "700" },
      health: { paddingHorizontal: 9, paddingVertical: 5, borderRadius: 999, borderWidth: accessibilityPreferences.highContrast ? 2 : 1, borderColor: theme.colors.border, backgroundColor: theme.colors.surface2 },
      healthText: { color: theme.colors.foreground, fontSize: 12, fontWeight: "600" },
      warningText: { color: theme.colors.statusWarning },
      dangerText: { color: theme.colors.statusDanger },
      successText: { color: theme.colors.statusSuccess },
      facts: { flexDirection: "row", flexWrap: "wrap", gap: 12 },
      fact: { color: theme.colors.foregroundMuted, fontSize: 12 },
      projectDetails: { flexDirection: compact ? "column" : "row", gap: compact ? 8 : 24 },
      detailGroup: { flex: 1, gap: 3 },
      sectionTitle: { color: theme.colors.foreground, fontWeight: "600" },
      detailLabel: { color: theme.colors.foregroundMuted, fontSize: 11, fontWeight: "600", textTransform: "uppercase" },
      chips: { flexDirection: "row", flexWrap: "wrap", gap: 6 },
      chip: { paddingHorizontal: 8, paddingVertical: 5, borderRadius: 7, backgroundColor: theme.colors.surface2 },
      chipText: { color: theme.colors.foregroundMuted, fontSize: 12 },
      panel: {
        gap: 9,
      },
      modalContent: { padding: compact ? 16 : 20, gap: 12 },
      previewPanel: { padding: 12, gap: 7, borderLeftWidth: 3, borderLeftColor: theme.colors.accent, backgroundColor: theme.colors.surface1 },
      field: { gap: 5 },
      fieldLabel: { color: theme.colors.foreground, fontSize: 12, fontWeight: "600" },
      input: {
        minHeight: 44,
        paddingHorizontal: 12,
        paddingVertical: 10,
        borderRadius: 8,
        borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
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
        borderTopWidth: accessibilityPreferences.highContrast ? 2 : 1,
        borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
        borderColor: theme.colors.border,
        backgroundColor: theme.colors.surface1,
      },
      centered: { textAlign: "center" },
      more: { alignSelf: "center" },
    }),
    [accessibilityPreferences.highContrast, compact, metrics, theme],
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
    if ((action.kind === "doctor" || action.kind === "open_needs_you") && action.projectId) {
      openDoctorForProject(action.projectId);
      return;
    }
    if (action.kind === "repair" && action.projectId) {
      openRepairForProject(action.projectId);
      return;
    }
    const intent = mutationIntent(action);
    if (intent && action.command) {
      control.mutate(bindPlanningMutation(action.command, intent));
    }
  }

  function openDoctorForProject(projectId: string): void {
    const target = snapshot?.page.projects.find((project) => project.id === projectId);
    if (!target || stale) return;
    setInspectedProject(projectId);
    doctor.reset();
    doctor.mutate({ hostId: host.id, projectId, expectedProjectVersion: target.version });
  }

  function openRepairForProject(projectId: string): void {
    const target = snapshot?.page.projects.find((project) => project.id === projectId);
    const requestId = organizerRequestId();
    if (!target || !requestId || stale) return;
    setRepairRequest({ projectId, projectVersion: target.version, requestId });
    repair.reset();
    repair.mutate({
      schemaVersion: PLANNING_SCHEMA_VERSION,
      contractVersion: PLANNING_CONTRACT_VERSION,
      contractHash: PLANNING_CONTRACT_SHA256,
      hostId: host.id,
      requestId,
      kind: "repair.preview",
      projectId,
      expectedProjectVersion: target.version,
      previewId: null,
      confirmed: false,
    });
  }

  function handleApplyRepair(): void {
    const preview = repair.data?.status === "preview" ? repair.data.preview : null;
    if (!repairRequest || !preview || !preview.valid || stale || repair.isPending) return;
    repair.mutate({
      schemaVersion: PLANNING_SCHEMA_VERSION,
      contractVersion: PLANNING_CONTRACT_VERSION,
      contractHash: PLANNING_CONTRACT_SHA256,
      hostId: host.id,
      requestId: repairRequest.requestId,
      kind: "repair.apply",
      projectId: repairRequest.projectId,
      expectedProjectVersion: repairRequest.projectVersion,
      previewId: preview.id,
      confirmed: true,
    });
  }

  function actionButton(action: HomeAction) {
    const busy = control.isPending ||
      ((action.kind === "doctor" || action.kind === "open_needs_you") && doctor.isPending) ||
      (action.kind === "repair" && repair.isPending);
    const enabled = homeActionEnabled({ action, expectedHostId: host.id, stale, navigationAvailable: navigation !== undefined }) && !busy;
    const hint = action.unavailableReason?.message ?? (navigation === undefined && (action.kind === "open_board" || action.kind === "open_organizer")
      ? "This Paseo host does not expose native navigation"
      : action.kind === "create_project"
        ? "Opens Paseo’s native dialog or compact bottom sheet to create a Project"
        : action.kind === "adopt_organizer"
          ? "Opens Paseo’s native dialog or compact bottom sheet to adopt an Organizer"
          : action.kind === "open_board" || action.kind === "open_organizer"
            ? "Opens the exact engine-projected Paseo workspace"
            : action.kind === "doctor" || action.kind === "open_needs_you"
              ? "Opens bounded current Project facts in Paseo’s native modal"
              : "Submits this engine-issued Project intent for the exact host");
    const iconColor = action.emphasis === "primary"
      ? theme.colors.accentForeground
      : action.emphasis === "danger"
        ? theme.colors.statusDanger
        : theme.colors.foreground;
    return (
      <AccessiblePressable
        accessibilityLabel={action.label}
        accessibilityHint={hint}
        accessibilityRole="button"
        accessibilityState={{ busy, disabled: !enabled }}
        disabled={!enabled}
        key={`${action.hostId}:${action.projectId ?? "home"}:${action.kind}`}
        onPress={() => runAction(action)}
        style={actionStyle(action, enabled)}
      >
        <Icon color={iconColor} name={actionIcons[action.kind]} size={16} />
        <Text style={[styles.actionText, action.emphasis === "primary" && styles.primaryActionText]}>{action.label}</Text>
      </AccessiblePressable>
    );
  }

  function healthTextStyle(project: HomeProject) {
    if (project.health === "healthy") return styles.successText;
    if (project.health === "needs_you" || project.health === "disconnected") return styles.dangerText;
    return styles.warningText;
  }

  function diagnosticTextStyle(status: string) {
    if (status === "healthy" || status === "passed") return styles.successText;
    if (status === "degraded" || status === "stale") return styles.warningText;
    return styles.dangerText;
  }

  function diagnosticBannerStyle(status: string) {
    if (status === "healthy" || status === "passed") return { borderLeftColor: theme.colors.statusSuccess };
    if (status === "degraded" || status === "stale") return { borderLeftColor: theme.colors.statusWarning };
    return styles.bannerDanger;
  }

  function projectCard(project: HomeProject) {
    return (
      <View key={homeProjectKey(host.id, project.id)} style={styles.card}>
        <View style={styles.cardHeader}>
          <Text accessibilityRole="header" style={styles.cardTitle}>{project.name}</Text>
          <View
            accessible
            accessibilityLabel={`Project health: ${healthLabels[project.health]}`}
            style={styles.health}
          >
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
  const repairTarget = snapshot?.page.projects.find((project) => project.id === repairRequest?.projectId) ?? null;

  const errorCopy = scene.kind === "error"
    ? scene.code === "contract"
      ? ["Director contract changed", "The response was rejected before any Project or action could be displayed."]
      : scene.code === "host"
        ? ["Director host identity changed", "The response did not match this exact Paseo host. No other host was selected."]
        : ["Director host is offline", "No current Home snapshot is available for this exact host."]
    : null;

  return (
    <AccessibilityProvider
      focusColor={theme.colors.accent}
      preferences={accessibilityPreferences}
    >
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
                <AccessiblePressable
                  accessibilityHint="Available after current exact-host Project facts load"
                  accessibilityLabel={label}
                  accessibilityRole="button"
                  accessibilityState={{ disabled: true }}
                  disabled
                  key={label}
                  style={[styles.action, styles.disabledAction]}
                >
                  <Text style={styles.actionText}>{label}</Text>
                </AccessiblePressable>
              ))}
            </View>
          )}
        </View>
        {snapshot ? (
          <Text selectable style={styles.operationalLine}>Engine {snapshot.page.host.instanceId} · host ID {snapshot.page.host.id}</Text>
        ) : null}

      {scene.kind === "loading" ? (
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          {accessibilityPreferences.reduceMotion ? (
            <Icon color={theme.colors.accent} name="Clock3" size={24} />
          ) : (
            <ActivityIndicator color={theme.colors.accent} />
          )}
          <Text style={styles.cardTitle}>Loading Director Home</Text>
          <Text style={[styles.body, styles.centered]}>Reading this exact host’s engine projection.</Text>
        </View>
      ) : null}
      {scene.kind === "error" && errorCopy ? (
        <View accessibilityLiveRegion="polite" style={[styles.liveState, styles.bannerDanger]}>
          <Text style={styles.cardTitle}>{errorCopy[0]}</Text>
          <Text style={[styles.body, styles.centered]}>{errorCopy[1]}</Text>
          <AccessiblePressable
            accessibilityHint="Retries Director Home on this exact host"
            accessibilityLabel="Try loading Director Home again"
            accessibilityRole="button"
            onPress={() => void home.refetch()}
            style={[styles.action, styles.primaryAction]}
          >
            <Text style={[styles.actionText, styles.primaryActionText]}>Try again</Text>
          </AccessiblePressable>
        </View>
      ) : null}
      {scene.kind === "stale" ? (
        <View accessibilityLiveRegion="polite" style={[styles.banner, styles.bannerDanger]}>
          <Text style={styles.bannerTitle}>Host offline or snapshot stale</Text>
          <Text style={styles.body}>Showing last known facts for {scene.snapshot.page.host.id}. Every action is disabled until this exact host returns current data.</Text>
          <AccessiblePressable
            accessibilityHint="Clears only this host’s cached Home snapshot and loads it again"
            accessibilityLabel="Refresh Director Home for this exact host"
            accessibilityRole="button"
            onPress={() => void queryClient.resetQueries({ queryKey: ["director", "home", host.id], exact: true })}
            style={[styles.action, styles.more]}
          >
            <Text style={styles.actionText}>Refresh exact host</Text>
          </AccessiblePressable>
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
            <AccessiblePressable
              accessibilityHint="Loads the next Project page from this exact engine snapshot"
              accessibilityLabel="Load more Director Projects"
              accessibilityRole="button"
              accessibilityState={{ busy: home.isFetchingNextPage, disabled: home.isFetchingNextPage || stale }}
              disabled={home.isFetchingNextPage || stale}
              onPress={() => void home.fetchNextPage()}
              style={[styles.action, styles.primaryAction, styles.more, (home.isFetchingNextPage || stale) && styles.disabledAction]}
            >
              <Text style={[styles.actionText, styles.primaryActionText]}>{home.isFetchingNextPage ? "Loading…" : "Load more Projects"}</Text>
            </AccessiblePressable>
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
                  <TextInput accessibilityHint="Enter the stable Director Project identifier" accessibilityLabel="Project ID" autoCapitalize="none" autoCorrect={false} onChangeText={(value) => updateOrganizerEntry("projectId", value)} style={styles.input} value={entry.projectId} />
                </View>
                <View style={styles.field}>
                  <Text style={styles.fieldLabel}>Project name</Text>
                  <TextInput accessibilityHint="Enter the human-readable Project name" accessibilityLabel="Project name" onChangeText={(value) => updateOrganizerEntry("projectName", value)} style={styles.input} value={entry.projectName} />
                </View>
                <View style={styles.field}>
                  <Text style={styles.fieldLabel}>Organizer path on this host</Text>
                  <TextInput accessibilityHint="Enter the exact Organizer repository path on this daemon" accessibilityLabel="Organizer path on this host" autoCapitalize="none" autoCorrect={false} onChangeText={(value) => updateOrganizerEntry("repositoryPath", value)} style={styles.input} value={entry.repositoryPath} />
                </View>
                {entry.mode === "create" ? (
                  <View style={styles.field}>
                    <Text style={styles.fieldLabel}>Exact paseo-director.json</Text>
                    <TextInput accessibilityHint="Enter the exact JSON configuration to include in the Preview" accessibilityLabel="Exact paseo-director.json" autoCapitalize="none" autoCorrect={false} multiline onChangeText={(value) => updateOrganizerEntry("configurationJson", value)} style={[styles.input, styles.configurationInput]} value={entry.configurationJson} />
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
                  <AccessiblePressable
                    accessibilityHint="Checks the entered Organizer facts without applying them"
                    accessibilityLabel="Preview Organizer changes"
                    accessibilityRole="button"
                    accessibilityState={{ busy: organizer.isPending, disabled: entryPreviewDisabled }}
                    disabled={entryPreviewDisabled}
                    onPress={() => submitOrganizerEntry(false)}
                    style={[styles.action, styles.primaryAction, entryPreviewDisabled && styles.disabledAction]}
                  >
                    <Icon color={theme.colors.accentForeground} name="ScanSearch" size={16} />
                    <Text style={[styles.actionText, styles.primaryActionText]}>{organizer.isPending ? "Checking…" : "Preview"}</Text>
                  </AccessiblePressable>
                  <AccessiblePressable
                    accessibilityHint="Applies only the unchanged exact Preview after confirmation"
                    accessibilityLabel="Apply exact Organizer Preview"
                    accessibilityRole="button"
                    accessibilityState={{ disabled: entryApplyDisabled }}
                    disabled={entryApplyDisabled}
                    onPress={() => submitOrganizerEntry(true)}
                    style={[styles.action, organizer.data?.status === "preview" && organizer.data.preview?.valid === true ? styles.dangerAction : styles.disabledAction]}
                  >
                    <Icon color={theme.colors.foreground} name="CheckCircle2" size={16} />
                    <Text style={styles.actionText}>Apply exact Preview</Text>
                  </AccessiblePressable>
                </View>
              </>
            ) : null}
          </ScrollView>
        </Modal.Content>
      </Modal>
      <Modal
        icon={<Icon color={inspected?.health === "needs_you" ? theme.colors.statusDanger : theme.colors.foreground} name={inspected?.health === "needs_you" ? "CircleAlert" : "Stethoscope"} size={18} />}
        onOpenChange={(open) => {
          if (!open) {
            setInspectedProject(null);
            doctor.reset();
          }
        }}
        open={inspected !== null}
        title={inspected?.health === "needs_you" ? `Needs you · ${inspected.name}` : `Doctor · ${inspected?.name ?? "Project"}`}
      >
        <Modal.Content>
          <ScrollView contentContainerStyle={styles.modalContent}>
            {doctor.isPending ? (
              <View accessibilityLiveRegion="polite" style={styles.liveState}>
                {accessibilityPreferences.reduceMotion ? (
                  <Icon color={theme.colors.accent} name="Clock3" size={24} />
                ) : (
                  <ActivityIndicator color={theme.colors.accent} />
                )}
                <Text style={styles.sectionTitle}>Running read-only Doctor…</Text>
                <Text style={[styles.body, styles.centered]}>Reading one exact host-bound engine snapshot. No repair, install, dispatch, or write is attempted.</Text>
              </View>
            ) : doctor.isError ? (
              <View accessibilityLiveRegion="assertive" style={[styles.banner, styles.bannerDanger]}>
                <Text style={[styles.bannerTitle, styles.dangerText]}>Doctor unavailable</Text>
                <Text style={styles.body}>The exact host rejected or could not produce current diagnostic facts. No other host or cached report was used.</Text>
                <Text style={styles.dangerText}>Missing capability: Current exact-host Doctor response</Text>
                <Text style={styles.body}>Restore this host’s matching Director Engine and connector contract, then run Doctor again.</Text>
              </View>
            ) : doctor.data ? (
              <>
                <View accessibilityLiveRegion="polite" style={[styles.banner, diagnosticBannerStyle(doctor.data.status)]}>
                  <Text style={[styles.bannerTitle, diagnosticTextStyle(doctor.data.status)]}>
                    Doctor {readableCode(doctor.data.status)} · {doctor.data.blockingCount} blocking
                  </Text>
                  <Text style={styles.body}>{doctor.data.assurance}</Text>
                  <Text selectable style={styles.operationalLine}>
                    Engine {doctor.data.hostInstanceId} · Project v{doctor.data.projectVersion} · Event {doctor.data.cursor} · Observation {doctor.data.observationId.slice(0, 16)}…
                  </Text>
                </View>

                <View style={styles.panel}>
                  <Text accessibilityRole="header" style={styles.sectionTitle}>Blocking preflight and capabilities</Text>
                  {doctor.data.checks.map((check) => (
                    <View
                      accessible
                      accessibilityLabel={`${readableCode(check.status)}. ${check.title}. ${check.detail}${check.missingCapability ? `. Missing capability: ${check.missingCapability}` : ""}`}
                      key={check.id}
                      style={[styles.banner, diagnosticBannerStyle(check.status)]}
                    >
                      <Text style={[styles.bannerTitle, diagnosticTextStyle(check.status)]}>
                        {readableCode(check.status)} · {check.title}
                      </Text>
                      <Text style={styles.body}>{check.detail}</Text>
                      {check.missingCapability ? <Text style={styles.dangerText}>Missing capability: {check.missingCapability}</Text> : null}
                      {check.installationGuidance.map((step) => <Text key={`${check.id}:${step}`} style={styles.body}>• {step}</Text>)}
                      {check.status !== "passed" ? <Text style={styles.operationalLine}>Code {check.code}</Text> : null}
                    </View>
                  ))}
                </View>

                <View style={styles.actions}>
                  <AccessiblePressable
                    accessibilityHint={doctor.data.repair.reason?.message ?? "Opens an exact engine-computed Repair Preview for human confirmation"}
                    accessibilityLabel="Preview Project Repair"
                    accessibilityRole="button"
                    accessibilityState={{ disabled: !doctor.data.repair.available || stale }}
                    disabled={!doctor.data.repair.available || stale}
                    onPress={() => {
                      const id = doctor.data.projectId;
                      setInspectedProject(null);
                      doctor.reset();
                      openRepairForProject(id);
                    }}
                    style={[styles.action, styles.primaryAction, (!doctor.data.repair.available || stale) && styles.disabledAction]}
                  >
                    <Icon color={theme.colors.accentForeground} name="Wrench" size={16} />
                    <Text style={[styles.actionText, styles.primaryActionText]}>Preview Repair…</Text>
                  </AccessiblePressable>
                  <AccessiblePressable
                    accessibilityHint="Closes this read-only Doctor report"
                    accessibilityLabel="Close Doctor"
                    accessibilityRole="button"
                    onPress={() => {
                      setInspectedProject(null);
                      doctor.reset();
                    }}
                    style={styles.action}
                  >
                    <Text style={styles.actionText}>Close</Text>
                  </AccessiblePressable>
                </View>
              </>
            ) : null}
          </ScrollView>
        </Modal.Content>
      </Modal>

      <Modal
        icon={<Icon color={theme.colors.statusWarning} name="Wrench" size={18} />}
        onOpenChange={(open) => {
          if (!open) {
            setRepairRequest(null);
            repair.reset();
          }
        }}
        open={repairRequest !== null}
        title={`Repair Project · ${repairTarget?.name ?? "Project"}`}
      >
        <Modal.Content>
          <ScrollView contentContainerStyle={styles.modalContent}>
            {repairRequest ? (
              <>
                <View accessibilityLiveRegion="polite" style={styles.banner}>
                  <Text style={styles.bannerTitle}>Two-Phase Repair: Preview & Human Confirmation</Text>
                  <Text style={styles.body}>
                    Doctor never mutates. Director Engine calculates and hashes the exact effects; the client cannot repair, install, or choose another host.
                  </Text>
                </View>

                {repair.isPending ? (
                  <View accessibilityLiveRegion="polite" style={styles.liveState}>
                    {accessibilityPreferences.reduceMotion ? (
                      <Icon color={theme.colors.accent} name="Clock3" size={24} />
                    ) : (
                      <ActivityIndicator color={theme.colors.accent} />
                    )}
                    <Text style={styles.sectionTitle}>{repair.variables?.kind === "repair.apply" ? "Applying exact confirmed Repair…" : "Preparing exact Repair Preview…"}</Text>
                  </View>
                ) : repair.isError ? (
                  <View accessibilityLiveRegion="assertive" style={[styles.banner, styles.bannerDanger]}>
                    <Text style={[styles.bannerTitle, styles.dangerText]}>Repair error</Text>
                    <Text style={styles.body}>The exact host could not complete this request. No effect, retry, installation, or cross-host fallback is inferred.</Text>
                    <Text style={styles.dangerText}>Missing capability: Current exact-host Repair response</Text>
                    <Text style={styles.body}>Restore the matching engine executor and current observations, then run Doctor and Preview again.</Text>
                  </View>
                ) : repair.data?.status === "applied" ? (
                  <View accessibilityLiveRegion="polite" style={[styles.banner, { borderLeftColor: theme.colors.statusSuccess }]}>
                    <Text style={[styles.bannerTitle, styles.successText]}>Repair applied</Text>
                    <Text style={styles.body}>{repair.data.message}</Text>
                    <Text style={styles.body}>Updated Project version {repair.data.projectVersion} · Event {repair.data.cursor}</Text>
                    <Text style={styles.operationalLine}>Success is engine readback, not client narration. Run Doctor again to verify health.</Text>
                  </View>
                ) : repair.data?.status === "refused" ? (
                  <View accessibilityLiveRegion="assertive" style={[styles.banner, styles.bannerDanger]}>
                    <Text style={[styles.bannerTitle, styles.dangerText]}>Repair refused</Text>
                    <Text style={styles.body}>{repair.data.message}</Text>
                    <Text style={styles.dangerText}>Reason {repair.data.refusalCode}</Text>
                    <Text style={styles.operationalLine}>Nothing was retried or redirected. Run Doctor and Preview again from current facts.</Text>
                  </View>
                ) : repair.data?.status === "preview" && repair.data.preview ? (
                  <>
                    <View accessibilityLiveRegion="polite" style={styles.previewPanel}>
                      <Text accessibilityRole="header" style={styles.sectionTitle}>
                        {repair.data.preview.valid ? "Repair Preview ready" : "Repair blocked"}
                      </Text>
                      <Text selectable style={styles.operationalLine}>
                        Preview {repair.data.preview.id.slice(0, 16)}… · Project v{repair.data.preview.projectVersion} · Event {repair.data.preview.cursor} · Observation {repair.data.preview.observationId.slice(0, 12)}…
                      </Text>
                      <Text style={[styles.detailLabel, { marginTop: 4 }]}>Exact effects</Text>
                      {repair.data.preview.operations.map((operation) => (
                        <View
                          accessible
                          accessibilityLabel={`${operation.description}. Affects ${operation.affectedResource}. ${readableCode(operation.effectClass)}. Non-destructive. No automatic installation`}
                          key={operation.id}
                          style={styles.field}
                        >
                          <Text style={styles.sectionTitle}>{operation.description}</Text>
                          <Text style={styles.body}>Affects {operation.affectedResource} · {readableCode(operation.effectClass)}</Text>
                          <Text style={styles.operationalLine}>Non-destructive · no automatic installation</Text>
                        </View>
                      ))}
                      {repair.data.preview.issues.map((issue) => (
                        <Text key={issue.code} style={styles.dangerText}>{issue.message}</Text>
                      ))}
                    </View>

                    <Text style={styles.dangerText}>{repair.data.preview.confirmation}</Text>

                    <View style={styles.actions}>
                      <AccessiblePressable
                        accessibilityHint="Applies only this unchanged engine-issued Repair Preview after server-authenticated human confirmation"
                        accessibilityLabel="Confirm exact Repair"
                        accessibilityRole="button"
                        accessibilityState={{ busy: repair.isPending, disabled: !repair.data.preview.valid || stale }}
                        disabled={!repair.data.preview.valid || stale}
                        onPress={handleApplyRepair}
                        style={[styles.action, styles.primaryAction, (!repair.data.preview.valid || stale) && styles.disabledAction]}
                      >
                        <Icon color={theme.colors.accentForeground} name="CheckCircle2" size={16} />
                        <Text style={[styles.actionText, styles.primaryActionText]}>Confirm exact Repair</Text>
                      </AccessiblePressable>
                      <AccessiblePressable
                        accessibilityHint="Closes this Repair Preview without applying effects"
                        accessibilityLabel="Cancel Repair"
                        accessibilityRole="button"
                        onPress={() => {
                          setRepairRequest(null);
                          repair.reset();
                        }}
                        style={styles.action}
                      >
                        <Text style={styles.actionText}>Cancel</Text>
                      </AccessiblePressable>
                    </View>
                  </>
                ) : null}
              </>
            ) : null}
          </ScrollView>
        </Modal.Content>
      </Modal>
      </>
    </AccessibilityProvider>
  );
}
