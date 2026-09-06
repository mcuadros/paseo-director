import type { PluginContext } from "@getpaseo/plugin";
import { startProbe } from "./probe.server";
import { lifecyclePing } from "./probe.shared";

export default function contribute(plugin: PluginContext) {
  const record = plugin as unknown as Record<string, unknown>;
  if (typeof record.definitelyMissing !== "function") {
    throw new Error("Missing required public PluginContext method: definitelyMissing");
  }
  const lifecycle = startProbe("director-lifecycle-incompatible");
  plugin.handle(lifecyclePing, ({ value }) => ({ value, instanceId: lifecycle.instanceId }));
  return lifecycle.cleanup;
}
