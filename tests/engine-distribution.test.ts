// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  EMPTY_SHA256,
  EngineDistributionError,
  parseReleaseMetadata,
  resolveEngine,
  type EngineBinaryIdentity,
} from "../connector/engine-distribution.server.ts";
import {
  EngineSelectionError,
  selectEngine,
  type DevelopmentEngineSelection,
  type ReleaseEngineSelection,
} from "../connector/engine-selection.server.ts";

const SOURCE = "1".repeat(40);
const CONNECTOR = "2".repeat(40);
const CONTRACT = "e550b83612c34acbb1a3086066451daca70e230276d189b8bd4074746597264e";

function digest(bytes: Uint8Array | string): string {
  return createHash("sha256").update(bytes).digest("hex");
}

function releaseFixture() {
  const root = mkdtempSync(join(tmpdir(), "director-release-test-"));
  const checkoutRoot = join(root, "checkout");
  const cacheRoot = join(root, "cache");
  const metadataPath = join(checkoutRoot, "release.json");
  const binary = Buffer.from("fake-director-engine");
  const notices = Buffer.from(`source-candidate: ${SOURCE}\n`);
  const metadata = {
    schemaVersion: 2,
    state: "published",
    version: "1.2.3",
    target: "linux-amd64",
    sourceCandidate: SOURCE,
    binary: {
      name: "director-engine-linux-amd64",
      url: "https://github.com/mcuadros/paseo-director/releases/download/v1.2.3/director-engine-linux-amd64",
      sha256: digest(binary),
    },
    notices: {
      name: "THIRD_PARTY_NOTICES.txt",
      url: "https://github.com/mcuadros/paseo-director/releases/download/v1.2.3/THIRD_PARTY_NOTICES.txt",
      sha256: digest(notices),
    },
  } as const;
  mkdirSync(checkoutRoot, { mode: 0o700 });
  writeFileSync(metadataPath, `${JSON.stringify(metadata)}\n`);
  const selection: ReleaseEngineSelection = { mode: "release", checkoutRoot, cacheRoot, metadataPath };
  const identity = (overrides: Partial<EngineBinaryIdentity> = {}): EngineBinaryIdentity => ({
    name: "director-engine",
    version: metadata.version,
    buildMode: "release",
    sourceCandidate: metadata.sourceCandidate,
    target: "linux-amd64",
    executableSha256: metadata.binary.sha256,
    noticesSha256: metadata.notices.sha256,
    contractVersion: "director-host/v1",
    contractSha256: CONTRACT,
    productBehavior: true,
    ...overrides,
  });
  return { root, selection, binary, notices, metadata, identity };
}

function releaseDependencies(fixture: ReturnType<typeof releaseFixture>) {
  let fetches = 0;
  return {
    dependencies: {
      async fetchAsset(url: string) {
        fetches += 1;
        return url.endsWith("THIRD_PARTY_NOTICES.txt") ? fixture.notices : fixture.binary;
      },
      connectorCommit() { return CONNECTOR; },
      inspectBinary() { return fixture.identity(); },
    },
    fetches: () => fetches,
  };
}

