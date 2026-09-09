// SPDX-License-Identifier: Apache-2.0
// Paseo plugin RPC contracts transport engine state without reducing it.

import { defineRpc } from "@getpaseo/plugin/server";
import { z } from "zod";

import {
  HOST_CAPABILITIES,
  HOST_CONTRACT_SHA256,
  HOST_CONTRACT_VERSION,
  HOST_CREDENTIAL_SCOPE,
} from "../generated/host-contract.shared.ts";

const descriptorSchema = z.strictObject({
  credentialScope: z.literal(HOST_CREDENTIAL_SCOPE),
  contractVersion: z.literal(HOST_CONTRACT_VERSION),
  contractHash: z.literal(HOST_CONTRACT_SHA256),
  capabilities: z.tuple(
    HOST_CAPABILITIES.map((capability) => z.literal(capability)) as [
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[0]>,
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[1]>,
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[2]>,
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[3]>,
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[4]>,
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[5]>,
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[6]>,
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[7]>,
      z.ZodLiteral<(typeof HOST_CAPABILITIES)[8]>,
    ],
  ),
});

export const connectorStartupStatus = defineRpc({
  name: "director.startup-status",
  input: z.strictObject({}),
  output: z.strictObject({
    state: z.literal("board-ready"),
    engineMode: z.enum(["release", "development"]),
    productBehavior: z.literal(true),
    descriptor: descriptorSchema,
  }),
});

export type ConnectorStartupStatus = z.output<
  typeof connectorStartupStatus.output
>;
