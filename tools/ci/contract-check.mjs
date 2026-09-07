#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import {
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export function generateClient(repositoryRoot, schemaPath) {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-contract-check-"));
  const outputPath = join(temporaryRoot, "generated.ts");
  const goCache = join(temporaryRoot, "go-cache");
  const result = spawnSync(
    "go",
    [
      "-C",
      "engine",
      "run",
      "./cmd/generate-host-client",
      "--schema",
      schemaPath,
      "--output",
      outputPath,
    ],
    {
      cwd: repositoryRoot,
      encoding: "utf8",
      env: { ...process.env, GOCACHE: goCache, GOTOOLCHAIN: "local" },
    },
  );
  if (result.status !== 0) {
    rmSync(temporaryRoot, { recursive: true, force: true });
    throw new Error(
      `host client generation failed: ${result.stderr.trim() || result.error?.message || "unknown error"}`,
    );
  }
  const generated = readFileSync(outputPath);
  rmSync(temporaryRoot, { recursive: true, force: true });
  return generated;
}

export function generatedClientMatches(repositoryRoot, schemaPath) {
  const generated = generateClient(repositoryRoot, schemaPath);
  const committed = readFileSync(
    resolve(repositoryRoot, "shared/generated-host-contract.shared.ts"),
  );
  return generated.equals(committed);
}

function run(repositoryRoot) {
  const schemaPath = resolve(
    repositoryRoot,
    "engine/contract/host-interface.v1.json",
  );
  if (!generatedClientMatches(repositoryRoot, schemaPath)) {
    console.error(
      "Generated host client drifted; run npm run contract:generate and review the result.",
    );
    return 1;
  }
  console.log("Engine-owned host schema and generated TypeScript client match.");
  return 0;
}

const modulePath = realpathSync(fileURLToPath(import.meta.url));
const invokedPath = process.argv[1]
  ? realpathSync(resolve(process.argv[1]))
  : null;
if (invokedPath === modulePath) {
  process.exitCode = run(fileURLToPath(new URL("../../", import.meta.url)));
}
