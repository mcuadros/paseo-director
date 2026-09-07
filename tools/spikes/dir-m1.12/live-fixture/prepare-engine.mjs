#!/usr/bin/env node

import { createHash } from "node:crypto";
import {
  copyFileSync,
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";

const ENGINE_SHA = "fb29fe79c1632f8921dff2d681e9e8879e0b782e8bb13d3394ec15aeea032ca1";
const runtimeRoot = process.env.DIRECTOR_M112_RUNTIME_ROOT;
if (!runtimeRoot) throw new Error("DIRECTOR_M112_RUNTIME_ROOT is required");
const source = join(process.cwd(), "engine-source.mjs");
const sourceHash = createHash("sha256").update(readFileSync(source)).digest("hex");
if (sourceHash !== ENGINE_SHA) throw new Error("engine source hash drift");
const artifactRoot = join(runtimeRoot, "artifacts", ENGINE_SHA);
const artifact = join(artifactRoot, "director-engine.mjs");
mkdirSync(artifactRoot, { recursive: true, mode: 0o700 });
copyFileSync(source, artifact);
chmodSync(artifact, 0o700);
const artifactHash = createHash("sha256").update(readFileSync(artifact)).digest("hex");
if (artifactHash !== ENGINE_SHA) throw new Error("prepared engine hash mismatch");
if (existsSync(join(runtimeRoot, "install-script-ran"))) {
  throw new Error("package install script ran unexpectedly");
}
writeFileSync(
  join(runtimeRoot, "prepared.json"),
  `${JSON.stringify({
    engineSha: ENGINE_SHA,
    artifactHash,
    installScriptsIgnored: true,
    downloadedArtifacts: 0,
    vendoredArtifacts: 0,
  })}\n`,
  { mode: 0o600 },
);
