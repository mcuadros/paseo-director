// SPDX-License-Identifier: Apache-2.0
// Deterministic test-only engine adapter for the generated planning client.

import { createHash } from "node:crypto";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  PLANNING_SCHEMA_VERSION,
  PLANNING_STABLE_SORTS,
  planningMutationInputSchema,
  planningMutationResultSchema,
  planningQueryInputSchema,
  planningSnapshotSchema,
  taskDetailQueryInputSchema,
  taskDetailSnapshotSchema,
  type AllowedAction,
  type AllowedActionKind,
  type AttentionCode,
  type ConfigurationEntry,
  type ConfigurationOverride,
  type ConfigurationPreview,
  type DerivedState,
  type PlanningClient,
  type PlanningMutationInput,
  type PlanningQueryInput,
  type PlanningSnapshot,
  type Priority,
  type ProjectSummary,
  type StableSort,
  type TaskDetailSnapshot,
  type TaskSummary,
  type WorkspaceSummary,
} from "../../generated/planning-contract.shared.ts";

const projectId = "project-director";
const labels = ["m2", "ui", "backend"] as const;
const priorities = ["urgent", "high", "normal", "low"] as const;
const openStates = [
  "needs_you",
  "queued",
  "building",
  "validating",
  "in_review",
  "ready",
] as const;

function count(value: number): string {
  return String(value);
}

function action(
  kind: AllowedActionKind,
  targetId: string | null,
  version: string,
  options: {
    label?: string;
    approval?: boolean;
    acknowledgementRevision?: string | null;
  } = {},
): AllowedAction {
  const token = `${kind}-${targetId ?? "surface"}`.replaceAll(".", "-");
  return {
    kind,
    label: options.label ?? kind,
    targetId,
    requestId: `request-${token}`,
    idempotencyKey: `idempotency-${token}`,
    expectedVersion: version,
    humanApprovalRef: options.approval ? `approval-${token}` : null,
    acknowledgementRevision: options.acknowledgementRevision ?? null,
    emphasis: kind === "task.launch-now" ? "primary" : "secondary",
  };
}

function taskActions(index: number, version: string): readonly AllowedAction[] {
  const taskId = `task-${index}`;
  const actions: AllowedAction[] = [
    action("task.update", taskId, version, { label: "Edit task" }),
    action("configuration.preview", taskId, version, {
      label: "Preview configuration",
    }),
  ];
  if (index % 6 === 1) {
    actions.push(
      action("task.launch-now", taskId, version, { label: "Launch now" }),
    );
  }
  if (index === 0) {
    actions.push(
      action("dependency.override", "dependency-0", version, {
        label: "Override dependency",
        approval: true,
        acknowledgementRevision: "4",
      }),
    );
  }
  return actions;
}

function makeTask(index: number, historical: boolean): TaskSummary {
  const version = String((index % 17) + 1);
  const state = historical ? "done" : openStates[index % openStates.length];
  const blocker = index === 0
    ? [{
        code: "dependency-wait",
        message: "Waiting for DIR-DEPENDENCY",
        wakeCondition: "dependency dependency-0 is complete",
        humanActionRequired: false,
      }]
    : [];
  const needsYou = index === 0
    ? [{
        code: "policy_override_required",
        message: "A human may approve a scoped dependency override",
        wakeCondition: "approval revision 4 is applied",
        humanActionRequired: true,
      }]
    : [];
  return {
    id: `task-${index}`,
    projectId,
    workspaceId: `workspace-${index % 25}`,
    epicId: `epic-${index % 10}`,
    version,
    key: `DIR-${String(index + 1).padStart(5, "0")}`,
    title: index === 0
      ? "Contract-first planning UI shell"
      : `${historical ? "Historical" : "Open"} task ${index + 1}`,
    derivedState: state,
    priority: priorities[index % priorities.length],
    labels: [labels[index % labels.length]],
    updatedAt: new Date(Date.UTC(2026, 8, 9, 0, 0, index % 60)).toISOString(),
    blockers: blocker,
    needsYou,
    allowedActions: taskActions(index, version),
    schedulingFacts: {
      launchMode: index % 2 === 0 ? "automatic" : "manual",
      launchDisposition: state === "needs_you"
        ? "needs_you"
        : state === "queued"
          ? "eligible"
          : "waiting",
      queuePosition: state === "queued" ? String(index + 1) : null,
      factsRevision: "12",
      explanations: blocker,
    },
  };
}

