#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Production facade: runtime inputs cannot select or replace the Go toolchain.

import { spawnSync } from "node:child_process";

import {
  BootstrapBuildError,
  cachePublishedBootstrap,
  createBootstrapBuildCore,
  runBootstrapPreparation,
} from "./bootstrap-build-core.mjs";

export const PRODUCTION_GO_TOOLCHAIN_CANDIDATES = Object.freeze([
  "/usr/local/go/bin/go",
  "/usr/bin/go",
]);

const productionBootstrapBuildCore = createBootstrapBuildCore({
  fixedCandidates: PRODUCTION_GO_TOOLCHAIN_CANDIDATES,
  spawnSync,
  platform: process.platform,
  architecture: process.arch,
});

export { BootstrapBuildError, cachePublishedBootstrap, runBootstrapPreparation };

export function resolveBootstrapGoToolchain() {
  return productionBootstrapBuildCore.resolveBootstrapGoToolchain();
}

export function buildMainBootstrap({ repositoryRoot, sourceCandidate, environment }) {
  return productionBootstrapBuildCore.buildMainBootstrap({ repositoryRoot, sourceCandidate, environment });
}
