import type { PluginContext } from "@getpaseo/plugin";
import { defineRpc } from "@getpaseo/plugin/server";
import { z } from "zod";

import { startLiveConnector } from "./connector.server";

const describeConnector = defineRpc({
  name: "standalone.probe.describe",
  input: z.object({}),
  output: z.object({ hasPaseoApi: z.boolean(), contractHash: z.string() }),
});

export default function contribute(plugin: PluginContext) {
  const connector = startLiveConnector(plugin as unknown as Record<string, unknown>);
  plugin.handle(describeConnector, (_input, { paseo }) => {
    connector.attachPaseo(paseo);
    return connector.describe();
  });
  return connector.cleanup;
}
