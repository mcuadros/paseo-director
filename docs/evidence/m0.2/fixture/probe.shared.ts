import { defineRpc } from "@getpaseo/plugin/server";
import { z } from "zod";

export const lifecyclePing = defineRpc({
  name: "lifecycle.ping",
  input: z.object({ value: z.string() }),
  output: z.object({ value: z.string(), instanceId: z.string() }),
});
