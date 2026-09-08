// SPDX-License-Identifier: Apache-2.0
// Paseo RPC validates the engine-owned Board snapshot at the client boundary.

import { defineRpc } from "@getpaseo/plugin/server";
import { z } from "zod";

import {
  BOARD_MAXIMUM_TASKS,
  BOARD_SCHEMA_VERSION,
  BOARD_STATES,
} from "../generated/host-contract.shared.ts";

export const boardStates = BOARD_STATES;

const uint64Decimal = z
  .string()
  .regex(/^(?:0|[1-9][0-9]{0,19})$/)
  .refine((value) => value.length < 20 || value <= "18446744073709551615");
const boardTask = z.strictObject({
  id: z.string().min(1).max(128),
  projectId: z.string().min(1).max(128),
  projectName: z.string().min(1).max(512),
  title: z.string().min(1).max(512),
  state: z.enum(BOARD_STATES),
  runNumber: uint64Decimal.nullable(),
  candidateSha: z.string().regex(/^[0-9a-f]{40}$/).nullable(),
});

export const boardSnapshotRpc = defineRpc({
  name: "director.board-snapshot",
  input: z.strictObject({}),
  output: z.strictObject({
    schemaVersion: z.literal(BOARD_SCHEMA_VERSION),
    cursor: uint64Decimal,
    tasks: z.array(boardTask).max(BOARD_MAXIMUM_TASKS).readonly(),
  }),
});

export type BoardSnapshot = z.output<typeof boardSnapshotRpc.output>;
export type BoardTask = BoardSnapshot["tasks"][number];
export type BoardState = BoardTask["state"];
