// SPDX-License-Identifier: Apache-2.0
// Imported only by tests; installed production code never imports this seam.

import { createBootstrapBuildCore, evaluateSystemExecutableTrust } from "./bootstrap-build-core.mjs";

export function evaluateSystemExecutableTrustForTest(facts) {
  return evaluateSystemExecutableTrust(facts);
}

export function createHermeticBootstrapBuildCore(options) {
  return createBootstrapBuildCore({
    fixedCandidates: options.fixedCandidates,
    spawnSync: options.spawnSync,
    platform: options.platform ?? "linux",
    architecture: options.architecture ?? "x64",
  });
}
