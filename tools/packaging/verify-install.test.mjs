// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  prepareCandidateInstallation,
  preparedConnectorMetadataSource,
  verifyInstall,
} from "./verify-install.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const supported = {
  platform: "linux",
  architecture: "x64",
  nodeVersion: "22.0.0",
  repositoryRoot,
};

test("install verification binds exact compatibility to the installed locked closure", () => {
  assert.equal(verifyInstall({ ...supported, paseoVersion: "0.7.2" }).code, "DIRECTOR_INSTALL_READY");
});

test("install verification rejects incompatible hosts with bounded path-free diagnostics", () => {
  assert.deepEqual(verifyInstall({ ...supported, paseoVersion: "0.8.0" }), {
    code: "DIRECTOR_INSTALL_PASEO_UNSUPPORTED",
    message: "Director supports exact Paseo 0.7.2; observed 0.8.0",
  });
  const unsafe = verifyInstall({ ...supported, paseoVersion: "secret/path?credential=value" });
  assert.equal(unsafe.code, "DIRECTOR_INSTALL_PASEO_UNSUPPORTED");
  assert.doesNotMatch(unsafe.message, /secret|path|credential|value/u);
});

test("declared candidate preparation needs no pre-existing runtime configuration", () => {
  const root = mkdtempSync(join(tmpdir(), "director-install-metadata-"));
  const checkout = join(root, "checkout");
  for (const path of [join(checkout, "connector"), join(checkout, "release")]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
  }
  const release = {
    schemaVersion: 2,
    state: "unpublished",
    version: "0.0.0-scaffold",
    target: "linux-amd64",
  };
  writeFileSync(join(checkout, "release", "engine.json"), `${JSON.stringify(release)}\n`);
  writeFileSync(
    join(checkout, "connector", "install-metadata.server.ts"),
    preparedConnectorMetadataSource("0".repeat(40), release),
  );
  const git = (...args) => spawnSync("git", ["-C", checkout, ...args], {
    encoding: "utf8",
    env: { PATH: process.env.PATH, GIT_CONFIG_NOSYSTEM: "1" },
  });
  try {
    assert.equal(git("init", "-b", "stable").status, 0);
    assert.equal(git("add", ".").status, 0);
    assert.equal(git("-c", "user.name=Director Test", "-c", "user.email=director@example.invalid", "commit", "-m", "fixture").status, 0);
    const commit = git("rev-parse", "HEAD").stdout.trim();
    assert.equal(git("status", "--porcelain=v1", "--untracked-files=no").stdout.trim(), "");
    const first = prepareCandidateInstallation({
      repositoryRoot: checkout,
      environment: {
        XDG_CONFIG_HOME: join(root, "missing-config"),
        XDG_CACHE_HOME: join(root, "missing-cache"),
        PATH: process.env.PATH,
      },
      home: join(root, "missing-home"),
    });
    assert.equal(first.metadataState, "generated");
    assert.equal(first.code, "DIRECTOR_INSTALL_CANDIDATE_READY");
    const source = readFileSync(join(checkout, "connector", "install-metadata.server.ts"), "utf8");
    assert.match(source, new RegExp(commit, "u"));
    assert.doesNotMatch(source, /missing-config|missing-cache|missing-home|director-install-metadata/u);
    assert.equal(prepareCandidateInstallation({ repositoryRoot: checkout }).metadataState, "adopted");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
