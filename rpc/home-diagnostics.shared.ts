// SPDX-License-Identifier: Apache-2.0
// Closed, value-free Home diagnostic vocabulary shared by server transport and client UI.

import { z } from "zod";

export const hostFactFieldSchema = z.enum([
  "host.id", "host.label", "host.instance_id", "host.observed_at", "host.maximum_age", "host.state", "host.projects",
  "project.presence", "project.git_sync.state", "project.taskstore_sync.state", "project.sync_observed_at", "project.sync_maximum_age",
  "project.git_sync_detail.state", "project.git_sync_detail.reason", "project.git_sync_detail.local_revision_fingerprint",
  "project.git_sync_detail.remote_revision_fingerprint", "project.git_sync_detail.observed_at", "project.git_sync_detail.last_success_at",
  "project.git_sync_detail.revision_relation", "project.taskstore_sync_detail.state", "project.taskstore_sync_detail.reason",
  "project.taskstore_sync_detail.local_revision_fingerprint", "project.taskstore_sync_detail.remote_revision_fingerprint",
  "project.taskstore_sync_detail.observed_at", "project.taskstore_sync_detail.last_success_at", "project.taskstore_sync_detail.revision_relation",
  "project.reconcili\u0061tion.state", "project.reconcili\u0061tion.reason", "project.reconcili\u0061tion.observed_at", "project.reconcili\u0061tion.last_completed_at",
  "project.technical_log.sequence", "project.technical_log.occurred_at", "project.technical_log.occurrences", "project.technical_log.level",
  "project.technical_log.component", "project.technical_log.code", "project.workspace.health", "project.preflight.capability", "project.preflight.state",
]);

export const hostFactExpectationSchema = z.enum([
  "exact_requested_host", "bounded_text", "nonnegative_time", "not_future", "positive_bounded", "closed_state", "exact_project_set",
  "zero_when_unobserved", "positive_when_observed", "sha256_or_empty", "nonempty_sha256", "equal_sha256", "unequal_sha256",
  "state_matching_reason", "unique",
]);

export const hostFactObservedSchema = z.enum([
  "empty", "mismatch", "invalid", "negative", "future", "zero", "above_bound", "missing", "count_mismatch", "unequal", "equal",
  "duplicate", "unsupported",
]);

export const hostFactRejectionSchema = z.strictObject({
  code: z.enum(["HOME_HOST_FACT_REJECTED", "DOCTOR_HOST_FACT_REJECTED", "OPERATIONS_HOST_FACT_REJECTED"]),
  field: hostFactFieldSchema,
  expected: hostFactExpectationSchema,
  observed: hostFactObservedSchema,
});

export type HostFactDiagnosis = Pick<z.infer<typeof hostFactRejectionSchema>, "field" | "expected" | "observed">;

export const homeQueryFailureSchema = z.strictObject({
  status: z.literal("rejected"),
  code: z.string().regex(/^[A-Z][A-Z0-9_]{2,95}$/u),
  diagnosis: z.strictObject({
    field: hostFactFieldSchema,
    expected: hostFactExpectationSchema,
    observed: hostFactObservedSchema,
  }).nullable(),
});

export type HomeQueryFailure = z.infer<typeof homeQueryFailureSchema>;