test("release mode verifies identity and atomically caches the complete installed pin", async () => {
  const fixture = releaseFixture();
  const probe = releaseDependencies(fixture);
  let compiles = 0;
  try {
    const first = await resolveEngine(fixture.selection, {
      ...probe.dependencies,
      compile() { compiles += 1; },
    });
    assert.deepEqual({
      mode: first.mode,
      version: first.version,
      sourceCandidate: first.sourceCandidate,
      target: first.target,
      binarySha256: first.binarySha256,
      noticesSha256: first.noticesSha256,
      connectorCommit: first.connectorCommit,
      contractVersion: first.contractVersion,
      contractSha256: first.contractSha256,
    }, {
      mode: "release",
      version: "1.2.3",
      sourceCandidate: SOURCE,
      target: "linux-amd64",
      binarySha256: fixture.metadata.binary.sha256,
      noticesSha256: fixture.metadata.notices.sha256,
      connectorCommit: CONNECTOR,
      contractVersion: "director-host/v1",
      contractSha256: CONTRACT,
    });
    assert.deepEqual(readFileSync(first.binaryPath), fixture.binary);
    assert.deepEqual(readFileSync(first.noticesPath), fixture.notices);
    assert.equal(first.binaryPath.startsWith(fixture.selection.cacheRoot), true);
    assert.equal(first.binaryPath.startsWith(fixture.selection.checkoutRoot), false);
    assert.equal(readdirSync(dirname(first.binaryPath)).some((name) => name.startsWith(".partial-")), false);

    await resolveEngine(fixture.selection, probe.dependencies);
    assert.equal(probe.fetches(), 2, "verified cache must be adopted without another download");
    assert.equal(compiles, 0);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("unpublished metadata fails before fetch or compilation", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-unpublished-test-"));
  const metadataPath = join(root, "engine.json");
  let fetches = 0;
  let compiles = 0;
  try {
    writeFileSync(metadataPath, JSON.stringify({ schemaVersion: 2, state: "unpublished", version: "0.0.0-scaffold", target: "linux-amd64" }));
    await assert.rejects(resolveEngine({ mode: "release", checkoutRoot: root, cacheRoot: join(root, "cache"), metadataPath }, {
      async fetchAsset() { fetches += 1; return new Uint8Array(); },
      compile() { compiles += 1; },
    }), (error: unknown) => error instanceof EngineDistributionError && error.code === "ENGINE_RELEASE_UNPUBLISHED");
    assert.equal(fetches, 0);
    assert.equal(compiles, 0);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("published metadata closes source, target, asset name, URL, digest, and field drift", () => {
  const fixture = releaseFixture();
  const valid = fixture.metadata;
  try {
    for (const metadata of [
      { ...valid, sourceCandidate: "1".repeat(39) },
      { ...valid, target: "linux-arm64" },
      { ...valid, version: "latest" },
      { ...valid, extra: true },
      { ...valid, binary: { ...valid.binary, name: "director-engine" } },
      { ...valid, notices: { ...valid.notices, sha256: EMPTY_SHA256 } },
      { ...valid, binary: { ...valid.binary, url: valid.binary.url.replace("v1.2.3", "v1.2.4") } },
      { ...valid, binary: { ...valid.binary, url: `${valid.binary.url}?mirror=attacker` } },
      { ...valid, binary: { ...valid.binary, url: "https://example.invalid/director-engine" } },
    ]) {
      assert.throws(() => parseReleaseMetadata(Buffer.from(JSON.stringify(metadata))), (error: unknown) =>
        error instanceof EngineDistributionError && error.code === "ENGINE_RELEASE_METADATA");
    }
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("binary or notices failure leaves no final cache and a clean retry succeeds", async () => {
  for (const failingAsset of ["binary", "notices"] as const) {
    const fixture = releaseFixture();
    const finalRoot = join(fixture.selection.cacheRoot, "release", "1.2.3", "linux-amd64", fixture.metadata.binary.sha256);
    try {
      await assert.rejects(resolveEngine(fixture.selection, {
        connectorCommit() { return CONNECTOR; },
        inspectBinary() { return fixture.identity(); },
        async fetchAsset(url) {
          if ((failingAsset === "binary") === !url.endsWith("THIRD_PARTY_NOTICES.txt")) return Buffer.from("corrupt");
          return url.endsWith("THIRD_PARTY_NOTICES.txt") ? fixture.notices : fixture.binary;
        },
      }), (error: unknown) => error instanceof EngineDistributionError && error.code === "ENGINE_RELEASE_DIGEST");
      assert.equal(existsSync(finalRoot), false);
      const retry = releaseDependencies(fixture);
      await resolveEngine(fixture.selection, retry.dependencies);
      assert.equal(existsSync(join(finalRoot, "director-engine")), true);
    } finally {
      rmSync(fixture.root, { recursive: true, force: true });
    }
  }
});

test("identity mismatch fails closed without publishing or compiling", async () => {
  const fixture = releaseFixture();
  let compiles = 0;
  try {
    await assert.rejects(resolveEngine(fixture.selection, {
      ...releaseDependencies(fixture).dependencies,
      inspectBinary() { return fixture.identity({ sourceCandidate: "9".repeat(40) }); },
      compile() { compiles += 1; },
    }), (error: unknown) => error instanceof EngineDistributionError && error.code === "ENGINE_IDENTITY_MISMATCH");
    assert.equal(compiles, 0);
    assert.equal(existsSync(join(fixture.selection.cacheRoot, "release", "1.2.3", "linux-amd64", fixture.metadata.binary.sha256)), false);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("notices source identity and bounded asset size fail before cache publication", async () => {
  for (const kind of ["notices", "size"] as const) {
    const fixture = releaseFixture();
    let notices = fixture.notices;
    let noticesSha256 = fixture.metadata.notices.sha256;
    let inspections = 0;
    try {
      if (kind === "notices") {
        notices = Buffer.from("source-candidate: wrong\n");
        noticesSha256 = digest(notices);
        writeFileSync(fixture.selection.metadataPath, JSON.stringify({
          ...fixture.metadata,
          notices: { ...fixture.metadata.notices, sha256: noticesSha256 },
        }));
      }
      await assert.rejects(resolveEngine(fixture.selection, {
        connectorCommit() { return CONNECTOR; },
        inspectBinary() { inspections += 1; return fixture.identity({ noticesSha256 }); },
        async fetchAsset(url) {
          if (kind === "size" && url.endsWith("THIRD_PARTY_NOTICES.txt")) {
            return Buffer.alloc(16 * 1024 * 1024 + 1);
          }
          return url.endsWith("THIRD_PARTY_NOTICES.txt") ? notices : fixture.binary;
        },
      }), (error: unknown) => error instanceof EngineDistributionError &&
        error.code === (kind === "notices" ? "ENGINE_NOTICES_IDENTITY" : "ENGINE_RELEASE_SIZE"));
      assert.equal(inspections, 0);
    } finally {
      rmSync(fixture.root, { recursive: true, force: true });
    }
  }
});

test("lost response and concurrent replay adopt the exact complete cache", async () => {
  const fixture = releaseFixture();
  const probe = releaseDependencies(fixture);
  try {
    await assert.rejects(resolveEngine(fixture.selection, {
      ...probe.dependencies,
      afterPublish() { throw new Error("response lost"); },
    }), (error: unknown) => error instanceof EngineDistributionError && error.code === "ENGINE_DISTRIBUTION_IO" && !error.message.includes("response"));
    const recovered = await resolveEngine(fixture.selection, probe.dependencies);
    assert.equal(recovered.binarySha256, fixture.metadata.binary.sha256);
    assert.equal(probe.fetches(), 2);

    rmSync(dirname(recovered.binaryPath), { recursive: true });
    const concurrent = releaseDependencies(fixture);
    const [left, right] = await Promise.all([
      resolveEngine(fixture.selection, concurrent.dependencies),
      resolveEngine(fixture.selection, concurrent.dependencies),
    ]);
    assert.equal(left.binaryPath, right.binaryPath);
    assert.equal(readFileSync(left.noticesPath, "utf8"), fixture.notices.toString("utf8"));
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("compatible release upgrade is additive and a later failure preserves both known-good caches", async () => {
  const fixture = releaseFixture();
  try {
    const first = await resolveEngine(fixture.selection, releaseDependencies(fixture).dependencies);
    const upgradedBinary = Buffer.from("fake-director-engine-upgraded");
    const upgradedNotices = Buffer.from(`source-candidate: ${SOURCE}\ncompatible upgrade\n`);
    const upgraded = {
      ...fixture.metadata,
      version: "1.2.4",
      binary: {
        ...fixture.metadata.binary,
        url: "https://github.com/mcuadros/paseo-director/releases/download/v1.2.4/director-engine-linux-amd64",
        sha256: digest(upgradedBinary),
      },
      notices: {
        ...fixture.metadata.notices,
        url: "https://github.com/mcuadros/paseo-director/releases/download/v1.2.4/THIRD_PARTY_NOTICES.txt",
        sha256: digest(upgradedNotices),
      },
    };
    writeFileSync(fixture.selection.metadataPath, JSON.stringify(upgraded));
    const second = await resolveEngine(fixture.selection, {
      connectorCommit() { return CONNECTOR; },
      inspectBinary() { return fixture.identity({ version: "1.2.4", executableSha256: upgraded.binary.sha256, noticesSha256: upgraded.notices.sha256 }); },
      async fetchAsset(url) { return url.endsWith("THIRD_PARTY_NOTICES.txt") ? upgradedNotices : upgradedBinary; },
    });
    assert.notEqual(first.binaryPath, second.binaryPath);
    assert.equal(existsSync(first.binaryPath), true);
    assert.equal(existsSync(second.binaryPath), true);

    const failed = {
      ...upgraded,
      version: "1.2.5",
      binary: { ...upgraded.binary, url: upgraded.binary.url.replace("v1.2.4", "v1.2.5") },
      notices: { ...upgraded.notices, url: upgraded.notices.url.replace("v1.2.4", "v1.2.5") },
    };
    writeFileSync(fixture.selection.metadataPath, JSON.stringify(failed));
    await assert.rejects(resolveEngine(fixture.selection, {
      connectorCommit() { return CONNECTOR; },
      inspectBinary() { return fixture.identity({ version: "1.2.5", executableSha256: failed.binary.sha256, noticesSha256: failed.notices.sha256 }); },
      async fetchAsset() { return Buffer.from("corrupt"); },
    }), (error: unknown) => error instanceof EngineDistributionError && error.code === "ENGINE_RELEASE_DIGEST");
    assert.equal(existsSync(first.binaryPath), true);
    assert.equal(existsSync(second.binaryPath), true);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("cache poisoning, symlinks, and unsafe permissions fail without replacement", async () => {
  for (const poison of ["symlink", "digest", "permissions"] as const) {
    const fixture = releaseFixture();
    const finalRoot = join(fixture.selection.cacheRoot, "release", "1.2.3", "linux-amd64", fixture.metadata.binary.sha256);
    let fetches = 0;
    try {
      mkdirSync(finalRoot, { recursive: true, mode: 0o700 });
      writeFileSync(join(finalRoot, "director-engine"), poison === "digest" ? "wrong" : fixture.binary, { mode: 0o500 });
      writeFileSync(join(finalRoot, "THIRD_PARTY_NOTICES.txt"), fixture.notices, { mode: 0o400 });
      if (poison === "symlink") {
        rmSync(join(finalRoot, "director-engine"));
        symlinkSync("THIRD_PARTY_NOTICES.txt", join(finalRoot, "director-engine"));
      }
      if (poison === "permissions") chmodSync(finalRoot, 0o777);
      await assert.rejects(resolveEngine(fixture.selection, {
        connectorCommit() { return CONNECTOR; },
        inspectBinary() { return fixture.identity(); },
        async fetchAsset() { fetches += 1; return new Uint8Array(); },
      }), (error: unknown) => error instanceof EngineDistributionError &&
        ["ENGINE_CACHE_POISONED", "ENGINE_CACHE_PERMISSIONS"].includes(error.code));
      assert.equal(fetches, 0);
      assert.equal(existsSync(finalRoot), true, "untrusted cache content must not be deleted");
    } finally {
      rmSync(fixture.root, { recursive: true, force: true });
    }
  }
});

test("restart removes only an exact stale owned partial", async () => {
  const fixture = releaseFixture();
  const base = join(fixture.selection.cacheRoot, "release", "1.2.3", "linux-amd64");
  const partial = join(base, `.partial-${fixture.metadata.binary.sha256}-stale`);
  try {
    mkdirSync(partial, { recursive: true, mode: 0o700 });
    const binding = digest(JSON.stringify(fixture.metadata));
    writeFileSync(join(partial, ".owner.json"), `${JSON.stringify({ schemaVersion: 1, binding, pid: 2_000_000_000, processStart: "1" })}\n`, { mode: 0o600 });
    await resolveEngine(fixture.selection, releaseDependencies(fixture).dependencies);
    assert.equal(existsSync(partial), false);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("development mode builds one exact source Candidate and never downloads", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-development-test-"));
  const selection: DevelopmentEngineSelection = {
    mode: "development",
    checkoutRoot: join(root, "checkout"),
    sourceRoot: join(root, "source"),
    cacheRoot: join(root, "cache"),
    moduleCache: join(root, "module-cache"),
  };
  for (const path of [selection.checkoutRoot, selection.sourceRoot, selection.moduleCache]) mkdirSync(path, { recursive: true, mode: 0o700 });
  let compiles = 0;
  let downloads = 0;
  const inspectBinary = (): EngineBinaryIdentity => ({
    name: "director-engine", version: "0.0.0-dev", buildMode: "development",
    sourceCandidate: SOURCE, target: "linux-amd64", executableSha256: digest("compiled"), noticesSha256: EMPTY_SHA256,
    contractVersion: "director-host/v1", contractSha256: CONTRACT, productBehavior: true,
  });
  try {
    const dependencies = {
      sourceCandidate() { return SOURCE; }, connectorCommit() { return CONNECTOR; }, inspectBinary,
      compile(_selection: DevelopmentEngineSelection, destination: string) {
        compiles += 1; mkdirSync(dirname(destination), { recursive: true }); writeFileSync(destination, "compiled", { mode: 0o500 });
      },
      async fetchAsset() { downloads += 1; return new Uint8Array(); },
    };
    const first = await resolveEngine(selection, dependencies);
    const replay = await resolveEngine(selection, dependencies);
    assert.equal(first.binaryPath, replay.binaryPath);
    assert.equal(first.sourceCandidate, SOURCE);
    assert.equal(first.noticesSha256, EMPTY_SHA256);
    assert.equal(compiles, 1);
    assert.equal(downloads, 0);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("failed development replacement preserves the prior Candidate cache", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-development-preserve-"));
  const selection: DevelopmentEngineSelection = {
    mode: "development", checkoutRoot: join(root, "checkout"), sourceRoot: join(root, "source"),
    cacheRoot: join(root, "cache"), moduleCache: join(root, "module-cache"),
  };
  for (const path of [selection.checkoutRoot, selection.sourceRoot, selection.moduleCache]) mkdirSync(path, { recursive: true, mode: 0o700 });
  let candidate = SOURCE;
  const identity = (): EngineBinaryIdentity => ({
    name: "director-engine", version: "0.0.0-dev", buildMode: "development", sourceCandidate: candidate,
    target: "linux-amd64", executableSha256: digest("good"), noticesSha256: EMPTY_SHA256, contractVersion: "director-host/v1",
    contractSha256: CONTRACT, productBehavior: true,
  });
  try {
    const first = await resolveEngine(selection, {
      sourceCandidate() { return candidate; }, connectorCommit() { return CONNECTOR; }, inspectBinary: identity,
      compile(_selection, destination) { mkdirSync(dirname(destination), { recursive: true }); writeFileSync(destination, "good", { mode: 0o500 }); },
    });
    candidate = "8".repeat(40);
    await assert.rejects(resolveEngine(selection, {
      sourceCandidate() { return candidate; }, connectorCommit() { return CONNECTOR; }, inspectBinary: identity,
      compile() { throw new EngineDistributionError("ENGINE_DEVELOPMENT_BUILD", "build failed"); },
    }), (error: unknown) => error instanceof EngineDistributionError && error.code === "ENGINE_DEVELOPMENT_BUILD");
    assert.equal(readFileSync(first.binaryPath, "utf8"), "good");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("engine mode and canonical paths are explicit and disjoint", () => {
  const root = mkdtempSync(join(tmpdir(), "director-mode-test-"));
  const checkoutRoot = join(root, "checkout");
  mkdirSync(checkoutRoot);
  try {
    assert.throws(() => selectEngine({ XDG_CACHE_HOME: join(root, "cache") }, checkoutRoot), (error: unknown) =>
      error instanceof EngineSelectionError && error.code === "ENGINE_MODE_REQUIRED");
    assert.throws(() => selectEngine({ DIRECTOR_ENGINE_MODE: "release", DIRECTOR_ENGINE_SOURCE_ROOT: join(root, "source"), XDG_CACHE_HOME: join(root, "cache") }, checkoutRoot), (error: unknown) =>
      error instanceof EngineSelectionError && error.code === "ENGINE_MODE_CONFLICT");
    assert.equal(selectEngine({ DIRECTOR_ENGINE_MODE: "development", DIRECTOR_ENGINE_SOURCE_ROOT: join(root, "source"), XDG_CACHE_HOME: join(root, "cache") }, checkoutRoot).mode, "development");
    assert.throws(() => selectEngine({ DIRECTOR_ENGINE_MODE: "development", DIRECTOR_ENGINE_SOURCE_ROOT: join(root, "source"), XDG_CACHE_HOME: join(root, "cache"), GOMODCACHE: join(checkoutRoot, "module-cache") }, checkoutRoot), (error: unknown) =>
      error instanceof EngineSelectionError && error.code === "ENGINE_MODULE_CACHE_IN_CHECKOUT");
    const cacheLink = join(root, "cache-link");
    symlinkSync(checkoutRoot, cacheLink);
    assert.throws(() => selectEngine({ DIRECTOR_ENGINE_MODE: "release", XDG_CACHE_HOME: cacheLink }, checkoutRoot), (error: unknown) =>
      error instanceof EngineSelectionError && error.code === "ENGINE_CACHE_IN_CHECKOUT");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