function configuration(): readonly ConfigurationEntry[] {
  return [
    {
      key: "launchPolicy",
      configured: { key: "launchPolicy", mode: "inherit" },
      effectiveValue: "automatic",
      effectiveSource: "project",
      allowedValues: ["manual", "automatic"],
    },
    {
      key: "maxSubagentsPerTask",
      configured: { key: "maxSubagentsPerTask", mode: "value", value: "3" },
      effectiveValue: "3",
      effectiveSource: "task",
      allowedValues: ["1", "2", "3"],
    },
    {
      key: "autoFixCiFailures",
      configured: { key: "autoFixCiFailures", mode: "inherit" },
      effectiveValue: true,
      effectiveSource: "project",
      allowedValues: [true, false],
    },
  ];
}

export class DeterministicPlanningFixture implements PlanningClient {
  readonly projects: readonly ProjectSummary[];
  readonly workspaces: readonly WorkspaceSummary[];
  readonly tasks: readonly TaskSummary[];
  readonly openTaskCount = 500;
  readonly historicalTaskCount = 10_000;
  readonly requests: PlanningQueryInput[] = [];
  readonly mutations: PlanningMutationInput[] = [];
  #snapshot = 1;

  constructor() {
    this.projects = [{
      id: projectId,
      version: "9",
      name: "Director",
      state: "active",
      workspaceCount: "25",
      taskCounts: { open: "500", done: "10000", needsYou: "1" },
      allowedActions: [
        action("project.update", projectId, "9", { label: "Edit project" }),
        action("epic.create", projectId, "9", { label: "Create epic" }),
        action("task.create", projectId, "9", { label: "Create task" }),
      ],
    }];
    this.workspaces = Array.from({ length: 25 }, (_, index) => ({
      id: `workspace-${index}`,
      projectId,
      version: "3",
      name: `Workspace ${String(index + 1).padStart(2, "0")}`,
      health: index === 24 ? "degraded" as const : "healthy" as const,
      defaultBaseBranch: "main",
      taskCounts: { open: "20", done: "400", needsYou: index === 0 ? "1" : "0" },
      allowedActions: [
        action("task.create", `workspace-${index}`, "3", { label: "Create task" }),
      ],
    }));
    this.tasks = [
      ...Array.from({ length: this.openTaskCount }, (_, index) => makeTask(index, false)),
      ...Array.from(
        { length: this.historicalTaskCount },
        (_, index) => makeTask(this.openTaskCount + index, true),
      ),
    ];
  }

