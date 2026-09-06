import type { PluginContext } from "@getpaseo/plugin";
import { MainSurface } from "./main.client";
import { startProbe } from "./probe.server";
import { lifecyclePing } from "./probe.shared";

function assertPluginContext(plugin: PluginContext): void {
  const record = plugin as unknown as Record<string, unknown>;
  if (typeof record.handle !== "function") {
    throw new Error("Missing required public server PluginContext method: handle");
  }
}

export default function contribute(plugin: PluginContext) {
  plugin.addSurface("main", MainSurface);
  assertPluginContext(plugin);
  const lifecycle = startProbe("director-lifecycle-probe");
  plugin.handle(lifecyclePing, ({ value }) => ({ value, instanceId: lifecycle.instanceId }));
  return lifecycle.cleanup;
}
