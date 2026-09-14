// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  NoticesError,
  auditLicenseInventory,
  goLicenseInventory,
  loadLicensePolicy,
  lockedGoModuleInventory,
  npmLicenseInventory,
  renderThirdPartyNotices,
  unsafeNoticeContent,
  verifyReleaseBinaryModules,
  verifyThirdPartyNotices,
} from "./notices.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const candidate = "1".repeat(40);

function hasCode(code) {
  return (error) => error instanceof NoticesError && error.code === code;
}

test("exact npm, Go module, and Go toolchain licenses form one closed inventory", () => {
  const result = auditLicenseInventory(repositoryRoot);
  assert.deepEqual({
    npmLockEntries: result.summary.npmLockEntries,
    npmCoordinates: result.summary.npmCoordinates,
    npmLinuxCoordinates: result.summary.npmLinuxCoordinates,
    npmInstalledVerifiedCoordinates: result.summary.npmInstalledVerifiedCoordinates,
    npmPlatformExcludedEntries: result.summary.npmPlatformExcludedEntries,
    npmShippedCoordinates: result.summary.npmShippedCoordinates,
    npmBuildOnlyCoordinates: result.summary.npmBuildOnlyCoordinates,
    goModules: result.summary.goModules,
    goToolchains: result.summary.goToolchains,
  }, {
    npmLockEntries: 580,
    npmCoordinates: 546,
    npmLinuxCoordinates: 508,
    npmInstalledVerifiedCoordinates: 508,
    npmPlatformExcludedEntries: 38,
    npmShippedCoordinates: 7,
    npmBuildOnlyCoordinates: 539,
    goModules: 2,
    goToolchains: 1,
  });
  const paseoClient = result.inventory.find((item) => item.ecosystem === "npm" && item.name === "@getpaseo/client");
  assert.equal(paseoClient.version, "0.7.2");
  assert.equal(paseoClient.spdx, "Apache-2.0");
  assert.deepEqual(paseoClient.scope, ["shipped-plugin"]);
  assert.equal(paseoClient.shipped, true);
  assert.equal(paseoClient.licenseEvidence.sha256, "79d5aedce6aa0adc547336dc1bd34c5cc9308ba110fac7079ed97515ee573ad3");
  const paseoCLI = result.fullInventory.find((item) => item.ecosystem === "npm" && item.name === "@getpaseo/cli");
  assert.equal(paseoCLI.version, "0.7.2");
  assert.deepEqual(paseoCLI.scope, ["ci-build-only"]);
  assert.equal(paseoCLI.shipped, false);
  assert.equal(result.inventory.some((item) => item.name === "@getpaseo/cli"), false);
  const mysql = result.inventory.find((item) => item.name === "github.com/go-sql-driver/mysql");
  assert.equal(mysql.spdx, "MPL-2.0");
  assert.deepEqual(mysql.scope, ["release-engine-binary"]);
  const standardLibrary = result.inventory.find((item) => item.ecosystem === "go-toolchain");
  assert.deepEqual(standardLibrary.scope, ["release-bootstrap-binary", "release-engine-binary"]);
});