  async query(rawInput: PlanningQueryInput): Promise<PlanningSnapshot> {
    const input = planningQueryInputSchema.parse(rawInput);
    this.requests.push(input);
    const selectedProjectId = input.projectId ?? projectId;
    let matches = this.tasks.filter((task) => task.projectId === selectedProjectId);
    if (input.workspaceIds.length > 0) {
      matches = matches.filter((task) => input.workspaceIds.includes(task.workspaceId));
    }
    if (input.epicIds.length > 0) {
      matches = matches.filter(
        (task) => task.epicId !== null && input.epicIds.includes(task.epicId),
      );
    }
    if (input.states.length > 0) {
      matches = matches.filter((task) => input.states.includes(task.derivedState));
    } else {
      matches = matches.filter((task) => task.derivedState !== "done");
    }
    if (input.priorities.length > 0) {
      matches = matches.filter((task) => input.priorities.includes(task.priority));
    }
    if (input.labels.length > 0) {
      matches = matches.filter((task) =>
        input.labels.every((label) => task.labels.includes(label)),
      );
    }
    if (input.attention.length > 0) {
      matches = matches.filter((task) =>
        task.needsYou.some((explanation) => input.attention.includes(
          explanation.code as AttentionCode,
        )),
      );
    }
    if (input.search !== null) {
      const search = input.search.toLocaleLowerCase("en-US");
      matches = matches.filter((task) =>
        `${task.key} ${task.title}`.toLocaleLowerCase("en-US").includes(search),
      );
    }
    matches = this.sortTasks(matches, input.sort);
    const fingerprint = this.queryFingerprint(input);
    const offset = input.cursor === null
      ? 0
      : this.decodeCursor(input.cursor, fingerprint);
    const selected = matches.slice(offset, offset + input.pageSize);
    const nextOffset = offset + selected.length;
    return planningSnapshotSchema.parse({
      schemaVersion: PLANNING_SCHEMA_VERSION,
      contractVersion: PLANNING_CONTRACT_VERSION,
      contractHash: PLANNING_CONTRACT_SHA256,
      cursor: count(this.#snapshot),
      page: {
        selectedProjectId,
        selectedWorkspaceIds: input.workspaceIds,
        selectedEpicIds: input.epicIds,
        appliedQuery: input,
        availableSorts: PLANNING_STABLE_SORTS,
        availableLabels: labels,
        projects: this.projects,
        workspaces: selectedProjectId === projectId ? this.workspaces : [],
        epics: selectedProjectId === projectId
          ? Array.from({ length: 10 }, (_, index) => ({
              id: `epic-${index}`,
              projectId,
              version: "2",
              key: `M2-${index + 1}`,
              title: `Milestone epic ${index + 1}`,
              priority: priorities[index % priorities.length] as Priority,
              labels: [labels[index % labels.length]],
              progress: { completed: "0", total: "50" },
              blockers: [],
              allowedActions: [
                action("epic.update", `epic-${index}`, "2", { label: "Edit epic" }),
              ],
            }))
          : [],
        tasks: selected,
        capacity: {
          activeTasks: "3",
          maxActiveTasks: "4",
          activeWorkspaceTasks: "1",
          maxActiveTasksPerWorkspace: "2",
          activeAgents: "5",
          maxConcurrentAgents: "8",
          reservedAgents: "1",
        },
        surfaceActions: [
          action("project.create", null, "0", { label: "Create Project" }),
        ],
        totalTasks: count(matches.length),
        nextCursor: nextOffset < matches.length
          ? this.encodeCursor(nextOffset, fingerprint)
          : null,
      },
    });
  }

  async taskDetail(rawInput: Parameters<PlanningClient["taskDetail"]>[0]): Promise<TaskDetailSnapshot> {
    const input = taskDetailQueryInputSchema.parse(rawInput);
    const summary = this.tasks.find((task) => task.id === input.taskId);
    if (!summary) throw new Error("fixture task not found");
    return taskDetailSnapshotSchema.parse({
      schemaVersion: PLANNING_SCHEMA_VERSION,
      contractVersion: PLANNING_CONTRACT_VERSION,
      contractHash: PLANNING_CONTRACT_SHA256,
      cursor: count(this.#snapshot),
      detail: {
        summary,
        objective: "Render only engine-owned planning facts and submit typed intents.",
        acceptanceCriteria: [{
          id: "criterion-contract",
          text: "The generated contract remains the only client source.",
          status: "pending",
        }],
        dependencies: summary.id === "task-0"
          ? [{
              id: "dependency-0",
              kind: "task",
              key: "DIR-DEPENDENCY",
              title: "Integrate prerequisite",
              satisfied: false,
              overrideStatus: "none",
              explanation: summary.blockers[0],
            }]
          : [],
        configurationTarget: { scope: "task", id: summary.id },
        configuration: configuration(),
        configurationPreview: null,
        activity: [{
          id: "activity-created",
          occurredAt: "2026-09-09T00:00:00.000Z",
          kind: "planning",
          message: "Task projection created",
        }],
      },
    });
  }

  async mutate(rawInput: PlanningMutationInput) {
    const input = planningMutationInputSchema.parse(rawInput);
    this.mutations.push(input);
    this.#snapshot += 1;
    const preview = input.intent.type === "configuration.preview"
      ? this.previewFor(input.intent.target, input.intent.overrides, input.expectedVersion)
      : null;
    return planningMutationResultSchema.parse({
      schemaVersion: PLANNING_SCHEMA_VERSION,
      contractVersion: PLANNING_CONTRACT_VERSION,
      contractHash: PLANNING_CONTRACT_SHA256,
      cursor: count(this.#snapshot),
      requestId: input.requestId,
      status: "accepted",
      message: preview ? "Configuration preview is ready" : "Intent accepted",
      updatedVersion: preview ? null : input.expectedVersion,
      preview,
    });
  }

  private previewFor(
    target: { scope: "project" | "workspace" | "task"; id: string },
    overrides: readonly ConfigurationOverride[],
    expectedVersion: string,
  ): ConfigurationPreview {
    const entries = configuration();
    const diff = overrides.flatMap((override) => {
      const entry = entries.find((candidate) => candidate.key === override.key);
      if (!entry) return [];
      const after = override.mode === "inherit" ? entry.effectiveValue : override.value;
      return after === entry.effectiveValue
        ? []
        : [{
            key: entry.key,
            before: entry.effectiveValue,
            after,
            effectiveSource: override.mode === "inherit" ? entry.effectiveSource : target.scope,
          }];
    });
    return {
      previewId: `preview-${target.id}`,
      target,
      valid: true,
      issues: [],
      diff,
      applyAction: action("configuration.apply", target.id, expectedVersion, {
        label: "Apply configuration",
        approval: true,
        acknowledgementRevision: "7",
      }),
    };
  }

  private sortTasks(tasks: readonly TaskSummary[], sort: StableSort): TaskSummary[] {
    const result = [...tasks];
    const priorityRank: Record<Priority, number> = { urgent: 0, high: 1, normal: 2, low: 3 };
    if (sort === "scheduler_order") {
      const stateRank = new Map<DerivedState, number>(
        openStates.map((state, index) => [state, index]),
      );
      stateRank.set("done", openStates.length);
      return result.sort(
        (left, right) =>
          (stateRank.get(left.derivedState) ?? 99) - (stateRank.get(right.derivedState) ?? 99) ||
          priorityRank[left.priority] - priorityRank[right.priority] ||
          left.key.localeCompare(right.key) ||
          left.id.localeCompare(right.id),
      );
    }
    if (sort === "updated_desc") {
      return result.sort(
        (left, right) => right.updatedAt.localeCompare(left.updatedAt) || left.id.localeCompare(right.id),
      );
    }
    if (sort === "key_asc") {
      return result.sort((left, right) => left.key.localeCompare(right.key));
    }
    return result.sort(
      (left, right) =>
        priorityRank[left.priority] - priorityRank[right.priority] ||
        left.key.localeCompare(right.key) ||
        left.id.localeCompare(right.id),
    );
  }

  private queryFingerprint(input: PlanningQueryInput): string {
    const canonical = {
      ...input,
      workspaceIds: [...input.workspaceIds].sort(),
      epicIds: [...input.epicIds].sort(),
      states: [...input.states].sort(),
      priorities: [...input.priorities].sort(),
      labels: [...input.labels].sort(),
      attention: [...input.attention].sort(),
      search: input.search?.toLocaleLowerCase("en-US") ?? null,
      cursor: null,
    };
    return createHash("sha256").update(JSON.stringify(canonical)).digest("hex");
  }

  private encodeCursor(offset: number, fingerprint: string): string {
    return Buffer.from(JSON.stringify({
      v: 1,
      snapshot: count(this.#snapshot),
      filter: fingerprint,
      offset,
    })).toString("base64url");
  }

  private decodeCursor(cursor: string, fingerprint: string): number {
    let value: unknown;
    try {
      value = JSON.parse(Buffer.from(cursor, "base64url").toString("utf8"));
    } catch {
      throw new Error("fixture cursor is invalid");
    }
    if (
      value === null ||
      typeof value !== "object" ||
      (value as Record<string, unknown>).v !== 1 ||
      (value as Record<string, unknown>).snapshot !== count(this.#snapshot) ||
      (value as Record<string, unknown>).filter !== fingerprint ||
      !Number.isSafeInteger((value as Record<string, unknown>).offset) ||
      Number((value as Record<string, unknown>).offset) < 0
    ) {
      throw new Error("fixture cursor belongs to another snapshot or filter");
    }
    return Number((value as Record<string, unknown>).offset);
  }
}
