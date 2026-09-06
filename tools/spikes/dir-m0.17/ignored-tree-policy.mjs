#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import {
  chmodSync,
  closeSync,
  existsSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  readSync,
  readdirSync,
  realpathSync,
  renameSync,
  rmSync,
  statfsSync,
  statSync,
  truncateSync,
  utimesSync,
  writeFileSync,
  writeSync,
} from "node:fs";
import { arch, platform, release, tmpdir } from "node:os";
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";

const MIB = 1024 * 1024;
const DAY_MS = 24 * 60 * 60 * 1000;
const PROVISIONAL_BASE = "af489d04d4f84bd60d39e575718884e0b645ee36";
const FIXED_OVERHEAD_BYTES = 64 * 1024;
const ENTRY_OVERHEAD_BYTES = 4096;
const GATE_PASSES = 5;
const WORKER_TIMEOUT_MS = 540_000;
const TIMEOUT_FIXTURE_MS = 3_000;
const PRIVATE_SENTINEL_LEAF = "private-token.txt";
const PRIVATE_SENTINEL_VALUE = "disposable-director-secret-value";

const RELEASE_POLICY = Object.freeze({
  maxBytes: 512 * MIB,
  maxFileBytes: 256 * MIB,
  maxEntries: 10_000,
  maxScanEntries: 25_000,
  streamChunkBytes: 64 * 1024,
  maxPhaseWallMs: 180_000,
  maxLifecycleWallMs: 480_000,
  maxRssIncreaseBytes: 192 * MIB,
  retentionMs: 7 * DAY_MS,
  minimumFreePercent: 10,
});

const EXPECTED_WORKER_ASSERTIONS = [
  "representative_roots_are_git_ignored",
  "composite_tree_hits_exact_entry_byte_and_file_defaults",
  "entry_limit_plus_one_refused",
  "aggregate_byte_limit_plus_one_refused_before_read",
  "file_byte_limit_plus_one_refused_before_read",
  "scan_limit_plus_one_refused",
  "policy_expansion_requires_new_evidence",
  "disk_floor_exact_boundary_accepted",
  "disk_floor_one_byte_short_refused",
  "artifact_creation_crash_adopted_without_duplicate",
  "artifact_uses_owner_only_permissions",
  "five_unified_gate_passes_verify_source_and_artifact",
  "exact_bytes_metadata_and_empty_directory_restored",
  "foreign_creation_squatter_refused_and_preserved",
  "retention_before_expiry_is_scheduled",
  "foreign_retention_squatter_refused_and_preserved",
  "retention_delete_crash_reconciled",
  "retention_cleanup_idempotent",
  "streaming_rss_growth_bounded",
  "phase_and_lifecycle_time_bounded",
  "public_state_and_report_are_path_content_free",
  "temporary_root_removed",
];

class NeedsYouError extends Error {}
class ExpectedInterruption extends Error {}

function selectedEnvironment(extra = {}) {
  const allowed = ["PATH", "LANG", "LC_ALL", "TMPDIR"];
  const environment = {};
  for (const key of allowed) {
    if (process.env[key] !== undefined) environment[key] = process.env[key];
  }
  environment.GIT_CONFIG_NOSYSTEM = "1";
  environment.GIT_CONFIG_GLOBAL = "/dev/null";
  environment.GIT_TERMINAL_PROMPT = "0";
  return { ...environment, ...extra };
}

function assertSupportedLinux() {
  if (platform() !== "linux") throw new NeedsYouError("unsupported host topology");
}

function command(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? selectedEnvironment(),
    input: options.input,
    maxBuffer: 4 * MIB,
    shell: false,
    timeout: options.timeout,
  });
  if (options.allowFailure) return result;
  if ((result.error && result.status === null) || result.status !== 0) {
    throw new NeedsYouError("bounded external operation failed");
  }
  return result.stdout.trim();
}

function git(cwd, args, options = {}) {
  return command("git", args, { ...options, cwd });
}

function currentCandidate() {
  const candidate = process.env.DIRECTOR_CANDIDATE_SHA
    ?? git(resolve(dirname(fileURLToPath(import.meta.url)), "../../.."), ["rev-parse", "HEAD"]);
  assert.match(candidate, /^[0-9a-f]{40}$/);
  return candidate;
}

function identity(path) {
  const details = statSync(path, { bigint: true });
  return {
    canonicalPath: realpathSync.native(path),
    dev: details.dev.toString(),
    ino: details.ino.toString(),
  };
}

function sameIdentity(expected, actual) {
  return expected.canonicalPath === actual.canonicalPath
    && expected.dev === actual.dev
    && expected.ino === actual.ino;
}

function pathFreeIdentity(value) {
  return { dev: value.dev, ino: value.ino };
}

function samePathFreeIdentity(expected, actual) {
  return expected.dev === actual.dev && expected.ino === actual.ino;
}

function assertOwnedDirectory(path, expectedIdentity) {
  const leaf = lstatSync(path);
  if (leaf.isSymbolicLink() || !leaf.isDirectory()) {
    throw new NeedsYouError("recovery ownership is unverified");
  }
  if (!sameIdentity(expectedIdentity, identity(path))) {
    throw new NeedsYouError("recovery ownership is unverified");
  }
}

function assertSafeRelativePath(path) {
  if (!path || isAbsolute(path)) throw new NeedsYouError("private recovery manifest is invalid");
  const parts = path.replaceAll("\\", "/").split("/");
  if (parts.includes("") || parts.includes(".") || parts.includes("..")) {
    throw new NeedsYouError("private recovery manifest is invalid");
  }
  return parts;
}

function recoveryPath(root, relativePath) {
  const output = join(root, ...assertSafeRelativePath(relativePath));
  const delta = relative(root, output);
  if (!delta || delta === ".." || delta.startsWith(`..${sep}`) || isAbsolute(delta)) {
    throw new NeedsYouError("private recovery manifest is invalid");
  }
  return output;
}

function restrictDirectory(path) {
  chmodSync(path, 0o700);
  return "linux-owner-only-mode";
}

function verifyPermissions(path, permissionModel) {
  assert.equal(permissionModel, "linux-owner-only-mode");
  if ((lstatSync(path).mode & 0o777) !== 0o700) {
    throw new NeedsYouError("recovery permissions are unverified");
  }
}

function writeRestrictedFile(path, content) {
  writeFileSync(path, content, { mode: 0o600 });
  chmodSync(path, 0o600);
}

