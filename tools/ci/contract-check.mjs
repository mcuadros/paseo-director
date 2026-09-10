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

function generate(repositoryRoot, schemaPath, command) {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-contract-check-"));
  const outputPath = join(temporaryRoot, "generated.ts");
  const goCache = join(temporaryRoot, "go-cache");
  const result = spawnSync(
    "go",
    [
      "-C",
      "engine",
      "run",
      command,
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

export function generateClient(repositoryRoot, schemaPath) {
  return generate(repositoryRoot, schemaPath, "./cmd/generate-host-client");
}

export function generatePlanningClient(repositoryRoot, schemaPath) {
  return generate(repositoryRoot, schemaPath, "./cmd/generate-planning-client");
}

export function generateAgentMCPClient(repositoryRoot, schemaPath) {
  return generate(repositoryRoot, schemaPath, "./cmd/generate-agent-mcp-client");
}

export function generatedClientMatches(repositoryRoot, schemaPath) {
  const generated = generateClient(repositoryRoot, schemaPath);
  const committed = readFileSync(
    resolve(repositoryRoot, "generated/host-contract.shared.ts"),
  );
  return generated.equals(committed);
}

export function generatedPlanningClientMatches(repositoryRoot, schemaPath) {
  const generated = generatePlanningClient(repositoryRoot, schemaPath);
  const committed = readFileSync(
    resolve(repositoryRoot, "generated/planning-contract.shared.ts"),
  );
  return generated.equals(committed);
}

export function generatedAgentMCPClientMatches(repositoryRoot, schemaPath) {
  const generated = generateAgentMCPClient(repositoryRoot, schemaPath);
  const committed = readFileSync(
    resolve(repositoryRoot, "generated/agent-mcp-contract.shared.ts"),
  );
  return generated.equals(committed);
}

function run(repositoryRoot) {
  const hostSchemaPath = resolve(
    repositoryRoot,
    "engine/ports/host/host-interface.v1.json",
  );
  const planningSchemaPath = resolve(
    repositoryRoot,
    "engine/ports/planning/planning-surface.v1.json",
  );
  const agentMCPSchemaPath = resolve(
    repositoryRoot,
    "engine/domain/agentbridge/schemas/director-agent-mcp.v1.json",
  );
  if (!generatedClientMatches(repositoryRoot, hostSchemaPath)) {
    console.error(
      "Generated host client drifted; run npm run contract:generate and review the result.",
    );
    return 1;
  }
  if (!generatedPlanningClientMatches(repositoryRoot, planningSchemaPath)) {
    console.error(
      "Generated planning client drifted; run npm run contract:generate and review the result.",
    );
    return 1;
  }
  if (!generatedAgentMCPClientMatches(repositoryRoot, agentMCPSchemaPath)) {
    console.error(
      "Generated agent MCP client drifted; run npm run contract:generate and review the result.",
    );
    return 1;
  }
  console.log("Engine-owned host, planning, and agent MCP schemas match their generated TypeScript clients.");
  return 0;
}

const modulePath = realpathSync(fileURLToPath(import.meta.url));
const invokedPath = process.argv[1]
  ? realpathSync(resolve(process.argv[1]))
  : null;
if (invokedPath === modulePath) {
  process.exitCode = run(fileURLToPath(new URL("../../", import.meta.url)));
}
