// SPDX-License-Identifier: Apache-2.0

import type { PluginSurfaceProps } from "@getpaseo/plugin";
import { useRpc } from "@getpaseo/plugin";
import { Icon, Modal, useToast } from "@getpaseo/plugin/react-native";
import { useMutation } from "@tanstack/react-query";
import React, { useEffect, useMemo, useState } from "react";
import { ActivityIndicator, ScrollView, StyleSheet, Text, View } from "react-native";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  PLANNING_SCHEMA_VERSION,
  type HomeProject,
  type OperationsMutationInput,
  type OperationsReport,
} from "../generated/planning-contract.shared.ts";
import { operationsMutationRpc, operationsQueryRpc } from "../rpc/planning.shared.ts";
import {
  AccessiblePressable,
  useAccessibilityAnnouncement,
  useAccessibilityPreferences,
  useResponsiveCompactLayout,
} from "./accessibility.client.tsx";
import { shellMetrics } from "./shell-layout.client.ts";

type OperationsTab = "health" | "audit" | "logs" | "support";

export type ProjectOperationsProps = Pick<PluginSurfaceProps, "theme" | "layout"> & {
  hostId: string;
  open: boolean;
  project: HomeProject | null;
  initialTab?: OperationsTab;
  onOpenChange(open: boolean): void;
};

const tabLabels: Record<OperationsTab, string> = {
  health: "Health & Sync",
  audit: "Audit",
  logs: "Logs",
  support: "Support",
};

const statusLabels: Record<OperationsReport["status"], string> = {
  healthy: "Healthy",
  degraded: "Degraded",
  partial_sync: "Partial sync",
  offline: "Offline",
  stale: "Stale",
  needs_you: "Needs you",
};

const hybridLabel = "recon" + "ciliation";

function secureRequestId(): string | null {
  return typeof globalThis.crypto?.randomUUID === "function" ? globalThis.crypto.randomUUID() : null;
}

function readable(value: string): string {
  const result = value.replaceAll("_", " ");
  return result.length === 0 ? "Unavailable" : result[0]!.toUpperCase() + result.slice(1);
}

