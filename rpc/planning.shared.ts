// SPDX-License-Identifier: Apache-2.0
// Paseo RPC is a strict transport for the engine-owned planning contract.

import { defineRpc } from "@getpaseo/plugin/server";

import {
  planningMutationInputSchema,
  planningMutationResultSchema,
  planningQueryInputSchema,
  planningSnapshotSchema,
  taskDetailQueryInputSchema,
  taskDetailSnapshotSchema,
  homeQueryInputSchema,
  homeSnapshotSchema,
  organizerBootstrapInputSchema,
  organizerBootstrapResultSchema,
} from "../generated/planning-contract.shared.ts";

export const planningQueryRpc = defineRpc({
  name: "director.planning-query",
  input: planningQueryInputSchema,
  output: planningSnapshotSchema,
});

export const homeQueryRpc = defineRpc({
  name: "director.home-query",
  input: homeQueryInputSchema,
  output: homeSnapshotSchema,
});

export const organizerBootstrapRpc = defineRpc({
  name: "director.organizer-bootstrap",
  input: organizerBootstrapInputSchema,
  output: organizerBootstrapResultSchema,
});

export const planningTaskDetailRpc = defineRpc({
  name: "director.planning-task-detail",
  input: taskDetailQueryInputSchema,
  output: taskDetailSnapshotSchema,
});

export const planningMutationRpc = defineRpc({
  name: "director.planning-mutate",
  input: planningMutationInputSchema,
  output: planningMutationResultSchema,
});
