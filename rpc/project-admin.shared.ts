// SPDX-License-Identifier: Apache-2.0

import { defineRpc } from "@getpaseo/plugin/server";
import { z } from "zod";

export const PROJECT_ADMIN_RECONNECT_INSTRUCTION =
  "Open ‘Create Director administration session’ from this agent. Paseo 0.7.2 cannot add MCP servers to the current live session, so Director creates a new session in the same Project; re-send your request there. Do not restart Paseo." as const;

const identity = z.string().regex(/^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$/u);

export const recreateProjectAdminSessionRpc = defineRpc({
  name: "director.recreate-project-admin-session",
  input: z.strictObject({
    requestId: identity,
    sourceWorkspaceId: identity,
    sourceAgentId: identity,
  }),
  output: z.strictObject({
    status: z.literal("created"),
    agentId: identity,
    instruction: z.literal(PROJECT_ADMIN_RECONNECT_INSTRUCTION),
  }),
});
