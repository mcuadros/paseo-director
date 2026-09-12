// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  prepareRuntimeInstallation,
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

test("candidate preparation embeds exact Git identity without paths or secrets", () => {
  const root = mkdtempSync(join(tmpdir(), "director-install-metadata-"));
  const checkout = join(root, "checkout");
  const configBase = join(root, "config");
  const cacheBase = join(root, "cache");
  const authority = join(root, "authority");
  for (const path of [join(checkout, "connector"), join(checkout, "release"), configBase, cacheBase, authority]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
  }
  const credential = join(authority, "connector.password");
  writeFileSync(credential, "metadata-test-secret\n", { mode: 0o600 });
  const runtimeDirectory = join(configBase, "director");
  mkdirSync(runtimeDirectory, { mode: 0o700 });
  writeFileSync(join(runtimeDirectory, "runtime.json"), `${JSON.stringify({
    schemaVersion: 1,
    paseo: { credentialFile: credential },
    engine: {},
  })}\n`, { mode: 0o600 });
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
    const environment = { XDG_CONFIG_HOME: configBase, XDG_CACHE_HOME: cacheBase, PATH: process.env.PATH };
    const first = prepareRuntimeInstallation({ repositoryRoot: checkout, environment, home: root });
    assert.equal(first.metadataState, "generated");
    const source = readFileSync(join(checkout, "connector", "install-metadata.server.ts"), "utf8");
    assert.match(source, new RegExp(commit, "u"));
    assert.doesNotMatch(source, /metadata-test-secret|connector\.password|director-install-metadata/u);
    assert.equal(prepareRuntimeInstallation({ repositoryRoot: checkout, environment, home: root }).metadataState, "adopted");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
