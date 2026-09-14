// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { join } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";

import { prepareCandidateInstallation, preparedConnectorMetadataSource, verifyInstall } from "./verify-install.mjs";

test("install verification remains exact Paseo 0.7.2 and path-safe", () => {
  assert.equal(verifyInstall({ platform: "linux", architecture: "x64", nodeVersion: "22.0.0", paseoVersion: "0.7.2", repositoryRoot: process.cwd() }).code, "DIRECTOR_INSTALL_READY");
  const unsafe = verifyInstall({ platform: "linux", architecture: "x64", nodeVersion: "22.0.0", paseoVersion: "secret/path?token=value", repositoryRoot: process.cwd() });
  assert.equal(unsafe.code, "DIRECTOR_INSTALL_PASEO_UNSUPPORTED"); assert.doesNotMatch(unsafe.message, /secret|path|token|value/u);
});

test("musl fails with an exact host diagnostic before dependency or license audit", () => {
  let licenseAuditCalled = false;
  const result = verifyInstall({
    platform: "linux",
    architecture: "x64",
    libc: "musl",
    nodeVersion: "22.0.0",
    paseoVersion: "0.7.2",
    licenseAudit() { licenseAuditCalled = true; },
  });
  assert.deepEqual(result, {
    code: "DIRECTOR_INSTALL_LIBC_UNSUPPORTED",
    message: "Director 1.0 supports glibc on Linux amd64 only",
  });
  assert.equal(licenseAuditCalled, false);
});

function fixture(state) {
  const root = mkdtempSync(join(tmpdir(), "director-install-bootstrap-"));
  const checkout = join(root, "checkout");
  mkdirSync(join(checkout, "release"), { recursive: true, mode: 0o700 }); mkdirSync(join(checkout, "connector"), { mode: 0o700 });
  const candidate = "e".repeat(40);
  const release = state === "unpublished" ? { schemaVersion: 2, state, version: "0.0.0-scaffold", target: "linux-amd64" }
    : { schemaVersion: 2, state, version: "1.0.0-alpha.1", target: "linux-amd64", sourceCandidate: candidate };
  writeFileSync(join(checkout, "release", "engine.json"), `${JSON.stringify(release)}\n`);
  writeFileSync(join(checkout, "connector", "install-metadata.server.ts"), preparedConnectorMetadataSource("0".repeat(40), "main", { schemaVersion: 1, target: "linux-amd64", path: "/private/bootstrap", sha256: "0".repeat(64), size: 1 }));
  if (state === "published") writeFileSync(join(checkout, "release", "bootstrap-linux-amd64.json"), `${JSON.stringify({ schemaVersion: 1, state, version: release.version, target: "linux-amd64", sourceCandidate: candidate, binary: { name: "director-bootstrap-linux-amd64", sha256: "1".repeat(64), size: 42 } })}\n`);
  const git = (...args) => spawnSync("git", ["-C", checkout, ...args], { encoding: "utf8", env: { PATH: process.env.PATH, GIT_CONFIG_NOSYSTEM: "1" } });
  if (state === "unpublished") {
    assert.equal(git("init", "-b", "main").status, 0); assert.equal(git("add", ".").status, 0);
    assert.equal(git("-c", "user.name=Director Test", "-c", "user.email=director@example.invalid", "commit", "-m", "fixture").status, 0);
  }
  return { root, checkout, candidate: state === "unpublished" ? git("rev-parse", "HEAD").stdout.trim() : candidate };
}

for (const channel of ["unpublished", "published"]) test(`${channel} preparation delegates channel effects to Go and writes metadata only after success`, () => {
  const value = fixture(channel);
  const bootstrap = { schemaVersion: 1, target: "linux-amd64", path: "/private/bootstrap", sha256: "2".repeat(64), size: 42 };
  let mainBuilds = 0; let releaseCopies = 0; let goPrepares = 0;
  try {
    const result = prepareCandidateInstallation({ repositoryRoot: value.checkout, environment: { HOME: value.root, XDG_CACHE_HOME: join(value.root, "cache") },
      buildMainBootstrap() { mainBuilds += 1; return { metadata: bootstrap, compilerInvocations: 1, cacheState: "published" }; },
      cachePublishedBootstrap() { releaseCopies += 1; return { metadata: bootstrap, compilerInvocations: 0, cacheState: "published" }; },
      runBootstrapPreparation({ sourceCandidate }) { goPrepares += 1; return { schemaVersion: 1, code: "DIRECTOR_BOOTSTRAP_PREPARED", channel: channel === "unpublished" ? "main" : "release", sourceCandidate, bootstrap: { sha256: bootstrap.sha256 }, engineBuilds: channel === "unpublished" ? 1 : 0 }; } });
    assert.equal(result.channel, channel === "unpublished" ? "main" : "release");
    assert.equal(mainBuilds, channel === "unpublished" ? 1 : 0); assert.equal(releaseCopies, channel === "published" ? 1 : 0); assert.equal(goPrepares, 1);
    assert.match(readFileSync(join(value.checkout, "connector", "install-metadata.server.ts"), "utf8"), new RegExp(value.candidate));
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("failed Go preparation preserves the prior installed metadata", () => {
  const value = fixture("unpublished");
  const target = join(value.checkout, "connector", "install-metadata.server.ts"); const before = readFileSync(target, "utf8");
  try {
    assert.throws(() => prepareCandidateInstallation({ repositoryRoot: value.checkout,
      buildMainBootstrap() { return { metadata: { schemaVersion: 1, target: "linux-amd64", path: "/private/bootstrap", sha256: "3".repeat(64), size: 42 }, compilerInvocations: 1, cacheState: "published" }; },
      runBootstrapPreparation() { throw new Error("DIRECTOR_MAIN_BUILD_FAILED"); } }), /DIRECTOR_MAIN_BUILD_FAILED/u);
    assert.equal(readFileSync(target, "utf8"), before);
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("install verification rejects an unapproved production license without exposing source paths", () => {
  const result = verifyInstall({
    platform: "linux",
    architecture: "x64",
    nodeVersion: "22.0.0",
    paseoVersion: "0.7.2",
    repositoryRoot: process.cwd(),
    licenseAudit() { throw new Error("/home/operator/private/license-token"); },
  });
  assert.deepEqual(result, {
    code: "DIRECTOR_INSTALL_LICENSE_AUDIT",
    message: "locked production license metadata is incomplete or unapproved",
  });
  assert.doesNotMatch(JSON.stringify(result), /home|operator|private|token/u);
});
