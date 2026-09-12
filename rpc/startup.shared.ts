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
    activation: z.strictObject({
      lifecycle: z.literal("plugin-reload"),
      result: z.literal("running-current"),
      configurationSchemaVersion: z.literal(1),
      configurationSha256: z.string().regex(/^[0-9a-f]{64}$/u),
      legacyEnvironment: z.enum(["absent", "ignored"]),
      settings: z.array(z.strictObject({
        name: z.enum([
          "paseo.url",
          "engine.mode",
          "engine.url",
          "engine.cache-base",
          "engine.module-cache",
        ]),
        source: z.enum(["defaulted", "overridden"]),
      })).max(5),
    }),
    compatibility: z.strictObject({
      paseoVersion: z.literal("0.7.2"),
      nodeVersion: z.string().regex(/^[0-9]+\.[0-9]+\.[0-9]+$/u),
      platform: z.literal("linux"),
      architecture: z.literal("x64"),
      target: z.literal("linux-amd64"),
    }),
    engine: z.strictObject({
      mode: z.enum(["release", "development"]),
      version: z.string().min(1).max(128),
      sourceCandidate: z.string().regex(/^[0-9a-f]{40}$/u),
      target: z.literal("linux-amd64"),
      binarySha256: z.string().regex(/^[0-9a-f]{64}$/u),
      noticesSha256: z.string().regex(/^[0-9a-f]{64}$/u),
      connectorCommit: z.string().regex(/^[0-9a-f]{40}$/u),
      contractVersion: z.string().min(1).max(64),
      contractSha256: z.string().regex(/^[0-9a-f]{64}$/u),
    }),
    descriptor: descriptorSchema,
  }),
});

export type ConnectorStartupStatus = z.output<
  typeof connectorStartupStatus.output
>;
