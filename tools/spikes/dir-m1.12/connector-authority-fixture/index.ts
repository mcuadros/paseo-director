import type { PluginContext } from "@getpaseo/plugin";

import { startConnectorAuthorityProbe } from "./connector.server";

export default function contribute(_plugin: PluginContext) {
  const connector = startConnectorAuthorityProbe();
  return connector.cleanup;
}