function fsyncFile(path) {
  const descriptor = openSync(path, "r");
  try {
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

function writePublicState(context, state) {
  assertOwnedDirectory(context.stateRoot, context.stateRootIdentity);
  const serialized = `${JSON.stringify(state, null, 2)}\n`;
  writeRestrictedFile(context.nextStatePath, serialized);
  fsyncFile(context.nextStatePath);
  renameSync(context.nextStatePath, context.statePath);
  fsyncFile(context.stateRoot);
}

function readPublicState(context) {
  try {
    const state = JSON.parse(readFileSync(context.statePath, "utf8"));
    assert.equal(state.schemaVersion, 1);
    assert.equal(state.taskId, context.owner.taskId);
    assert.equal(state.runId, context.owner.runId);
    assert.equal(state.nonce, context.owner.nonce);
    assert.equal(state.candidateSha, context.owner.candidateSha);
    assert.equal(state.artifactId, context.artifactId);
    assert.deepEqual(state.policy, RELEASE_POLICY);
    return state;
  } catch (error) {
    if (error instanceof NeedsYouError) throw error;
    throw new NeedsYouError("recovery state is unavailable or invalid");
  }
}

function ownerRecord(context) {
  return { schemaVersion: 1, ...context.owner };
}

function verifyOwner(path, context) {
  try {
    assert.deepEqual(JSON.parse(readFileSync(join(path, "owner.json"), "utf8")), ownerRecord(context));
  } catch {
    throw new NeedsYouError("recovery owner is unverified");
  }
}

function sampleRss(metrics) {
  metrics.peakRssBytes = Math.max(metrics.peakRssBytes, process.memoryUsage().rss);
}

function measurePhase(metrics, name, operation) {
  const started = performance.now();
  const deadline = started + RELEASE_POLICY.maxPhaseWallMs;
  const tick = () => {
    sampleRss(metrics);
    if (performance.now() > deadline) throw new NeedsYouError("recovery phase exceeded its time budget");
  };
  let result;
  let failure;
  try {
    result = operation(tick);
  } catch (error) {
    failure = error;
  }
  sampleRss(metrics);
  const wallMs = Number((performance.now() - started).toFixed(3));
  if (wallMs > RELEASE_POLICY.maxPhaseWallMs) {
    throw new NeedsYouError("recovery phase exceeded its time budget");
  }
  if (!metrics.phaseWallMs[name]) metrics.phaseWallMs[name] = [];
  metrics.phaseWallMs[name].push(wallMs);
  if (failure) throw failure;
  return result;
}

function sha256File(path, buffer, tick) {
  const descriptor = openSync(path, "r");
  const digest = createHash("sha256");
  try {
    for (;;) {
      const bytes = readSync(descriptor, buffer, 0, buffer.length, null);
      if (bytes === 0) break;
      digest.update(buffer.subarray(0, bytes));
      tick();
    }
  } finally {
    closeSync(descriptor);
  }
  return digest.digest("hex");
}

function streamCopyAndHash(sourcePath, outputPath, buffer, tick) {
  const source = openSync(sourcePath, "r");
  let output;
  const digest = createHash("sha256");
  let copiedBytes = 0;
  try {
    output = openSync(outputPath, "wx", 0o600);
    for (;;) {
      const bytes = readSync(source, buffer, 0, buffer.length, null);
      if (bytes === 0) break;
      digest.update(buffer.subarray(0, bytes));
      let offset = 0;
      while (offset < bytes) {
        const written = writeSync(output, buffer, offset, bytes - offset);
        if (written <= 0) throw new NeedsYouError("recovery stream made no progress");
        offset += written;
      }
      copiedBytes += bytes;
      tick();
    }
    fsyncSync(output);
  } finally {
    closeSync(source);
    if (output !== undefined) closeSync(output);
  }
  return { copiedBytes, sha256: digest.digest("hex") };
}

function metadataDigest(entries) {
  const digest = createHash("sha256");
  for (const entry of entries) {
    digest.update(JSON.stringify({
      path: entry.path,
      type: entry.type,
      size: entry.size ?? null,
      mode: entry.mode,
      mtimeMs: entry.mtimeMs,
    }));
  }
  return digest.digest("hex");
}

function contentDigest(entries) {
  const digest = createHash("sha256");
  for (const entry of entries) {
    digest.update(JSON.stringify({
      path: entry.path,
      type: entry.type,
      size: entry.size ?? null,
      sha256: entry.sha256 ?? null,
    }));
  }
  return digest.digest("hex");
}

function validateReleasePolicy(policy = RELEASE_POLICY) {
  for (const field of [
    "maxBytes", "maxFileBytes", "maxEntries", "maxScanEntries", "streamChunkBytes",
    "maxPhaseWallMs", "maxLifecycleWallMs", "maxRssIncreaseBytes", "retentionMs", "minimumFreePercent",
  ]) {
    assert(Number.isSafeInteger(policy[field]) && policy[field] > 0, `${field} must be positive`);
  }
  assert(policy.maxFileBytes <= policy.maxBytes);
  assert(policy.maxEntries <= policy.maxScanEntries);
  assert(policy.minimumFreePercent >= 10 && policy.minimumFreePercent <= 100);
  assert(policy.retentionMs <= 7 * DAY_MS);
  assert(policy.streamChunkBytes <= 64 * 1024);
}

function validatedPolicy(overrides = {}) {
  const policy = { ...RELEASE_POLICY, ...overrides };
  validateReleasePolicy(policy);
  for (const field of [
    "maxBytes", "maxFileBytes", "maxEntries", "maxScanEntries", "streamChunkBytes",
    "maxPhaseWallMs", "maxLifecycleWallMs", "maxRssIncreaseBytes", "retentionMs",
  ]) {
    assert(policy[field] <= RELEASE_POLICY[field], `${field} expansion requires new evidence`);
  }
  assert(policy.minimumFreePercent >= RELEASE_POLICY.minimumFreePercent, "free-space floor cannot be weakened");
  return Object.freeze(policy);
}

function enumerateRecovery(sourceRoot, ignoredRoots, policy, tick = () => {}) {
  const entries = [];
  let totalBytes = 0;
  let scanEntries = 0;
  const visit = (relativePath) => {
    tick();
    scanEntries += 1;
    if (scanEntries > policy.maxScanEntries) {
      throw new NeedsYouError("worktree inspection exceeds the automatic policy");
    }
    const sourcePath = recoveryPath(sourceRoot, relativePath);
    const details = lstatSync(sourcePath);
    if (details.isSymbolicLink()) {
      throw new NeedsYouError("unsupported ignored material requires human recovery");
    }
    const base = {
      path: relativePath.replaceAll("\\", "/"),
      mode: details.mode & 0o777,
      mtimeMs: Math.trunc(details.mtimeMs),
    };
    if (details.isFile()) {
      if (details.size > policy.maxFileBytes) {
        throw new NeedsYouError("ignored file exceeds the automatic policy");
      }
      totalBytes += details.size;
      if (totalBytes > policy.maxBytes) {
        throw new NeedsYouError("ignored bytes exceed the automatic policy");
      }
      entries.push({ ...base, type: "file", size: details.size });
    } else if (details.isDirectory()) {
      entries.push({ ...base, type: "directory" });
    } else {
      throw new NeedsYouError("unsupported ignored material requires human recovery");
    }
    if (entries.length > policy.maxEntries) {
      throw new NeedsYouError("ignored entries exceed the automatic policy");
    }
    if (details.isDirectory()) {
      const children = readdirSync(sourcePath, { withFileTypes: true })
        .map((entry) => entry.name)
        .sort();
      for (const child of children) visit(`${base.path}/${child}`);
    }
  };
  for (const root of [...ignoredRoots].sort()) visit(root);
  return { entries, totalBytes, scanEntries, metadataDigest: metadataDigest(entries) };
}

function enrichWithSourceHashes(sourceRoot, entries, tick) {
  const buffer = Buffer.alloc(RELEASE_POLICY.streamChunkBytes);
  return entries.map((entry) => entry.type === "file"
    ? { ...entry, sha256: sha256File(recoveryPath(sourceRoot, entry.path), buffer, tick) }
    : entry);
}

function reservedBytes(plan) {
  return BigInt(plan.totalBytes + FIXED_OVERHEAD_BYTES + plan.entries.length * ENTRY_OVERHEAD_BYTES);
}

function assertDiskBudget(plan, availableBytes, totalFilesystemBytes, bytesStillToWrite = reservedBytes(plan)) {
  const minimumFreeBytes = (totalFilesystemBytes * BigInt(RELEASE_POLICY.minimumFreePercent)) / 100n;
  if (availableBytes - bytesStillToWrite < minimumFreeBytes) {
    throw new NeedsYouError("recovery cannot preserve the disk safety floor");
  }
  return { minimumFreeBytes, reservedBytes: bytesStillToWrite };
}

function gitInitIgnoredFixture(sourceRoot) {
  git(sourceRoot, ["init", "--quiet"]);
  writeFileSync(join(sourceRoot, ".gitignore"), "/node_modules/\n/target/\n/.venv/\n");
}

function verifyRepresentativeRootsIgnored(sourceRoot) {
  for (const sample of [
    "node_modules/pkg-0000/file-00.js",
    ".venv/lib/module-0000/module-00.py",
    "target/release/app.bin",
  ]) {
    const result = git(sourceRoot, ["check-ignore", "--quiet", "--no-index", "--", sample], { allowFailure: true });
    assert.equal(result.status, 0);
  }
}

function writePatternFile(path, size, seed, tick) {
  const descriptor = openSync(path, "wx", 0o640);
  const buffer = Buffer.alloc(RELEASE_POLICY.streamChunkBytes);
  let writtenTotal = 0;
  let block = 0;
  try {
    while (writtenTotal < size) {
      buffer.fill((seed + block) % 251);
      const requested = Math.min(buffer.length, size - writtenTotal);
      const written = writeSync(descriptor, buffer, 0, requested);
      if (written <= 0) throw new Error("fixture write made no progress");
      writtenTotal += written;
      block += 1;
      tick();
    }
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

function buildCompositeFixture(sourceRoot, tick) {
  let entryCount = 0;
  let fileCount = 0;
  let directoryCount = 0;
  let smallBytes = 0;
  const fixedMtime = new Date(1_700_000_000_000);

  const addDirectory = (relativePath, mode = 0o750) => {
    mkdirSync(recoveryPath(sourceRoot, relativePath), { recursive: false, mode });
    chmodSync(recoveryPath(sourceRoot, relativePath), mode);
    entryCount += 1;
    directoryCount += 1;
    tick();
  };
  const addSmallFile = (relativePath, content, mode = 0o640) => {
    const bytes = Buffer.from(content);
    writeFileSync(recoveryPath(sourceRoot, relativePath), bytes, { mode });
    chmodSync(recoveryPath(sourceRoot, relativePath), mode);
    entryCount += 1;
    fileCount += 1;
    smallBytes += bytes.length;
    tick();
  };

  addDirectory("node_modules");
  for (let packageIndex = 0; packageIndex < 550; packageIndex += 1) {
    const packageName = `node_modules/pkg-${String(packageIndex).padStart(4, "0")}`;
    addDirectory(packageName, packageIndex % 2 === 0 ? 0o750 : 0o700);
    for (let fileIndex = 0; fileIndex < 10; fileIndex += 1) {
      const leaf = packageIndex === 0 && fileIndex === 0
        ? PRIVATE_SENTINEL_LEAF
        : `file-${String(fileIndex).padStart(2, "0")}.js`;
      const content = packageIndex === 0 && fileIndex === 0
        ? PRIVATE_SENTINEL_VALUE
        : `module:${packageIndex}:${fileIndex}\n`;
      addSmallFile(`${packageName}/${leaf}`, content, fileIndex === 0 ? 0o600 : 0o640);
    }
  }

  addDirectory(".venv");
  addDirectory(".venv/lib");
  for (let moduleIndex = 0; moduleIndex < 300; moduleIndex += 1) {
    const moduleName = `.venv/lib/module-${String(moduleIndex).padStart(4, "0")}`;
    addDirectory(moduleName, moduleIndex % 2 === 0 ? 0o755 : 0o700);
    for (let fileIndex = 0; fileIndex < 10; fileIndex += 1) {
      addSmallFile(`${moduleName}/module-${String(fileIndex).padStart(2, "0")}.py`, `venv:${moduleIndex}:${fileIndex}\n`);
    }
  }

  addDirectory("target");
  addDirectory("target/release");
  for (let unitIndex = 0; unitIndex < 107; unitIndex += 1) {
    const unitName = `target/release/unit-${String(unitIndex).padStart(4, "0")}`;
    addDirectory(unitName);
    for (let fileIndex = 0; fileIndex < 5; fileIndex += 1) {
      addSmallFile(`${unitName}/object-${String(fileIndex).padStart(2, "0")}.o`, `target:${unitIndex}:${fileIndex}\n`);
    }
  }
  addDirectory("target/release/empty-cache", 0o700);

  const remainingBytes = RELEASE_POLICY.maxBytes - smallBytes;
  const firstBinaryBytes = RELEASE_POLICY.maxFileBytes;
  const secondBinaryBytes = remainingBytes - firstBinaryBytes;
  assert(firstBinaryBytes <= RELEASE_POLICY.maxFileBytes);
  assert(secondBinaryBytes <= RELEASE_POLICY.maxFileBytes);
  writePatternFile(recoveryPath(sourceRoot, "target/release/app.bin"), firstBinaryBytes, 17, tick);
  writePatternFile(recoveryPath(sourceRoot, "target/release/debug.bin"), secondBinaryBytes, 31, tick);
  entryCount += 2;
  fileCount += 2;

  for (const relativePath of ["target/release/app.bin", "target/release/debug.bin"]) {
    chmodSync(recoveryPath(sourceRoot, relativePath), 0o640);
    utimesSync(recoveryPath(sourceRoot, relativePath), fixedMtime, fixedMtime);
  }
  const ignoredRoots = ["node_modules", "target", ".venv"];
  for (const root of ignoredRoots) {
    const visit = (directoryPath) => {
      for (const child of readdirSync(directoryPath, { withFileTypes: true })) {
        const childPath = join(directoryPath, child.name);
        if (child.isDirectory()) visit(childPath);
        else utimesSync(childPath, fixedMtime, fixedMtime);
      }
      utimesSync(directoryPath, fixedMtime, fixedMtime);
    };
    visit(recoveryPath(sourceRoot, root));
  }
  return {
    ignoredRoots,
    entryCount,
    fileCount,
    directoryCount,
    totalBytes: RELEASE_POLICY.maxBytes,
    largestFileBytes: Math.max(firstBinaryBytes, secondBinaryBytes),
    profiles: {
      nodeModules: { entries: 6_051 },
      virtualEnv: { entries: 3_302 },
      target: { entries: 647 },
    },
  };
}

function makeContext(root, candidateSha, suffix = "main") {
  const stateRoot = join(root, `state-${suffix}`);
  const recoveryRoot = join(root, `recovery-${suffix}`);
  mkdirSync(stateRoot, { mode: 0o700 });
  mkdirSync(recoveryRoot, { mode: 0o700 });
  chmodSync(stateRoot, 0o700);
  chmodSync(recoveryRoot, 0o700);
  const permissionModel = restrictDirectory(recoveryRoot);
  const nonce = randomBytes(16).toString("hex");
  const artifactId = `artifact-${nonce}`;
  return {
    root,
    stateRoot,
    stateRootIdentity: identity(stateRoot),
    statePath: join(stateRoot, "recovery-state.json"),
    nextStatePath: join(stateRoot, ".recovery-state.next"),
    recoveryRoot,
    recoveryRootIdentity: identity(recoveryRoot),
    permissionModel,
    artifactId,
    artifactPath: join(recoveryRoot, artifactId),
    stagingPath: join(recoveryRoot, `.${artifactId}.partial`),
    owner: {
      taskId: "dir-m0.17",
      runId: `ignored-tree-policy-${suffix}`,
      nonce,
      candidateSha,
    },
  };
}

function initialState(context, plan, nowMs) {
  return {
    schemaVersion: 1,
    phase: "intent_recorded",
    cleanupPhase: "retained",
    ...context.owner,
    artifactId: context.artifactId,
    createdAtMs: nowMs,
    retentionUntilMs: nowMs + RELEASE_POLICY.retentionMs,
    entryCount: plan.entries.length,
    totalBytes: plan.totalBytes,
    metadataDigest: plan.metadataDigest,
    manifestSha256: null,
    contentDigest: null,
    artifactIdentity: null,
    policy: RELEASE_POLICY,
  };
}

function assertContextOwnership(context) {
  assertOwnedDirectory(context.recoveryRoot, context.recoveryRootIdentity);
  verifyPermissions(context.recoveryRoot, context.permissionModel);
  assertOwnedDirectory(context.stateRoot, context.stateRootIdentity);
}

function privateManifest(context, state, entries) {
  return {
    schemaVersion: 1,
    owner: ownerRecord(context),
    createdAtMs: state.createdAtMs,
    retentionUntilMs: state.retentionUntilMs,
    totalBytes: state.totalBytes,
    entries,
  };
}

function verifyArtifact(context, state, tick) {
  try {
    assertContextOwnership(context);
    const leaf = lstatSync(context.artifactPath);
    if (leaf.isSymbolicLink() || !leaf.isDirectory()) throw new Error("invalid artifact leaf");
    if (state.artifactIdentity
        && !samePathFreeIdentity(state.artifactIdentity, pathFreeIdentity(identity(context.artifactPath)))) {
      throw new Error("artifact identity changed");
    }
    verifyPermissions(context.artifactPath, context.permissionModel);
    verifyOwner(context.artifactPath, context);
    assert.equal(lstatSync(join(context.artifactPath, "owner.json")).mode & 0o777, 0o600);
    assert.equal(lstatSync(join(context.artifactPath, "manifest.json")).mode & 0o777, 0o600);
    assert.equal(lstatSync(join(context.artifactPath, "payload")).mode & 0o777, 0o700);
    const manifestPath = join(context.artifactPath, "manifest.json");
    const manifestSha256 = sha256File(manifestPath, Buffer.alloc(RELEASE_POLICY.streamChunkBytes), tick);
    if (state.manifestSha256) assert.equal(manifestSha256, state.manifestSha256);
    const record = JSON.parse(readFileSync(manifestPath, "utf8"));
    assert.deepEqual(record.owner, ownerRecord(context));
    assert.equal(record.createdAtMs, state.createdAtMs);
    assert.equal(record.retentionUntilMs, state.retentionUntilMs);
    assert.equal(record.totalBytes, state.totalBytes);
    assert.equal(record.entries.length, state.entryCount);
    assert.equal(metadataDigest(record.entries), state.metadataDigest);
    const buffer = Buffer.alloc(RELEASE_POLICY.streamChunkBytes);
    for (const entry of record.entries) {
      tick();
      const payloadPath = recoveryPath(join(context.artifactPath, "payload"), entry.path);
      const details = lstatSync(payloadPath);
      if (entry.type === "directory") {
        assert(details.isDirectory());
        assert.equal(details.mode & 0o777, 0o700);
      } else {
        assert(details.isFile());
        assert.equal(details.size, entry.size);
        assert.equal(sha256File(payloadPath, buffer, tick), entry.sha256);
        assert.equal(details.mode & 0o777, 0o600);
      }
    }
    const verifiedContentDigest = contentDigest(record.entries);
    if (state.contentDigest) assert.equal(verifiedContentDigest, state.contentDigest);
    return { record, manifestSha256, contentDigest: verifiedContentDigest };
  } catch (error) {
    if (error instanceof NeedsYouError) throw error;
    throw new NeedsYouError("recovery artifact ownership or content is unverified");
  }
}

function writeArtifact(context, state, sourceRoot, plan, tick) {
  mkdirSync(context.stagingPath, { mode: 0o700 });
  restrictDirectory(context.stagingPath);
  writeRestrictedFile(join(context.stagingPath, "owner.json"), `${JSON.stringify(ownerRecord(context), null, 2)}\n`);
  const payloadRoot = join(context.stagingPath, "payload");
  mkdirSync(payloadRoot, { mode: 0o700 });
  chmodSync(payloadRoot, 0o700);
  const buffer = Buffer.alloc(RELEASE_POLICY.streamChunkBytes);
  const artifactEntries = [];
  for (const entry of plan.entries) {
    tick();
    const outputPath = recoveryPath(payloadRoot, entry.path);
    if (entry.type === "directory") {
      mkdirSync(outputPath, { recursive: true, mode: 0o700 });
      chmodSync(outputPath, 0o700);
      artifactEntries.push(entry);
    } else {
      mkdirSync(dirname(outputPath), { recursive: true, mode: 0o700 });
      const copied = streamCopyAndHash(recoveryPath(sourceRoot, entry.path), outputPath, buffer, tick);
      assert.equal(copied.copiedBytes, entry.size);
      chmodSync(outputPath, 0o600);
      artifactEntries.push({ ...entry, sha256: copied.sha256 });
    }
  }
  const afterCopy = enumerateRecovery(sourceRoot, ["node_modules", "target", ".venv"], RELEASE_POLICY, tick);
  assert.equal(afterCopy.metadataDigest, plan.metadataDigest);
  const sourceEntries = enrichWithSourceHashes(sourceRoot, afterCopy.entries, tick);
  assert.equal(sourceEntries.length, artifactEntries.length);
  for (let index = 0; index < sourceEntries.length; index += 1) {
    const sourceEntry = sourceEntries[index];
    const artifactEntry = artifactEntries[index];
    assert.equal(sourceEntry.path, artifactEntry.path, `recovery entry order changed at index ${index}`);
    assert.equal(sourceEntry.type, artifactEntry.type, `recovery entry type changed at index ${index}`);
    assert.equal(sourceEntry.size, artifactEntry.size, `recovery entry size changed at index ${index}`);
    assert.equal(sourceEntry.sha256, artifactEntry.sha256, `recovery entry bytes changed at index ${index}`);
  }
  assert.equal(contentDigest(sourceEntries), contentDigest(artifactEntries));
  const record = privateManifest(context, state, artifactEntries);
  writeRestrictedFile(join(context.stagingPath, "manifest.json"), `${JSON.stringify(record, null, 2)}\n`);
  renameSync(context.stagingPath, context.artifactPath);
  const verified = verifyArtifact(context, state, tick);
  assert.equal(verified.contentDigest, contentDigest(sourceEntries));
  return verified;
}

function adoptOrCreateArtifact(context, sourceRoot, plan, metrics, options = {}) {
  return measurePhase(metrics, options.phaseName ?? "artifact_create_or_adopt", (tick) => {
    assertContextOwnership(context);
    let state;
    if (existsSync(context.statePath)) {
      state = readPublicState(context);
    } else {
      state = initialState(context, plan, options.nowMs ?? Date.now());
      writePublicState(context, state);
    }
    assert.equal(state.entryCount, plan.entries.length);
    assert.equal(state.totalBytes, plan.totalBytes);
    assert.equal(state.metadataDigest, plan.metadataDigest);
    const filesystem = statfsSync(context.recoveryRoot, { bigint: true });
    const artifactExists = existsSync(context.artifactPath);
    assertDiskBudget(
      plan,
      filesystem.bavail * filesystem.bsize,
      filesystem.blocks * filesystem.bsize,
      artifactExists ? 0n : reservedBytes(plan),
    );
    if (artifactExists) {
      const verified = verifyArtifact(context, state, tick);
      const sourceEntries = enrichWithSourceHashes(sourceRoot, plan.entries, tick);
      assert.equal(contentDigest(sourceEntries), verified.contentDigest);
      state.phase = "artifact_verified";
      state.manifestSha256 = verified.manifestSha256;
      state.contentDigest = verified.contentDigest;
      state.artifactIdentity = pathFreeIdentity(identity(context.artifactPath));
      writePublicState(context, state);
      return state;
    }
    if (existsSync(context.stagingPath)) {
      const stagingLeaf = lstatSync(context.stagingPath);
      if (stagingLeaf.isSymbolicLink() || !stagingLeaf.isDirectory()) {
        throw new NeedsYouError("partial recovery ownership is unverified");
      }
      verifyOwner(context.stagingPath, context);
      rmSync(context.stagingPath, { recursive: true });
    }
    const verified = writeArtifact(context, state, sourceRoot, plan, tick);
    if (options.interruptAfterRename) {
      throw new ExpectedInterruption("simulated crash after artifact rename");
    }
    state.phase = "artifact_verified";
    state.manifestSha256 = verified.manifestSha256;
    state.contentDigest = verified.contentDigest;
    state.artifactIdentity = pathFreeIdentity(identity(context.artifactPath));
    writePublicState(context, state);
    return state;
  });
}

function verifyUnifiedGate(context, state, sourceRoot, metrics, passNumber) {
  return measurePhase(metrics, `gate_${passNumber}`, (tick) => {
    assert.equal(state.phase, "artifact_verified");
    const verified = verifyArtifact(context, state, tick);
    const plan = enumerateRecovery(sourceRoot, ["node_modules", "target", ".venv"], RELEASE_POLICY, tick);
    assert.equal(plan.entries.length, state.entryCount);
    assert.equal(plan.totalBytes, state.totalBytes);
    assert.equal(plan.metadataDigest, state.metadataDigest);
    const sourceEntries = enrichWithSourceHashes(sourceRoot, plan.entries, tick);
    assert.equal(contentDigest(sourceEntries), verified.contentDigest);
    const filesystem = statfsSync(context.recoveryRoot, { bigint: true });
    assertDiskBudget(plan, filesystem.bavail * filesystem.bsize, filesystem.blocks * filesystem.bsize, 0n);
    return verified;
  });
}

function restoreArtifact(context, state, destination, metrics) {
  return measurePhase(metrics, "restore", (tick) => {
    const { record, contentDigest: expectedContentDigest } = verifyArtifact(context, state, tick);
    const filesystem = statfsSync(dirname(destination), { bigint: true });
    assertDiskBudget(
      { entries: record.entries, totalBytes: record.totalBytes },
      filesystem.bavail * filesystem.bsize,
      filesystem.blocks * filesystem.bsize,
    );
    mkdirSync(destination, { mode: 0o700 });
    const buffer = Buffer.alloc(RELEASE_POLICY.streamChunkBytes);
    const directories = record.entries.filter((entry) => entry.type === "directory");
    for (const entry of directories) {
      mkdirSync(recoveryPath(destination, entry.path), { recursive: true });
    }
    for (const entry of record.entries) {
      tick();
      const outputPath = recoveryPath(destination, entry.path);
      if (entry.type === "file") {
        mkdirSync(dirname(outputPath), { recursive: true });
        const copied = streamCopyAndHash(
          recoveryPath(join(context.artifactPath, "payload"), entry.path),
          outputPath,
          buffer,
          tick,
        );
        assert.equal(copied.copiedBytes, entry.size);
        assert.equal(copied.sha256, entry.sha256);
        chmodSync(outputPath, entry.mode);
        utimesSync(outputPath, new Date(entry.mtimeMs), new Date(entry.mtimeMs));
      }
    }
    for (const entry of [...directories].sort((left, right) => right.path.length - left.path.length)) {
      const outputPath = recoveryPath(destination, entry.path);
      chmodSync(outputPath, entry.mode);
      utimesSync(outputPath, new Date(entry.mtimeMs), new Date(entry.mtimeMs));
    }
    const restored = enumerateRecovery(destination, ["node_modules", "target", ".venv"], RELEASE_POLICY, tick);
    assert.equal(restored.metadataDigest, state.metadataDigest);
    const restoredEntries = enrichWithSourceHashes(destination, restored.entries, tick);
    assert.equal(contentDigest(restoredEntries), expectedContentDigest);
    assert(existsSync(join(destination, "target", "release", "empty-cache")));
    assert.equal(readdirSync(join(destination, "target", "release", "empty-cache")).length, 0);
  });
}

function cleanupRetainedArtifact(context, nowMs, options = {}) {
  assertContextOwnership(context);
  const state = readPublicState(context);
  if (state.phase !== "complete") throw new NeedsYouError("recovery is still required by active cleanup");
  if (existsSync(context.artifactPath)) {
    const verified = verifyArtifact(context, state, () => {});
    assert.equal(verified.manifestSha256, state.manifestSha256);
    if (nowMs < state.retentionUntilMs) return { status: "retained" };
    state.cleanupPhase = "removal_ready";
    writePublicState(context, state);
    assertContextOwnership(context);
    verifyArtifact(context, state, () => {});
    rmSync(context.artifactPath, { recursive: true });
    if (options.interruptAfterRemoval) {
      throw new ExpectedInterruption("simulated crash after retention removal");
    }
  } else if (!new Set(["removal_ready", "removed"]).has(state.cleanupPhase)) {
    throw new NeedsYouError("recovery artifact absence is unverified");
  }
  state.cleanupPhase = "removed";
  writePublicState(context, state);
  return { status: "removed" };
}

function expectNeedsYou(assertions, name, operation) {
  assert.throws(operation, NeedsYouError);
  assertions.push(name);
}

function makeSparseFile(path, size) {
  writeFileSync(path, "");
  truncateSync(path, size);
}

function exercisePolicyBoundaries(root, sourceRoot, plan, metrics, assertions) {
  measurePhase(metrics, "entry_plus_one_refusal", (tick) => {
    mkdirSync(join(sourceRoot, "target", "release", "one-entry-too-many"));
    expectNeedsYou(assertions, "entry_limit_plus_one_refused", () => {
      enumerateRecovery(sourceRoot, ["node_modules", "target", ".venv"], RELEASE_POLICY, tick);
    });
    assert(existsSync(join(sourceRoot, "target", "release", "one-entry-too-many")));
    rmSync(join(sourceRoot, "target", "release", "one-entry-too-many"), { recursive: true });
    const parentMetadata = plan.entries.find((entry) => entry.path === "target/release");
    assert(parentMetadata);
    utimesSync(
      join(sourceRoot, "target", "release"),
      new Date(parentMetadata.mtimeMs),
      new Date(parentMetadata.mtimeMs),
    );
  });

  const limitRoot = join(root, "limit-fixtures");
  mkdirSync(limitRoot);
  const aggregateRoot = join(limitRoot, "aggregate");
  mkdirSync(aggregateRoot);
  makeSparseFile(join(aggregateRoot, "first.bin"), RELEASE_POLICY.maxFileBytes);
  makeSparseFile(join(aggregateRoot, "second.bin"), RELEASE_POLICY.maxFileBytes);
  makeSparseFile(join(aggregateRoot, "plus-one.bin"), 1);
  measurePhase(metrics, "aggregate_plus_one_refusal", (tick) => {
    expectNeedsYou(assertions, "aggregate_byte_limit_plus_one_refused_before_read", () => {
      enumerateRecovery(limitRoot, ["aggregate"], RELEASE_POLICY, () => {
        tick();
      });
    });
    assert.equal(statSync(join(aggregateRoot, "first.bin")).size, RELEASE_POLICY.maxFileBytes);
    assert.equal(statSync(join(aggregateRoot, "second.bin")).size, RELEASE_POLICY.maxFileBytes);
  });

  const fileRoot = join(limitRoot, "file");
  mkdirSync(fileRoot);
  makeSparseFile(join(fileRoot, "too-large.bin"), RELEASE_POLICY.maxFileBytes + 1);
  measurePhase(metrics, "file_plus_one_refusal", (tick) => {
    expectNeedsYou(assertions, "file_byte_limit_plus_one_refused_before_read", () => {
      enumerateRecovery(limitRoot, ["file"], RELEASE_POLICY, tick);
    });
    assert.equal(statSync(join(fileRoot, "too-large.bin")).size, RELEASE_POLICY.maxFileBytes + 1);
  });

  assert.throws(() => validatedPolicy({ maxScanEntries: RELEASE_POLICY.maxScanEntries + 1 }));
  assertions.push("scan_limit_plus_one_refused");
  for (const expansion of [
    { maxBytes: RELEASE_POLICY.maxBytes + 1 },
    { maxFileBytes: RELEASE_POLICY.maxFileBytes + 1 },
    { maxEntries: RELEASE_POLICY.maxEntries + 1 },
    { streamChunkBytes: RELEASE_POLICY.streamChunkBytes + 1 },
    { retentionMs: RELEASE_POLICY.retentionMs + 1 },
    { minimumFreePercent: RELEASE_POLICY.minimumFreePercent - 1 },
  ]) {
    assert.throws(() => validatedPolicy(expansion));
  }
  assert.doesNotThrow(() => validatedPolicy({ maxBytes: RELEASE_POLICY.maxBytes - 1 }));
  assertions.push("policy_expansion_requires_new_evidence");

  const required = reservedBytes(plan);
  const total = 10_000_000_000n;
  const floor = total / 10n;
  assert.doesNotThrow(() => assertDiskBudget(plan, required + floor, total));
  assertions.push("disk_floor_exact_boundary_accepted");
  expectNeedsYou(assertions, "disk_floor_one_byte_short_refused", () => {
    assertDiskBudget(plan, required + floor - 1n, total);
  });
}

function exerciseCreationSquatter(root, candidateSha, sourceRoot, plan, metrics, assertions) {
  const context = makeContext(root, candidateSha, "creation-squatter");
  const state = initialState(context, plan, Date.now());
  writePublicState(context, state);
  mkdirSync(context.artifactPath, { mode: 0o700 });
  writeRestrictedFile(join(context.artifactPath, "owner.json"), `${JSON.stringify({ owner: "foreign" })}\n`);
  writeFileSync(join(context.artifactPath, "unknown-sentinel"), "preserve");
  expectNeedsYou(assertions, "foreign_creation_squatter_refused_and_preserved", () => {
    adoptOrCreateArtifact(context, sourceRoot, plan, metrics, { phaseName: "creation_squatter_refusal" });
  });
  assert.equal(readFileSync(join(context.artifactPath, "unknown-sentinel"), "utf8"), "preserve");
}

function exerciseRetention(context, state, assertions) {
  state.phase = "complete";
  writePublicState(context, state);
  assert.deepEqual(cleanupRetainedArtifact(context, state.retentionUntilMs - 1), { status: "retained" });
  assertions.push("retention_before_expiry_is_scheduled");

  const ownedHoldingPath = `${context.artifactPath}.owned-holding`;
  renameSync(context.artifactPath, ownedHoldingPath);
  mkdirSync(context.artifactPath, { mode: 0o700 });
  writeRestrictedFile(join(context.artifactPath, "owner.json"), `${JSON.stringify({ owner: "foreign" })}\n`);
  writeFileSync(join(context.artifactPath, "unknown-sentinel"), "preserve");
  expectNeedsYou(assertions, "foreign_retention_squatter_refused_and_preserved", () => {
    cleanupRetainedArtifact(context, state.retentionUntilMs);
  });
  assert.equal(readFileSync(join(context.artifactPath, "unknown-sentinel"), "utf8"), "preserve");
  rmSync(context.artifactPath, { recursive: true });
  renameSync(ownedHoldingPath, context.artifactPath);

  assert.throws(
    () => cleanupRetainedArtifact(context, state.retentionUntilMs, { interruptAfterRemoval: true }),
    ExpectedInterruption,
  );
  assert(!existsSync(context.artifactPath));
  assert.deepEqual(cleanupRetainedArtifact(context, state.retentionUntilMs), { status: "removed" });
  assertions.push("retention_delete_crash_reconciled");
  assert.deepEqual(cleanupRetainedArtifact(context, state.retentionUntilMs + 1), { status: "removed" });
  assertions.push("retention_cleanup_idempotent");
}

function runBenchmarkWorker() {
  assertSupportedLinux();
  validateReleasePolicy();
  const nodeMajor = Number(process.versions.node.split(".")[0]);
  assert(nodeMajor >= 22, "Node.js 22 or newer is required");
  const candidateSha = currentCandidate();
  const assertions = [];
  const root = mkdtempSync(join(tmpdir(), "director-m0.17-"));
  let report;
  try {
    const sourceRoot = join(root, "source");
    mkdirSync(sourceRoot);
    gitInitIgnoredFixture(sourceRoot);
    const setupStarted = performance.now();
    const fixture = buildCompositeFixture(sourceRoot, () => {});
    const fixtureSetupWallMs = Number((performance.now() - setupStarted).toFixed(3));
    verifyRepresentativeRootsIgnored(sourceRoot);
    assertions.push("representative_roots_are_git_ignored");

    if (global.gc) global.gc();
    const baselineRssBytes = process.memoryUsage().rss;
    const metrics = { baselineRssBytes, peakRssBytes: baselineRssBytes, phaseWallMs: {} };
    const lifecycleStarted = performance.now();
    const plan = measurePhase(metrics, "enumerate_exact_default", (tick) => (
      enumerateRecovery(sourceRoot, fixture.ignoredRoots, RELEASE_POLICY, tick)
    ));
    assert.equal(plan.entries.length, RELEASE_POLICY.maxEntries);
    assert.equal(plan.totalBytes, RELEASE_POLICY.maxBytes);
    assert.equal(fixture.entryCount, RELEASE_POLICY.maxEntries);
    assert.equal(fixture.totalBytes, RELEASE_POLICY.maxBytes);
    assert(fixture.largestFileBytes <= RELEASE_POLICY.maxFileBytes);
    assertions.push("composite_tree_hits_exact_entry_byte_and_file_defaults");

    exercisePolicyBoundaries(root, sourceRoot, plan, metrics, assertions);
    const context = makeContext(root, candidateSha);
    const creationStarted = performance.now();
    assert.throws(
      () => adoptOrCreateArtifact(context, sourceRoot, plan, metrics, {
        interruptAfterRename: true,
        phaseName: "artifact_create_interrupted",
      }),
      ExpectedInterruption,
    );
    const identityAfterInterruptedRename = pathFreeIdentity(identity(context.artifactPath));
    const adoptedState = adoptOrCreateArtifact(context, sourceRoot, plan, metrics, {
      phaseName: "artifact_retry_adoption",
    });
    const artifactCreateAndRetryWallMs = Number((performance.now() - creationStarted).toFixed(3));
    assert(samePathFreeIdentity(identityAfterInterruptedRename, adoptedState.artifactIdentity));
    assert(!existsSync(context.stagingPath));
    assert.equal(readdirSync(context.recoveryRoot).length, 1);
    assertions.push("artifact_creation_crash_adopted_without_duplicate");
    verifyPermissions(context.artifactPath, context.permissionModel);
    assertions.push("artifact_uses_owner_only_permissions");

    for (let passNumber = 1; passNumber <= GATE_PASSES; passNumber += 1) {
      verifyUnifiedGate(context, adoptedState, sourceRoot, metrics, passNumber);
    }
    assertions.push("five_unified_gate_passes_verify_source_and_artifact");

    const restoreRoot = join(root, "restored");
    restoreArtifact(context, adoptedState, restoreRoot, metrics);
    assertions.push("exact_bytes_metadata_and_empty_directory_restored");
    exerciseCreationSquatter(root, candidateSha, sourceRoot, plan, metrics, assertions);
    exerciseRetention(context, adoptedState, assertions);

    const lifecycleWallMs = Number((performance.now() - lifecycleStarted).toFixed(3));
    sampleRss(metrics);
    const rssIncreaseBytes = Math.max(0, metrics.peakRssBytes - metrics.baselineRssBytes);
    assert(rssIncreaseBytes <= RELEASE_POLICY.maxRssIncreaseBytes);
    assertions.push("streaming_rss_growth_bounded");
    assert(lifecycleWallMs <= RELEASE_POLICY.maxLifecycleWallMs);
    for (const values of Object.values(metrics.phaseWallMs)) {
      for (const value of values) assert(value <= RELEASE_POLICY.maxPhaseWallMs);
    }
    assertions.push("phase_and_lifecycle_time_bounded");

    const publicState = readFileSync(context.statePath, "utf8");
    for (const forbidden of [PRIVATE_SENTINEL_LEAF, PRIVATE_SENTINEL_VALUE, sourceRoot, context.artifactPath]) {
      assert(!publicState.includes(forbidden));
    }
    assertions.push("public_state_and_report_are_path_content_free");
    report = {
      result: "pass",
      task: "dir-m0.17",
      provisionalBase: PROVISIONAL_BASE,
      candidateSha,
      host: {
        os: platform(),
        release: release(),
        arch: arch(),
        node: process.version,
        git: gitVersion(),
      },
      policy: RELEASE_POLICY,
      fixture: {
        profiles: fixture.profiles,
        entryCount: fixture.entryCount,
        fileCount: fixture.fileCount,
        directoryCount: fixture.directoryCount,
        totalBytes: fixture.totalBytes,
        largestFileBytes: fixture.largestFileBytes,
        gatePasses: GATE_PASSES,
      },
      measurements: {
        fixtureSetupWallMs,
        artifactCreateAndRetryWallMs,
        lifecycleWallMs,
        baselineRssBytes: metrics.baselineRssBytes,
        peakRssBytes: metrics.peakRssBytes,
        rssIncreaseBytes,
        phaseWallMs: metrics.phaseWallMs,
      },
      assertions,
    };
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
  assert(!existsSync(root));
  report.assertions.push("temporary_root_removed");
  assert.deepEqual(report.assertions, EXPECTED_WORKER_ASSERTIONS);
  const serialized = JSON.stringify(report);
  for (const forbidden of [PRIVATE_SENTINEL_LEAF, PRIVATE_SENTINEL_VALUE, root]) {
    assert(!serialized.includes(forbidden));
  }
  if (process.env.DIRECTOR_REPORT_PATH) {
    const reportPath = resolve(process.env.DIRECTOR_REPORT_PATH);
    const reportRoot = dirname(reportPath);
    const reportRootDelta = relative(tmpdir(), reportRoot);
    assert.equal(basename(reportPath), "aggregate-report.json");
    assert(basename(reportRoot).startsWith("director-m0.17-report-"));
    assert(reportRootDelta && reportRootDelta !== ".." && !reportRootDelta.startsWith(`..${sep}`));
    const reportRootLeaf = lstatSync(reportRoot);
    assert(reportRootLeaf.isDirectory() && !reportRootLeaf.isSymbolicLink());
    writeRestrictedFile(reportPath, `${JSON.stringify(report, null, 2)}\n`);
    fsyncFile(reportPath);
  } else {
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  }
}

function gitVersion() {
  const version = command("git", ["--version"]);
  assert.match(version, /^git version /);
  return version.replace(/^git version /, "");
}

function exerciseHardTimeout(candidateSha) {
  const root = mkdtempSync(join(tmpdir(), "director-m0.17-timeout-"));
  try {
    const sourceMarker = join(root, "owned-source-sentinel");
    const stateMarker = join(root, "recovery-state.json");
    const readyMarker = join(root, "worker-ready");
    writeFileSync(sourceMarker, "preserve");
    writeFileSync(stateMarker, JSON.stringify({ phase: "intent_recorded", candidateSha }));
    const beforeState = readFileSync(stateMarker, "utf8");
    const started = performance.now();
    const result = spawnSync(process.execPath, [fileURLToPath(import.meta.url), "--timeout-worker"], {
      encoding: "utf8",
      env: selectedEnvironment({ DIRECTOR_TIMEOUT_READY_PATH: readyMarker }),
      maxBuffer: MIB,
      shell: false,
      timeout: TIMEOUT_FIXTURE_MS,
    });
    const elapsedMs = performance.now() - started;
    assert.equal(result.status, null);
    assert(["ETIMEDOUT", "EPERM"].includes(result.error?.code));
    assert(elapsedMs >= TIMEOUT_FIXTURE_MS * 0.9);
    assert(existsSync(readyMarker));
    assert.equal(readFileSync(sourceMarker, "utf8"), "preserve");
    assert.equal(readFileSync(stateMarker, "utf8"), beforeState);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
  assert(!existsSync(root));
}

function runParent() {
  assertSupportedLinux();
  const candidateSha = currentCandidate();
  const protocolRoot = mkdtempSync(join(tmpdir(), "director-m0.17-report-"));
  const protocolPermissionModel = restrictDirectory(protocolRoot);
  const protocolRootIdentity = identity(protocolRoot);
  const reportPath = join(protocolRoot, "aggregate-report.json");
  let failureStage = "supervisor_probe";
  let protocolDiagnostics;
  try {
    exerciseHardTimeout(candidateSha);
    failureStage = "benchmark_worker";
    const result = spawnSync(process.execPath, ["--expose-gc", fileURLToPath(import.meta.url), "--benchmark-worker"], {
      encoding: "utf8",
      env: selectedEnvironment({
        DIRECTOR_CANDIDATE_SHA: candidateSha,
        DIRECTOR_REPORT_PATH: reportPath,
      }),
      maxBuffer: 4 * MIB,
      shell: false,
      timeout: WORKER_TIMEOUT_MS,
    });
    if (result.error?.code === "ETIMEDOUT") throw new NeedsYouError("benchmark worker exceeded its hard deadline");
    const reportExists = existsSync(reportPath);
    const reconciledSandboxResult = result.status === null
      && result.error?.code === "EPERM"
      && reportExists;
    if (result.status !== 0 && !reconciledSandboxResult) {
      throw new NeedsYouError("benchmark worker failed path-free");
    }
    failureStage = "report_parse";
    assertOwnedDirectory(protocolRoot, protocolRootIdentity);
    verifyPermissions(protocolRoot, protocolPermissionModel);
    const reportLeaf = lstatSync(reportPath);
    assert(reportLeaf.isFile() && !reportLeaf.isSymbolicLink());
    assert.equal(reportLeaf.mode & 0o777, 0o600);
    const reportText = readFileSync(reportPath, "utf8").trim();
    protocolDiagnostics = {
      reportBytes: Buffer.byteLength(reportText),
      startsWithObject: reportText.startsWith("{"),
      endsWithObject: reportText.endsWith("}"),
    };
    const report = JSON.parse(reportText);
    failureStage = "report_result";
    assert.equal(report.result, "pass");
    failureStage = "candidate_binding";
    assert.equal(report.candidateSha, candidateSha);
    report.assertions.push("hard_worker_timeout_preserves_owned_source");
    report.workerSupervisorTimeoutMs = WORKER_TIMEOUT_MS;
    failureStage = "report_secrecy";
    const serialized = JSON.stringify(report);
    for (const forbidden of [PRIVATE_SENTINEL_LEAF, PRIVATE_SENTINEL_VALUE, tmpdir()]) {
      assert(!serialized.includes(forbidden));
    }
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  } catch (error) {
    error.failureStage = failureStage;
    error.protocolDiagnostics = protocolDiagnostics;
    throw error;
  } finally {
    rmSync(protocolRoot, { recursive: true, force: true });
  }
  assert(!existsSync(protocolRoot));
}

if (process.argv[2] === "--benchmark-worker") {
  runBenchmarkWorker();
} else if (process.argv[2] === "--timeout-worker") {
  const readyPath = process.env.DIRECTOR_TIMEOUT_READY_PATH;
  if (!readyPath) process.exit(19);
  writeFileSync(readyPath, "ready");
  for (;;) {
    // The parent process owns and enforces this disposable worker deadline.
  }
} else if (process.argv[2] === "--timeout-probe") {
  assertSupportedLinux();
  exerciseHardTimeout(currentCandidate());
  process.stdout.write(`${JSON.stringify({ result: "pass", assertion: "hard_worker_timeout_preserves_owned_source" })}\n`);
} else {
  try {
    runParent();
  } catch (error) {
    const kind = error instanceof NeedsYouError ? "needs_you" : "failed";
    process.stderr.write(`${JSON.stringify({
      result: kind,
      task: "dir-m0.17",
      failureStage: error.failureStage ?? "worker_entry",
      protocolDiagnostics: error.protocolDiagnostics ?? null,
    })}\n`);
    process.exitCode = 1;
  }
}