test("notices generation is deterministic, exact-source bound, complete, and path-free", () => {
  const first = renderThirdPartyNotices(repositoryRoot, candidate);
  const second = renderThirdPartyNotices(repositoryRoot, candidate);
  assert.deepEqual(first, second);
  const text = first.toString("utf8");
  assert.match(text, new RegExp(`^source-candidate: ${candidate}$`, "mu"));
  assert.match(text, /@getpaseo\/client/u);
  assert.doesNotMatch(text, /"name": "@getpaseo\/cli"/u);
  assert.doesNotMatch(text, /claude-agent-sdk/u);
  assert.match(text, /github\.com\/go-sql-driver\/mysql/u);
  assert.match(text, /Mozilla Public License Version 2\.0/u);
  assert.doesNotMatch(text, /\/(?:home|Users)\//u);
  assert.doesNotMatch(text, /\bwks_[A-Za-z0-9]+\b/u);
  const verified = verifyThirdPartyNotices(repositoryRoot, candidate, first);
  assert.equal(verified.noticesBytes, first.length);
  assert.match(verified.noticesSha256, /^[0-9a-f]{64}$/u);

  const mainChannel = renderThirdPartyNotices(repositoryRoot, candidate, { installedScope: "production" });
  const mainVerified = verifyThirdPartyNotices(repositoryRoot, candidate, mainChannel, { installedScope: "production" });
  assert.equal(mainVerified.npmInstalledVerifiedCoordinates, 7);
  assert.match(mainChannel.toString("utf8"), /"npmInstalledScope": "production"/u);

  const duplicate = Buffer.concat([first, Buffer.from("\nduplicate-entry\n")]);
  assert.throws(() => verifyThirdPartyNotices(repositoryRoot, candidate, duplicate), hasCode("NOTICES_STALE"));
  const stale = Buffer.from(text.replace("@getpaseo/client", "@getpaseo/client-stale"));
  assert.throws(() => verifyThirdPartyNotices(repositoryRoot, candidate, stale), hasCode("NOTICES_STALE"));
});

test("notice redaction rejects private paths, secrets, and internal identities", () => {
  for (const value of [
    "/home/operator/private/state.json",
    "/tmp/director-private/state.json",
    "C:\\Users\\operator\\private\\state.json",
    "Authorization: Bearer example-secret-value",
    "password=example-secret-value",
    "wks_private123",
    "paseo:12345678-1234-1234-1234-123456789abc",
  ]) assert.equal(unsafeNoticeContent(value), true, value);
  assert.equal(unsafeNoticeContent("Apache-2.0 attribution with public source URLs"), false);
});

test("npm licensing fails closed on unknown, missing, stale, and duplicate policy", () => {
  const packageJSON = JSON.parse(readFileSync(resolve(repositoryRoot, "package.json"), "utf8"));
  const lock = JSON.parse(readFileSync(resolve(repositoryRoot, "package-lock.json"), "utf8"));
  const policy = loadLicensePolicy(repositoryRoot);

  const incompatible = structuredClone(lock);
  incompatible.packages["node_modules/zod"].license = "GPL-3.0-only";
  assert.throws(() => npmLicenseInventory(repositoryRoot, {
    installedScope: "none", packageJSON, lock: incompatible, policy,
  }), hasCode("NOTICES_LICENSE_INCOMPATIBLE"));

  const missing = structuredClone(policy);
  missing.npm.missingMetadataOverrides = missing.npm.missingMetadataOverrides.filter((entry) => entry.name !== "@getpaseo/client");
  assert.throws(() => npmLicenseInventory(repositoryRoot, {
    installedScope: "none", packageJSON, lock, policy: missing,
  }), hasCode("NOTICES_LICENSE_MISSING"));

  const duplicate = structuredClone(policy);
  duplicate.npm.missingMetadataOverrides.push(structuredClone(duplicate.npm.missingMetadataOverrides[0]));
  assert.throws(() => npmLicenseInventory(repositoryRoot, {
    installedScope: "none", packageJSON, lock, policy: duplicate,
  }), hasCode("NOTICES_POLICY"));

  const invalidSource = structuredClone(policy);
  invalidSource.npm.missingMetadataOverrides[0].source = "https://github.com/getpaseo/paseo/tree/short/packages/client";
  assert.throws(() => npmLicenseInventory(repositoryRoot, {
    installedScope: "none", packageJSON, lock, policy: invalidSource,
  }), hasCode("NOTICES_POLICY"));

  const stale = structuredClone(policy);
  stale.npm.missingMetadataOverrides.push({
    ...structuredClone(stale.npm.missingMetadataOverrides[0]),
    name: "stale-package",
    version: "1.0.0",
  });
  assert.throws(() => npmLicenseInventory(repositoryRoot, {
    installedScope: "none", packageJSON, lock, policy: stale,
  }), hasCode("NOTICES_POLICY_STALE"));

  const invented = structuredClone(policy);
  invented.npm.buildOnlyAllowedSpdx.push("GPL-3.0-only");
  assert.throws(() => npmLicenseInventory(repositoryRoot, {
    installedScope: "none", packageJSON, lock, policy: invented,
  }), hasCode("NOTICES_LICENSE_INCOMPATIBLE"));

  const restrictedShipped = structuredClone(lock);
  restrictedShipped.packages["node_modules/@anthropic-ai/claude-agent-sdk"].dev = false;
  assert.throws(() => npmLicenseInventory(repositoryRoot, {
    installedScope: "none", packageJSON, lock: restrictedShipped, policy,
  }), hasCode("NOTICES_LICENSE_INCOMPATIBLE"));

  const changedReference = structuredClone(policy);
  changedReference.npm.buildOnlyLicenseReferences[0].licenseSha256 = "0".repeat(64);
  assert.throws(() => npmLicenseInventory(repositoryRoot, {
    installedScope: "all", packageJSON, lock, policy: changedReference,
  }), hasCode("NOTICES_POLICY_STALE"));
});

test("Go licensing rejects a changed reviewed license and stale module entry", () => {
  const changed = structuredClone(loadLicensePolicy(repositoryRoot));
  changed.go.modules[0].licenseFiles[0].sha256 = "0".repeat(64);
  assert.throws(() => goLicenseInventory(repositoryRoot, { policy: changed }), hasCode("NOTICES_POLICY_STALE"));

  const stale = structuredClone(loadLicensePolicy(repositoryRoot));
  stale.go.modules.push({
    ...structuredClone(stale.go.modules[0]),
    name: "example.invalid/stale",
    version: "v1.0.0",
  });
  assert.throws(() => goLicenseInventory(repositoryRoot, { policy: stale }), hasCode("NOTICES_POLICY_STALE"));

  const incompatible = structuredClone(loadLicensePolicy(repositoryRoot));
  incompatible.go.modules[0].spdx = "GPL-3.0-only";
  assert.throws(() => goLicenseInventory(repositoryRoot, { policy: incompatible }), hasCode("NOTICES_LICENSE_INCOMPATIBLE"));

  const unsafePath = structuredClone(loadLicensePolicy(repositoryRoot));
  unsafePath.go.modules[0].licenseFiles[0].path = "../private/LICENSE";
  assert.throws(() => goLicenseInventory(repositoryRoot, { policy: unsafePath }), hasCode("NOTICES_POLICY"));

  const missingPath = structuredClone(loadLicensePolicy(repositoryRoot));
  missingPath.go.modules[0].licenseFiles[0].path = "third_party/licenses/missing-module/LICENSE";
  assert.throws(() => goLicenseInventory(repositoryRoot, { policy: missingPath }), hasCode("NOTICES_GO_LICENSE"));
});

test("committed go.mod and go.sum form the exact cache-independent module inventory", () => {
  const metadataResult = spawnSync("go", ["mod", "edit", "-json"], {
    cwd: resolve(repositoryRoot, "engine"),
    encoding: "utf8",
    env: {
      ...process.env,
      GOTOOLCHAIN: "local",
      GOWORK: "off",
      GOPROXY: "off",
      GOSUMDB: "off",
    },
  });
  assert.equal(metadataResult.status, 0, metadataResult.stderr);
  const metadata = JSON.parse(metadataResult.stdout);
  const goSum = readFileSync(resolve(repositoryRoot, "engine/go.sum"), "utf8");
  const policy = loadLicensePolicy(repositoryRoot);
  assert.deepEqual(lockedGoModuleInventory(metadata, goSum, policy).map(({ name, version }) => `${name}@${version}`), [
    "filippo.io/edwards25519@v1.2.0",
    "github.com/go-sql-driver/mysql@v1.10.1",
  ]);

  const replaced = structuredClone(metadata);
  replaced.Replace = [{ Old: { Path: "github.com/go-sql-driver/mysql", Version: "v1.10.1" }, New: { Path: "example.invalid/mysql", Version: "v1.10.1" } }];
  assert.throws(() => lockedGoModuleInventory(replaced, goSum, policy), hasCode("NOTICES_GO_MODULES"));

  const unknown = structuredClone(metadata);
  unknown.Require.push({ Path: "example.invalid/unreviewed", Version: "v1.0.0", Indirect: true });
  assert.throws(() => lockedGoModuleInventory(unknown, goSum, policy), hasCode("NOTICES_LICENSE_INCOMPATIBLE"));

  const missing = goSum.split("\n").filter((line) => !line.startsWith("filippo.io/edwards25519 v1.2.0 ")).join("\n");
  assert.throws(() => lockedGoModuleInventory(metadata, missing, policy), hasCode("NOTICES_GO_MODULES"));

  const duplicate = `${goSum}${goSum.split("\n")[0]}\n`;
  assert.throws(() => lockedGoModuleInventory(metadata, duplicate, policy), hasCode("NOTICES_DUPLICATE"));
});

test("license checks pass with an empty module cache and network disabled", () => {
  const root = mkdtempSync(join(tmpdir(), "director-notices-cold-cache-"));
  try {
    const environment = {
      ...process.env,
      GOCACHE: join(root, "gocache"),
      GOMODCACHE: join(root, "gomodcache"),
      GOTOOLCHAIN: "local",
      GOWORK: "off",
      GOPROXY: "off",
      GOSUMDB: "off",
    };
    delete environment.NODE_TEST_CONTEXT;
    const result = spawnSync("/usr/bin/env", [
      "-u", "NODE_TEST_CONTEXT", process.execPath,
      resolve(repositoryRoot, "tools/release/notices.mjs"),
      "check", "--source", repositoryRoot, "--installed-scope", "all",
    ], {
      cwd: repositoryRoot,
      encoding: "utf8",
      env: environment,
    });
    assert.equal(result.error, undefined, result.error?.message);
    assert.equal(result.status, 0, result.stderr);
    assert.notEqual(result.stdout, "", JSON.stringify({ status: result.status, signal: result.signal, stderr: result.stderr, error: result.error?.message }));
    const summary = JSON.parse(result.stdout);
    assert.deepEqual({ goModules: summary.goModules, goToolchains: summary.goToolchains }, { goModules: 2, goToolchains: 1 });
    assert.equal(existsSync(join(root, "gomodcache", "filippo.io")), false);
    assert.equal(existsSync(join(root, "gomodcache", "github.com")), false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the shipped static binary module graph exactly matches its notices inventory", () => {
  const root = mkdtempSync(join(tmpdir(), "director-notices-binary-"));
  const binary = join(root, "director-engine");
  const bootstrap = join(root, "director-bootstrap");
  try {
    const build = spawnSync("go", ["build", "-trimpath", "-buildvcs=false", "-o", binary, "./cmd/director-engine"], {
      cwd: resolve(repositoryRoot, "engine"),
      encoding: "utf8",
      env: {
        ...process.env,
        CGO_ENABLED: "0",
        GOCACHE: join(root, "go-cache"),
        GOARCH: "amd64",
        GOOS: "linux",
        GOTOOLCHAIN: "local",
        GOWORK: "off",
        GOPROXY: "off",
        GOSUMDB: "off",
      },
    });
    assert.equal(build.status, 0, build.stderr);
    const bootstrapBuild = spawnSync("go", ["build", "-trimpath", "-buildvcs=false", "-o", bootstrap, "./cmd/director-bootstrap"], {
      cwd: resolve(repositoryRoot, "engine"),
      encoding: "utf8",
      env: {
        ...process.env,
        CGO_ENABLED: "0",
        GOCACHE: join(root, "go-cache"),
        GOARCH: "amd64",
        GOOS: "linux",
        GOTOOLCHAIN: "local",
        GOWORK: "off",
        GOPROXY: "off",
        GOSUMDB: "off",
      },
    });
    assert.equal(bootstrapBuild.status, 0, bootstrapBuild.stderr);
    assert.deepEqual(verifyReleaseBinaryModules(repositoryRoot, binary, { kind: "engine" }), { kind: "engine", moduleCount: 2, toolchain: "go1.26.5" });
    assert.deepEqual(verifyReleaseBinaryModules(repositoryRoot, bootstrap, { kind: "bootstrap" }), { kind: "bootstrap", moduleCount: 0, toolchain: "go1.26.5" });
    assert.throws(() => verifyReleaseBinaryModules(repositoryRoot, bootstrap, { kind: "engine" }), hasCode("NOTICES_BINARY"));
    writeFileSync(binary, "not a Go binary");
    assert.throws(() => verifyReleaseBinaryModules(repositoryRoot, binary, { kind: "engine" }), hasCode("NOTICES_BINARY"));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