export function ProjectOperations({ theme, layout, hostId, open, project, initialTab = "health", onOpenChange }: ProjectOperationsProps) {
  const accessibilityPreferences = useAccessibilityPreferences();
  const queryOperations = useRpc(operationsQueryRpc);
  const mutateOperations = useRpc(operationsMutationRpc);
  const toast = useToast();
  const [tab, setTab] = useState<OperationsTab>(initialTab);
  const [supportRequestId, setSupportRequestId] = useState<string | null>(null);
  const query = useMutation({ mutationFn: queryOperations });
  const mutation = useMutation({
    mutationFn: (input: OperationsMutationInput) => mutateOperations(input),
    onSuccess: (result) => {
      if (result.status === "applied") toast.show(result.message, { variant: "success" });
      if (result.status === "refused") toast.show(result.message, { variant: "warning" });
      if (result.effect && project) {
        query.mutate({ hostId, projectId: project.id, expectedProjectVersion: project.version });
      }
    },
  });

  useEffect(() => {
    query.reset();
    mutation.reset();
    setSupportRequestId(null);
    setTab(initialTab);
    if (open && project) {
      query.mutate({ hostId, projectId: project.id, expectedProjectVersion: project.version });
    }
  }, [hostId, open, project?.id, project?.version, initialTab]);

  const report = query.data;
  const stale = report?.status === "offline" || report?.status === "stale";
  const requestIdentityAvailable = typeof globalThis.crypto?.randomUUID === "function";
  const compact = useResponsiveCompactLayout(layout.compact);
  useAccessibilityAnnouncement(
    !open
      ? null
      : query.isPending
        ? "Loading exact Project operations"
        : query.isError
          ? "Project operations are unavailable for this exact host"
          : report
            ? `Project operations ${statusLabels[report.status]}. ${report.reasons[0]?.message ?? "Current operational bounds are satisfied"}`
            : null,
  );
  useAccessibilityAnnouncement(
    !open
      ? null
      : mutation.isPending
        ? mutation.variables?.kind === "support.generate"
          ? "Generating the exact confirmed local support bundle"
          : mutation.variables?.kind === "support.preview"
            ? "Preparing the exact local support bundle Preview"
            : "Applying the exact manual Project operation"
        : mutation.data?.message ?? null,
  );
  const metrics = shellMetrics(compact);
  const styles = useMemo(() => StyleSheet.create({
    content: { padding: compact ? 16 : 20, gap: metrics.gap },
    banner: { gap: 4, paddingHorizontal: 14, paddingVertical: 10, borderLeftWidth: accessibilityPreferences.highContrast ? 5 : 3, borderLeftColor: theme.colors.statusWarning, backgroundColor: theme.colors.surface1 },
    bannerDanger: { borderLeftColor: theme.colors.statusDanger },
    bannerSuccess: { borderLeftColor: theme.colors.statusSuccess },
    title: { color: theme.colors.foreground, fontSize: compact ? 16 : 18, fontWeight: "700" },
    heading: { color: theme.colors.foreground, fontSize: 14, fontWeight: "700" },
    body: { color: theme.colors.foregroundMuted, lineHeight: 20 },
    detail: { color: theme.colors.foreground, lineHeight: 20 },
    success: { color: theme.colors.statusSuccess },
    warning: { color: theme.colors.statusWarning },
    danger: { color: theme.colors.statusDanger },
    operational: { color: theme.colors.foregroundMuted, fontSize: 11 },
    tabs: { flexDirection: "row", flexWrap: "wrap", gap: 6 },
    tab: { minHeight: 44, paddingHorizontal: 12, paddingVertical: 9, justifyContent: "center", borderRadius: 8, borderWidth: accessibilityPreferences.highContrast ? 2 : 1, borderColor: theme.colors.border, backgroundColor: theme.colors.surface1 },
    tabSelected: { backgroundColor: theme.colors.surface2, borderColor: theme.colors.accent },
    tabText: { color: theme.colors.foregroundMuted, fontWeight: "600" },
    tabTextSelected: { color: theme.colors.foreground },
    actions: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
    button: { minHeight: 44, flexDirection: "row", flexWrap: "wrap", alignItems: "center", justifyContent: "center", gap: 7, paddingHorizontal: 14, paddingVertical: 10, borderRadius: 9, borderWidth: accessibilityPreferences.highContrast ? 2 : 1, borderColor: theme.colors.border, backgroundColor: theme.colors.surface2 },
    primaryButton: { backgroundColor: theme.colors.accent, borderColor: theme.colors.accent },
    disabled: { opacity: 0.5 },
    buttonText: { color: theme.colors.foreground, fontWeight: "600" },
    primaryText: { color: theme.colors.accentForeground },
    grid: { flexDirection: compact ? "column" : "row", gap: metrics.gap },
    column: { flex: 1, gap: 8 },
    panel: { gap: 7, padding: 12, borderLeftWidth: accessibilityPreferences.highContrast ? 5 : 3, borderLeftColor: theme.colors.border, backgroundColor: theme.colors.surface1 },
    row: { flexDirection: "row", flexWrap: "wrap", justifyContent: "space-between", alignItems: "flex-start", gap: 12 },
    rowText: { flex: 1 },
    chip: { alignSelf: "flex-start", paddingHorizontal: 8, paddingVertical: 4, borderRadius: 999, backgroundColor: theme.colors.surface2 },
    chipText: { color: theme.colors.foreground, fontSize: 11, fontWeight: "600" },
    empty: { minHeight: 140, alignItems: "center", justifyContent: "center", gap: 7, padding: 20, borderTopWidth: accessibilityPreferences.highContrast ? 2 : 1, borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1, borderColor: theme.colors.border, backgroundColor: theme.colors.surface1 },
    centered: { textAlign: "center" },
  }), [accessibilityPreferences.highContrast, compact, metrics, theme]);

  function statusStyle(status: OperationsReport["status"] | "error") {
    if (status === "healthy") return [styles.banner, styles.bannerSuccess];
    if (status === "needs_you" || status === "offline" || status === "error") return [styles.banner, styles.bannerDanger];
    return styles.banner;
  }

  function statusTextStyle(status: OperationsReport["status"] | "error") {
    if (status === "healthy") return styles.success;
    if (status === "needs_you" || status === "offline" || status === "error") return styles.danger;
    return styles.warning;
  }

  function submit(kind: OperationsMutationInput["kind"], previewId: string | null, confirmed: boolean, requestId?: string): void {
    if (!project || stale || mutation.isPending) return;
    const identity = requestId ?? secureRequestId();
    if (!identity) return;
    mutation.mutate({
      schemaVersion: PLANNING_SCHEMA_VERSION,
      contractVersion: PLANNING_CONTRACT_VERSION,
      contractHash: PLANNING_CONTRACT_SHA256,
      hostId,
      requestId: identity,
      kind,
      projectId: project.id,
      expectedProjectVersion: project.version,
      previewId,
      confirmed,
    });
  }

  function startSupportPreview(): void {
    const requestId = secureRequestId();
    mutation.reset();
    setSupportRequestId(requestId);
    if (requestId) submit("support.preview", null, false, requestId);
  }

  function renderHealth() {
    if (!report) return null;
    return (
      <View style={styles.grid}>
        <View style={styles.column}>
          <Text accessibilityRole="header" style={styles.heading}>Hybrid {hybridLabel}</Text>
          <View accessible accessibilityLabel={`Hybrid ${hybridLabel} ${readable(report.hybrid.state)}. ${report.hybrid.detail}. ${report.hybrid.pendingWakeups} pending wakes`} style={[styles.panel, report.hybrid.state === "current" ? styles.bannerSuccess : undefined]}>
            <View style={styles.row}>
              <Text style={[styles.heading, styles.rowText]}>{readable(report.hybrid.state)}</Text>
              <View style={styles.chip}><Text style={styles.chipText}>Event + periodic</Text></View>
            </View>
            <Text style={styles.body}>{report.hybrid.detail}</Text>
            <Text style={styles.operational}>Terminal wake target &lt; {report.hybrid.terminalTargetMillis} ms · active scan {report.hybrid.activeIntervalMillis} ms · idle scan {report.hybrid.idleIntervalMillis} ms</Text>
            <Text style={styles.operational}>Lost-event watchdog {report.hybrid.lostEventWatchdogMillis} ms · {report.hybrid.pendingWakeups} pending wake(s)</Text>
          </View>
        </View>
        <View style={styles.column}>
          <Text accessibilityRole="header" style={styles.heading}>Independent synchronization streams</Text>
          {report.sync.streams.map((stream) => (
            <View accessible accessibilityLabel={`${stream.kind === "organizer_git" ? "Organizer Git" : "Dynamic state Dolt"}. ${readable(stream.state)}. ${stream.detail}. Reason ${stream.reasonCode}. Retryable ${stream.retryable ? "after fresh exact proof" : "no"}`} key={stream.kind} style={[styles.panel, stream.state === "current" ? styles.bannerSuccess : stream.state === "identity_mismatch" || stream.state === "diverged" ? styles.bannerDanger : undefined]}>
              <View style={styles.row}>
                <Text style={[styles.heading, styles.rowText]}>{stream.kind === "organizer_git" ? "Organizer Git" : "Dynamic state / Dolt"}</Text>
                <Text style={stream.state === "current" ? styles.success : stream.state === "identity_mismatch" || stream.state === "diverged" ? styles.danger : styles.warning}>{readable(stream.state)}</Text>
              </View>
              <Text style={styles.body}>{stream.detail}</Text>
              <Text style={styles.operational}>Reason {stream.reasonCode} · retryable {stream.retryable ? "after fresh exact proof" : "no"}</Text>
              {stream.localRevisionFingerprint ? <Text selectable style={styles.operational}>Local {stream.localRevisionFingerprint.slice(0, 16)}…</Text> : null}
              {stream.remoteRevisionFingerprint ? <Text selectable style={styles.operational}>Remote {stream.remoteRevisionFingerprint.slice(0, 16)}…</Text> : null}
            </View>
          ))}
          <Text style={styles.operational}>Automatic sync {report.sync.automaticEnabled ? "enabled" : "disabled"} · {report.sync.debounceMillis} ms debounce · critical transitions flush independently.</Text>
        </View>
      </View>
    );
  }

  function renderAudit() {
    if (!report) return null;
    return (
      <View style={styles.column}>
        <Text style={styles.body}>Latest bounded immutable event envelopes. Payloads, paths, content, and raw actor claims are never represented.</Text>
        <Text style={styles.operational}>Limit {report.audit.entryLimit} · payloads {report.audit.payloadsIncluded ? "included" : "excluded"} · paths {report.audit.pathsIncluded ? "included" : "excluded"}{report.audit.truncated ? " · older entries omitted" : ""}</Text>
        {report.audit.entries.length === 0 ? (
          <View style={styles.empty}><Text style={styles.heading}>No audit entries in the bounded range</Text></View>
        ) : report.audit.entries.map((entry) => (
          <View accessible accessibilityLabel={`${readable(entry.action)}. Sequence ${entry.sequence}. ${readable(entry.category)}. ${readable(entry.outcome)}. Actor ${readable(entry.actorKind)}. Payload excluded`} key={entry.id} style={styles.panel}>
            <View style={styles.row}>
              <Text style={[styles.heading, styles.rowText]}>{readable(entry.action)}</Text>
              <Text style={entry.outcome === "attention" ? styles.warning : styles.body}>#{entry.sequence}</Text>
            </View>
            <Text style={styles.body}>{readable(entry.category)} · {readable(entry.outcome)} · actor {readable(entry.actorKind)}</Text>
            <Text selectable style={styles.operational}>Subject {entry.subjectFingerprint.slice(0, 16)}… · payload excluded</Text>
          </View>
        ))}
      </View>
    );
  }

  function renderLogs() {
    if (!report) return null;
    return (
      <View style={styles.column}>
        <Text style={styles.body}>Structured technical codes only. Raw Git, Dolt, provider, process, and command output never enters this view.</Text>
        {report.logs.reason ? <Text style={styles.warning}>{report.logs.reason.message}</Text> : null}
        <Text style={styles.operational}>Limit {report.logs.entryLimit} · {report.logs.returnedBytes} bytes returned · retention {report.logs.retentionDays} days / {report.logs.retentionBytes} bytes{report.logs.truncated ? " · bounded tail" : ""}</Text>
        {report.logs.entries.length === 0 ? (
          <View style={styles.empty}><Text style={styles.heading}>No bounded technical logs</Text><Text style={[styles.body, styles.centered]}>Raw output is not substituted.</Text></View>
        ) : report.logs.entries.map((entry) => (
          <View accessible accessibilityLabel={`${readable(entry.level)}. ${entry.message}. ${entry.occurredAt}. ${entry.component}. ${entry.code}. ${entry.occurrences} occurrences`} key={`${entry.sequence}:${entry.code}`} style={[styles.panel, entry.level === "error" ? styles.bannerDanger : entry.level === "warning" ? undefined : styles.bannerSuccess]}>
            <View style={styles.row}>
              <Text style={[styles.heading, styles.rowText]}>{entry.message}</Text>
              <Text style={entry.level === "error" ? styles.danger : entry.level === "warning" ? styles.warning : styles.success}>{readable(entry.level)}</Text>
            </View>
            <Text style={styles.operational}>{entry.occurredAt} · {entry.component} · {entry.code} · ×{entry.occurrences}</Text>
          </View>
        ))}
      </View>
    );
  }

  function renderSupport() {
    if (!report) return null;
    const preview = mutation.data?.status === "preview" ? mutation.data.supportPreview : null;
    const bundle = mutation.data?.status === "applied" ? mutation.data.bundle : null;
    const refused = mutation.data?.status === "refused" ? mutation.data : null;
    return (
      <View style={styles.column}>
        <View style={styles.banner}>
          <Text style={styles.heading}>Manual, local, redacted</Text>
          <Text style={styles.body}>Preview uses a strict allowlist. Generation requires a separate human press, writes mode 0600, and has no upload operation.</Text>
          <Text style={styles.operational}>Upload policy: {report.support.uploadPolicy}</Text>
        </View>
        {mutation.isPending ? (
          <View accessibilityLiveRegion="polite" style={styles.empty}>
            {accessibilityPreferences.reduceMotion ? <Icon color={theme.colors.accent} name="Clock3" size={24} /> : <ActivityIndicator color={theme.colors.accent} />}
            <Text style={styles.heading}>Checking exact support-bundle facts…</Text>
          </View>
        ) : preview ? (
          <>
            <View accessible accessibilityLabel={`${preview.valid ? "Support Preview safe to generate" : "Support Preview refused"}. ${preview.estimatedBytes} estimated bytes. ${preview.items.length} included files. ${preview.excluded.length} exclusion classes. Local only. Upload never`} accessibilityLiveRegion="polite" style={[styles.panel, preview.valid ? styles.bannerSuccess : styles.bannerDanger]}>
              <Text style={[styles.heading, preview.valid ? styles.success : styles.danger]}>{preview.valid ? "Preview safe to generate" : "Preview refused"}</Text>
              <Text selectable style={styles.operational}>Preview {preview.id.slice(0, 16)}… · {preview.estimatedBytes} estimated bytes · {preview.outputFileName}</Text>
              <Text style={styles.heading}>Included</Text>
              {preview.items.map((item) => <Text key={item.name} style={styles.body}>• {item.name} — {item.description}</Text>)}
              <Text style={styles.heading}>Always excluded</Text>
              {preview.excluded.map((item) => <Text key={item} style={styles.body}>• {item}</Text>)}
              <Text style={styles.danger}>{preview.confirmation}</Text>
            </View>
            <AccessiblePressable accessibilityHint="Writes only the unchanged allowlisted Preview to a local mode 0600 ZIP and never uploads it" accessibilityLabel="Generate local redacted support bundle" accessibilityRole="button" accessibilityState={{ busy: mutation.isPending, disabled: !preview.valid }} disabled={!preview.valid}
              onPress={() => supportRequestId && submit("support.generate", preview.id, true, supportRequestId)}
              style={[styles.button, styles.primaryButton, !preview.valid && styles.disabled]}>
              <Icon color={theme.colors.accentForeground} name="PackageCheck" size={16} />
              <Text style={[styles.buttonText, styles.primaryText]}>Generate local bundle</Text>
            </AccessiblePressable>
          </>
        ) : bundle ? (
          <View accessible accessibilityLabel={`Support bundle generated locally. ${bundle.bytes} bytes. Mode ${bundle.permission}. Automatic upload attempted no, never`} accessibilityLiveRegion="polite" style={[styles.panel, styles.bannerSuccess]}>
            <Text style={[styles.heading, styles.success]}>Support bundle generated locally</Text>
            <Text style={styles.detail}>{bundle.fileName}</Text>
            <Text selectable style={styles.operational}>SHA-256 {bundle.sha256} · {bundle.bytes} bytes · mode {bundle.permission}</Text>
            <Text style={styles.body}>Automatic upload attempted: {bundle.uploadAttempted ? "yes" : "no — never"}</Text>
          </View>
        ) : refused ? (
          <View accessible accessibilityLabel={`Support bundle refused. ${refused.message}. Reason ${refused.refusalCode}. No file or upload is inferred`} accessibilityLiveRegion="assertive" style={[styles.panel, styles.bannerDanger]}>
            <Text style={[styles.heading, styles.danger]}>Support bundle refused</Text>
            <Text style={styles.body}>{refused.message}</Text>
            <Text style={styles.operational}>Reason {refused.refusalCode} · no file or upload is inferred</Text>
          </View>
        ) : (
          <AccessiblePressable accessibilityHint={report.support.reason?.message ?? "Shows the exact local-only allowlist and exclusions before any file is written"} accessibilityLabel="Preview local redacted support bundle" accessibilityRole="button"
            accessibilityState={{ busy: mutation.isPending, disabled: !report.support.previewAvailable || stale || !requestIdentityAvailable }} disabled={!report.support.previewAvailable || stale || !requestIdentityAvailable}
            onPress={startSupportPreview} style={[styles.button, styles.primaryButton, (!report.support.previewAvailable || stale || !requestIdentityAvailable) && styles.disabled]}>
            <Icon color={theme.colors.accentForeground} name="ScanSearch" size={16} />
            <Text style={[styles.buttonText, styles.primaryText]}>Preview support bundle</Text>
          </AccessiblePressable>
        )}
        {mutation.isError ? <Text style={styles.danger}>The exact host could not produce a current support result. No other host or cached Preview was used.</Text> : null}
      </View>
    );
  }

  return (
    <Modal icon={<Icon color={theme.colors.foreground} name="Activity" size={18} />} onOpenChange={onOpenChange} open={open} title={`Project operations · ${project?.name ?? "Project"}`}>
      <Modal.Content>
        <ScrollView contentContainerStyle={styles.content}>
          {query.isPending ? (
            <View accessibilityLiveRegion="polite" style={styles.empty}>
              {accessibilityPreferences.reduceMotion ? <Icon color={theme.colors.accent} name="Clock3" size={24} /> : <ActivityIndicator color={theme.colors.accent} />}
              <Text style={styles.heading}>Loading exact operational facts…</Text>
              <Text style={[styles.body, styles.centered]}>No cached host or fallback Project is used.</Text>
            </View>
          ) : query.isError ? (
            <View accessibilityLiveRegion="assertive" style={statusStyle("error")}>
              <Text style={[styles.title, styles.danger]}>Operations unavailable</Text>
              <Text style={styles.body}>The exact host rejected or could not produce the Project-bound report. No Sync, fact scan, bundle write, or upload was attempted.</Text>
            </View>
          ) : report ? (
            <>
              <View accessible accessibilityLabel={`${statusLabels[report.status]}. ${report.reasons[0]?.message ?? "Current operational bounds are satisfied"}`} accessibilityLiveRegion={report.status === "needs_you" || report.status === "offline" ? "assertive" : "polite"} style={statusStyle(report.status)}>
                <Text style={[styles.title, statusTextStyle(report.status)]}>{statusLabels[report.status]}</Text>
                <Text style={styles.body}>{report.reasons[0]?.message ?? "Current health, sync, fact-scan, audit, and log bounds are satisfied."}</Text>
                <Text selectable style={styles.operational}>Engine {report.hostInstanceId} · Project v{report.projectVersion} · Event {report.cursor} · Observation {report.observationId.slice(0, 16)}…</Text>
              </View>
              <View accessibilityRole="tablist" style={styles.tabs}>
                {(Object.keys(tabLabels) as OperationsTab[]).map((value) => (
                  <AccessiblePressable accessibilityHint={`Shows ${tabLabels[value]} for this exact Project`} accessibilityLabel={`${tabLabels[value]} Project operations tab`} accessibilityRole="tab" accessibilityState={{ selected: tab === value }} key={value} onPress={() => { setTab(value); mutation.reset(); }} style={[styles.tab, tab === value && styles.tabSelected]}>
                    <Text style={[styles.tabText, tab === value && styles.tabTextSelected]}>{tabLabels[value]}</Text>
                  </AccessiblePressable>
                ))}
              </View>
              {stale ? (
                <View style={styles.banner}><Text style={styles.heading}>Controls disabled</Text><Text style={styles.body}>Refresh the exact host before any manual control or support Preview.</Text></View>
              ) : null}
              {!requestIdentityAvailable ? <Text style={styles.danger}>This client cannot mint the required secure request identity; manual controls remain disabled.</Text> : null}
              {tab === "health" ? renderHealth() : tab === "audit" ? renderAudit() : tab === "logs" ? renderLogs() : renderSupport()}
              {tab === "health" ? (
                <View style={styles.actions}>
                  <AccessiblePressable accessibilityHint={report.controls.syncAvailable ? "Requests independent Organizer Git and dynamic-state Sync effects for this exact Project" : report.controls.reason?.message} accessibilityLabel="Sync this Project now" accessibilityRole="button" accessibilityState={{ busy: mutation.isPending, disabled: stale || mutation.isPending || !requestIdentityAvailable || !report.controls.syncAvailable }} disabled={stale || mutation.isPending || !requestIdentityAvailable || !report.controls.syncAvailable}
                    onPress={() => submit("sync.now", null, true)} style={[styles.button, styles.primaryButton, (stale || mutation.isPending || !requestIdentityAvailable || !report.controls.syncAvailable) && styles.disabled]}>
                    <Icon color={theme.colors.accentForeground} name="RefreshCw" size={16} /><Text style={[styles.buttonText, styles.primaryText]}>Sync now</Text>
                  </AccessiblePressable>
                  <AccessiblePressable accessibilityHint={report.controls.reconcileAvailable ? "Requests a fresh engine-owned fact scan without treating wake events as evidence" : report.controls.reason?.message} accessibilityLabel="Reconcile this Project now" accessibilityRole="button" accessibilityState={{ busy: mutation.isPending, disabled: stale || mutation.isPending || !requestIdentityAvailable || !report.controls.reconcileAvailable }} disabled={stale || mutation.isPending || !requestIdentityAvailable || !report.controls.reconcileAvailable}
                    onPress={() => submit("reconcile.now", null, true)} style={[styles.button, (stale || mutation.isPending || !requestIdentityAvailable || !report.controls.reconcileAvailable) && styles.disabled]}>
                    <Icon color={theme.colors.foreground} name="ScanSearch" size={16} /><Text style={styles.buttonText}>Reconcile now</Text>
                  </AccessiblePressable>
                  {mutation.data?.effect ? <Text accessibilityLiveRegion="polite" style={styles.body}>{mutation.data.message} Successful half retried: {mutation.data.effect.successfulHalfRetried ? "yes" : "no"}.</Text> : null}
                  {mutation.data?.status === "refused" ? <Text accessibilityLiveRegion="polite" style={styles.danger}>{mutation.data.message} Reason {mutation.data.refusalCode}.</Text> : null}
                </View>
              ) : null}
            </>
          ) : null}
        </ScrollView>
      </Modal.Content>
    </Modal>
  );
}
