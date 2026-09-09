// SPDX-License-Identifier: Apache-2.0
// Paseo RPC transports the root-workspace Director Workers aggregate.

import { defineRpc } from "@getpaseo/plugin/server";
import { z } from "zod";

import {
  WORKER_LABEL,
  WORKER_REGISTRY_SCHEMA_VERSION,
  WORKER_ROLES,
} from "../generated/host-contract.shared.ts";

export const workerLabel = WORKER_LABEL;
export const workerRoles = WORKER_ROLES;

/** Paseo caps a single agent directory page at 200 entries. */
export const WORKER_PAGE_LIMIT = 100;
export const WORKER_MAXIMUM = 1_000;

const identity = z.string().min(1).max(200).regex(/^[A-Za-z0-9][A-Za-z0-9._:@/-]*$/u);
const phase = z.string().regex(/^[a-z][a-z0-9_]{0,63}$/u);
const commitSha = z.string().regex(/^[0-9a-f]{40}$/u);
const instant = z
  .string()
  .max(64)
  .regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/u)
  .refine((value) => Number.isFinite(Date.parse(value)), {
    message: "worker timestamp is not a real instant",
  });

const directorWorker = z.strictObject({
  agentId: identity,
  workspaceId: identity,
  title: z.string().min(1).max(512),
  status: z.enum(["initializing", "idle", "running", "error", "closed"]),
  taskId: identity,
  runId: identity,
  role: z.enum(WORKER_ROLES),
  phase,
  candidate: commitSha.nullable(),
  base: commitSha.nullable(),
  registeredAt: instant,
  startedAt: instant,
});

export const directorWorkersRpc = defineRpc({
  name: "director.workers",
  input: z.strictObject({ rootWorkspaceId: identity }),
  output: z.strictObject({
    schemaVersion: z.literal(WORKER_REGISTRY_SCHEMA_VERSION),
    rootWorkspaceId: identity,
    workers: z.array(directorWorker).max(WORKER_MAXIMUM).readonly(),
  }),
});

export type DirectorWorkersSnapshot = z.output<typeof directorWorkersRpc.output>;
export type DirectorWorker = DirectorWorkersSnapshot["workers"][number];
export type DirectorWorkerRole = DirectorWorker["role"];
export type DirectorWorkerStatus = DirectorWorker["status"];
