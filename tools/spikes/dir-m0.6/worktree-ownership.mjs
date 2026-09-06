#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
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
  readlinkSync,
  readdirSync,
  readSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  statfsSync,
  symlinkSync,
  truncateSync,
  utimesSync,
  writeSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:http";
import { isAbsolute, join, relative, resolve, sep } from "node:path";
import { platform, release, tmpdir } from "node:os";
import { createHash } from "node:crypto";
import { fileURLToPath, pathToFileURL } from "node:url";

const ZERO_SHA = "0".repeat(40);
const MAIN_REF = "refs/heads/main";
const TASK_REF = "refs/heads/director/task-dir-m0.6-run-1";
const RECOVERY_REF = "refs/director/recovery/dir-m0.6/run-1/contract-nonce";
const CLEAN_REF = "refs/heads/director/task-dir-m0.6-clean-run";
const CLEAN_RECOVERY_REF = "refs/director/recovery/dir-m0.6/clean-run/clean-nonce";
const CLEAN_LOCK_REF = "refs/heads/director/task-dir-m0.6-clean-lock-run";
const CLEAN_LOCK_RECOVERY_REF = "refs/director/recovery/dir-m0.6/clean-lock-run/clean-lock-nonce";
const ASSUME_REF = "refs/heads/director/task-dir-m0.6-assume-hidden";
const SKIP_REF = "refs/heads/director/task-dir-m0.6-skip-hidden";
const SPARSE_REF = "refs/heads/director/task-dir-m0.6-sparse-missing";
const NORMALIZATION_REF = "refs/heads/director/task-dir-m0.6-normalization";
const STAGED_ONLY_REF = "refs/heads/director/task-dir-m0.6-staged-only";
const STAGED_ADDITION_REF = "refs/heads/director/task-dir-m0.6-staged-addition";
const STAGED_DELETION_REF = "refs/heads/director/task-dir-m0.6-staged-deletion";
const TOKEN_GUARD_REF = "refs/heads/director/task-dir-m0.6-token-guard";
const IGNORED_RECOVERY_FIXED_OVERHEAD_BYTES = 64 * 1024;
const IGNORED_RECOVERY_ENTRY_OVERHEAD_BYTES = 4096;
const DEFAULT_IGNORED_RECOVERY_POLICY = Object.freeze({
  maxBytes: 2 * 1024 * 1024 * 1024,
  maxEntries: 10_000,
  maxScanEntries: 100_000,
  maxFileBytes: 2 * 1024 * 1024 * 1024,
  streamChunkBytes: 64 * 1024,
  retentionMs: 7 * 24 * 60 * 60 * 1000,
  minimumFreePercent: 10,
});
const DISPOSABLE_LOCAL_ORIGIN_AUTHORITY = Object.freeze({
  humanApproved: true,
  contained: true,
});
const PASEO_SCHEMA = {
  package: "@getpaseo/protocol",
  version: "0.7.2",
  declarationSha256: "cca82722dea170cfa64862c802784d78b12b2d6ebb0ddbfda0f0ff720ced3724",
  runtimeSha256: "46196b354916602f8cadb87fe4690755c2e98d5305648864841cab41309469c7",
  automaticWorktreeSurfaces: [
    "worktree.setup",
    "worktree.teardown",
    "worktree.terminals",
    "worktree.servicePorts.portScript",
  ],
};
const CLEAN_LOCK_ASSERTION = platform() === "win32"
  ? "windows_clean_removal_lock_fail_closed"
  : "linux_clean_open_handle_removal";
const DIRTY_LOCK_ASSERTION = platform() === "win32"
  ? "windows_dirty_snapshot_lock_fail_closed"
  : "linux_dirty_open_handle_recovery";
const SPECIAL_MATERIAL_ASSERTION = platform() === "win32"
  ? "windows_reparse_material_needs_you"
  : "linux_symlink_fifo_and_socket_material_needs_you";
const EXPECTED_ASSERTIONS = [
  "all_installed_lifecycle_surfaces_refused",
  "state_file_fsync_platform_access_and_failure",
  "local_origin_effects_require_approval_and_containment",
  "file_url_origin_refused_consistently",
  "destructive_commands_require_fresh_gate_token",
  "task_branch_and_worktree_owned",
  "worktree_registration_identity_verified",
  "committed_symlink_and_inactive_filter_supported",
  "atomic_intent_write_and_corruption_fail_closed",
  "detached_review_exact_candidate",
  "source_and_common_dir_refused",
  "unknown_worktree_refused",
  "lexical_alias_refused",
  "canonical_alias_refused",
  "path_swap_refused",
  "quarantine_squatting_needs_you",
  "administrative_lock_refused",
  "foreign_common_dir_refused",
  "unintegrated_refs_refused",
  "unintegrated_clean_worktree_preserved",
  "remote_compare_delete_race_refused",
  "assume_unchanged_edit_recovered",
  "skip_worktree_edit_recovered",
  "staged_index_only_edit_recovered",
  "staged_index_only_addition_recovered",
  "staged_index_only_deletion_recovered",
  "sparse_missing_material_needs_you_then_recovers",
  "eol_normalization_needs_you_without_false_recovery",
  "ls_remote_statuses_distinguished",
  "offline_and_auth_errors_refused",
  "unproven_recovery_policy_expansion_blocked",
  "ignored_recovery_max_bytes_boundary_enforced",
  "ignored_recovery_max_file_bytes_boundary_enforced",
  "ignored_recovery_minimum_free_percent_boundary_enforced",
  "ignored_recovery_retention_boundary_enforced",
  "ignored_recovery_disk_pressure_refused",
  "oversized_sparse_ignored_needs_you_before_read",
  "foreign_owner_recovery_squat_needs_you",
  "ignored_recovery_artifact_effect_crash_reconciled",
  "ignored_recovery_replacement_refused",
  "ignored_content_change_path_free_needs_you",
  "ignored_recovery_retry_charges_only_remaining_bytes",
  "destructive_gate_token_binds_exact_command_once",
  "stored_evidence_gate_artifact_before_move_table",
  "stored_evidence_gate_artifact_before_remove_table",
  "clean_removal_ready_crash_reconciled",
  "stored_evidence_gate_artifact_before_local_delete_table",
  "clean_ignored_material_preserved_removed_and_restored",
  "active_task_ref_consumer_refused",
  "source_replacement_after_removal_refused",
  "common_dir_replacement_after_removal_refused",
  "origin_unavailable_classified",
  "origin_replacement_after_removal_refused",
  "live_base_rewrite_refused",
  "local_delete_effect_retry_reconciled",
  "stored_evidence_gate_artifact_before_remote_delete_table",
  "force_with_lease_race_refused",
  "remote_same_sha_recreation_needs_you",
  "remote_delete_effect_retry_reconciled",
  "completed_ref_recreation_needs_you",
  "ignored_recovery_retention_cleanup_idempotent",
  "clean_lock_removal_ready_persisted",
  "removal_ready_same_metadata_edit_refused",
  CLEAN_LOCK_ASSERTION,
  "clean_lock_retry_completed",
  "ignored_material_needs_you",
  "nested_repository_and_ignored_coexistence_needs_you",
  SPECIAL_MATERIAL_ASSERTION,
  "snapshot_filter_refused_and_hooks_disabled",
  "recovery_ref_effect_crash_reconciled",
  "post_snapshot_change_refused",
  "dirty_snapshot_verified",
  "dirty_empty_directory_preserved",
  ...(platform() === "win32" ? [DIRTY_LOCK_ASSERTION] : []),
  "stored_evidence_gate_before_move_table",
  "quarantine_move_effect_crash_reconciled",
  "stored_evidence_gate_before_remove_table",
  "worktree_removal_effect_crash_reconciled",
  "stored_evidence_gate_before_local_delete_table",
  ...(platform() === "win32" ? [] : [DIRTY_LOCK_ASSERTION]),
  "stored_evidence_gate_before_remote_delete_table",
  "owned_worktree_removed",
  "exact_local_ref_removed",
  "exact_remote_ref_removed",
  "stored_evidence_gate_before_retention_remove_table",
  "recovery_ref_expiry_ordered_after_artifact",
  "stored_evidence_repair_then_resume_all_stages",
  "source_and_unknown_resources_preserved",
  "snapshot_restored_after_removal",
  "cleanup_idempotent",
  "temporary_root_removed",
];

class ExpectedInterruption extends Error {}
class NeedsYouError extends Error {}
class ExternalUnavailableError extends Error {}
class GitCommandError extends Error {
  constructor(subcommand, exitCode) {
    super(`git ${subcommand} exited ${exitCode}`);
    this.name = "GitCommandError";
    this.exitCode = exitCode;
    this.subcommand = subcommand;
  }
}

let gitConfigPath;
let disabledHooksPath;

function selectedEnvironment() {
  const keys = platform() === "win32"
    ? ["PATH", "Path", "PATHEXT", "SystemRoot", "WINDIR", "ComSpec", "TEMP", "TMP"]
    : ["PATH", "LANG", "LC_ALL", "TMPDIR"];
  const env = {};
  for (const key of keys) {
    if (process.env[key] !== undefined) env[key] = process.env[key];
  }
  env.GIT_CONFIG_NOSYSTEM = "1";
  env.GIT_TERMINAL_PROMPT = "0";
  env.GIT_ASKPASS = "";
  return env;
}

function command(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? selectedEnvironment(),
    input: options.input,
    maxBuffer: 4 * 1024 * 1024,
    shell: false,
    windowsHide: true,
  });
  if (result.error) throw result.error;
  if (options.allowFailure) return result;
  if (result.status !== 0) {
    throw new Error(`${executable} exited ${result.status}`);
  }
  return result.stdout.trim();
}

function git(cwd, args, options = {}) {
  const result = command("git", [
    "-c", `include.path=${gitConfigPath}`,
    "-c", `core.hooksPath=${disabledHooksPath}`,
    "-c", "core.fsmonitor=false",
    "-c", "core.sparseCheckout=false",
    "-c", "core.sparseCheckoutCone=false",
    "-c", "index.sparse=false",
    "-c", "credential.helper=",
    "-c", "core.pager=cat",
    "-c", "advice.detachedHead=false",
    ...args,
  ], { ...options, allowFailure: true, cwd });
  if (options.allowFailure) return result;
  if (result.status !== 0) {
    const subcommand = args[0] === "worktree" ? `${args[0]} ${args[1]}` : args[0];
    throw new GitCommandError(subcommand, result.status);
  }
  return result.stdout.trim();
}

function canonical(path) {
  return realpathSync.native(path);
}

function identity(path) {
  const details = statSync(path, { bigint: true });
  return {
    canonicalPath: canonical(path),
    dev: details.dev.toString(),
    ino: details.ino.toString(),
  };
}

function sameIdentity(expected, actual) {
  return expected.canonicalPath === actual.canonicalPath
    && expected.dev === actual.dev
    && expected.ino === actual.ino;
}

function sameFileIdentity(expected, actual) {
  return expected.dev === actual.dev && expected.ino === actual.ino;
}

function isStrictDescendant(parent, child) {
  const delta = relative(parent, child);
  return delta !== "" && delta !== ".." && !delta.startsWith(`..${sep}`) && !isAbsolute(delta);
}

function expectRefusal(assertions, name, operation, expected) {
  assert.throws(operation, expected);
  assertions.push(name);
}

function commonDir(cwd) {
  return canonical(git(cwd, ["rev-parse", "--path-format=absolute", "--git-common-dir"]));
}

function remoteFacts(cwd) {
  const url = git(cwd, ["remote", "get-url", "origin"]);
  assert(!url.includes("@"), "fixture remote must contain no credentials");
  let path;
  try {
    path = url.startsWith("file://") ? fileURLToPath(url) : resolve(cwd, url);
  } catch {
    throw new NeedsYouError("local-path origin effects require human approval and containment");
  }
  try {
    const leaf = lstatSync(path);
    assert(!leaf.isSymbolicLink(), "origin became a symlink or junction");
    return { url, identity: identity(path), transport: "path" };
  } catch (error) {
    if (["ENOENT", "EACCES", "EPERM", "ENOTDIR"].includes(error?.code)) {
      throw new ExternalUnavailableError("origin filesystem identity unavailable");
    }
    throw error;
  }
}

function assertRemoteExecutionPolicy(manifest) {
  assert.equal(manifest.remoteTransport, "path", "fixture remote transport changed");
  if (!manifest.localOriginAuthority?.humanApproved || !manifest.localOriginAuthority?.contained) {
    throw new NeedsYouError("local-path origin effects require human approval and containment");
  }
}

function validateRef(cwd, ref) {
  assert(!ref.startsWith("-"), "ref cannot be parsed as an option");
  git(cwd, ["check-ref-format", ref]);
}

function assertStateOwnership(manifest, state) {
  assert.equal(state.schemaVersion, 2, "cleanup intent schema changed");
  assert.equal(state.operationId, `cleanup-${manifest.taskId}-${manifest.runId}`, "cleanup operation identity changed");
  assert.equal(state.taskId, manifest.taskId, "cleanup Task changed");
  assert.equal(state.runId, manifest.runId, "cleanup Run changed");
  assert.equal(state.nonce, manifest.nonce, "cleanup nonce changed");
  assert.equal(state.expectedCandidate, manifest.candidateSha, "cleanup Candidate changed");
  assert.equal(state.taskRef, manifest.taskRef, "cleanup local ref changed");
  assert.equal(state.remoteTaskRef, manifest.remoteTaskRef, "cleanup remote ref changed");
  assert.equal(state.baseRef, manifest.baseRef, "cleanup base ref changed");
  assert.equal(state.recoveryRef, manifest.recoveryRef, "cleanup recovery ref changed");
  assert.equal(state.indexRecoveryRef, manifest.indexRecoveryRef, "cleanup index recovery ref changed");
  assert.deepEqual(state.localOriginAuthority, manifest.localOriginAuthority, "local-origin authority changed");
  if (state.snapshotSha !== null) assert.match(state.snapshotSha, /^[0-9a-f]{40}$/, "cleanup snapshot identity is invalid");
  if (state.indexSnapshotSha !== null) assert.match(state.indexSnapshotSha, /^[0-9a-f]{40}$/, "cleanup index snapshot identity is invalid");
  if (state.removalReady) {
    const ready = state.removalReady;
    assert.equal(ready.candidateSha, manifest.candidateSha, "removal-ready Candidate changed");
    assert.deepEqual(ready.worktreeIdentity, manifest.worktreeIdentity, "removal-ready worktree identity changed");
    assert.equal(typeof ready.clean, "boolean", "removal-ready clean classification is invalid");
    assert.match(ready.prospectiveTreeSha, /^[0-9a-f]{40}$/, "removal-ready tree identity is invalid");
    assert.match(ready.indexTreeSha, /^[0-9a-f]{40}$/, "removal-ready index identity is invalid");
    assert.equal(ready.snapshotSha, state.snapshotSha, "removal-ready worktree snapshot changed");
    assert.equal(ready.indexSnapshotSha, state.indexSnapshotSha, "removal-ready index snapshot changed");
  }
  if (state.localDelete) {
    assert(["intent_recorded", "refused_before_dispatch", "confirmed_absent"].includes(state.localDelete.status), "local deletion status changed");
    assert.equal(state.localDelete.expectedSha, manifest.candidateSha, "local deletion Candidate changed");
    assert.equal(state.localDelete.taskRef, manifest.taskRef, "local deletion ref changed");
    assert.equal(state.localDelete.nonce, manifest.nonce, "local deletion nonce changed");
  }
  if (state.remoteDelete) {
    assert(["intent_recorded", "refused_before_dispatch", "confirmed_absent"].includes(state.remoteDelete.status), "remote deletion status changed");
    assert.equal(state.remoteDelete.expectedSha, manifest.candidateSha, "remote deletion Candidate changed");
    assert.equal(state.remoteDelete.taskRef, manifest.remoteTaskRef, "remote deletion ref changed");
    assert.equal(state.remoteDelete.nonce, manifest.nonce, "remote deletion nonce changed");
  }
}

function assertRepositoryOwnership(manifest, state) {
  if (state) assertStateOwnership(manifest, state);
  assert.equal(manifest.stateDir, resolve(manifest.stateDir), "state directory lexical path changed");
  assert(sameIdentity(manifest.stateDirIdentity, identity(manifest.stateDir)), "state directory identity changed");
  assert.equal(manifest.sourcePath, resolve(manifest.sourcePath), "source lexical path changed");
  const sourceLeaf = lstatSync(manifest.sourcePath);
  assert(!sourceLeaf.isSymbolicLink(), "source became a symlink or junction");
  assert(sameIdentity(manifest.sourceIdentity, identity(manifest.sourcePath)), "source identity changed");
  assert.equal(commonDir(manifest.sourcePath), manifest.commonDirIdentity.canonicalPath, "Git common directory changed");
  assert(sameIdentity(manifest.commonDirIdentity, identity(commonDir(manifest.sourcePath))), "Git common-directory identity changed");
  const currentRemote = remoteFacts(manifest.sourcePath);
  assert.equal(currentRemote.url, manifest.remoteUrl, "origin URL changed");
  assert.equal(currentRemote.transport, manifest.remoteTransport, "origin transport changed");
  assert(sameIdentity(manifest.remoteIdentity, currentRemote.identity), "origin identity changed");
  assert.equal(typeof manifest.localOriginAuthority?.humanApproved, "boolean", "local-origin approval fact changed");
  assert.equal(typeof manifest.localOriginAuthority?.contained, "boolean", "local-origin containment fact changed");
  for (const ref of [manifest.taskRef, manifest.remoteTaskRef, manifest.baseRef, manifest.recoveryRef, manifest.indexRecoveryRef]) {
    validateRef(manifest.sourcePath, ref);
  }
}

function inspectOwnedWorktree(manifest, requestedPath, state) {
  assertRepositoryOwnership(manifest, state);
  assert(
    requestedPath === manifest.worktreePath || requestedPath === manifest.quarantinePath,
    "requested path differs from owned paths",
  );
  const leaf = lstatSync(requestedPath);
  assert(!leaf.isSymbolicLink(), "owned path became a symlink or junction");
  const current = identity(requestedPath);
  if (requestedPath === manifest.worktreePath) {
    assert(sameIdentity(manifest.worktreeIdentity, current), "owned path identity changed");
  } else {
    assert(sameFileIdentity(manifest.worktreeIdentity, current), "quarantined worktree identity changed");
  }
  assert(isStrictDescendant(manifest.worktreeRoot, current.canonicalPath), "worktree escaped its root");
  assert.notEqual(current.canonicalPath, manifest.sourceIdentity.canonicalPath, "source checkout is protected");
  assert.notEqual(current.canonicalPath, manifest.commonDirIdentity.canonicalPath, "Git common directory is protected");
  assert.equal(commonDir(requestedPath), manifest.commonDirIdentity.canonicalPath, "worktree Git common directory changed");
  const worktreeRemote = remoteFacts(requestedPath);
  assert.equal(worktreeRemote.url, manifest.remoteUrl, "worktree origin URL changed");
  assert(sameIdentity(manifest.remoteIdentity, worktreeRemote.identity), "worktree origin identity changed");
  const branch = git(requestedPath, ["symbolic-ref", "-q", "HEAD"], { allowFailure: true });
  assert.equal(branch.status, 0, "owned worktree became detached");
  assert.equal(branch.stdout.trim(), manifest.taskRef, "branch ownership changed");
  assert.equal(git(requestedPath, ["rev-parse", "HEAD"]), manifest.candidateSha, "Candidate changed");
}

function hasConfiguredValue(value) {
  if (value === undefined || value === null) return false;
  if (typeof value === "string") return value.trim().length > 0;
  if (Array.isArray(value)) return value.length > 0;
  if (typeof value === "object") return Object.keys(value).length > 0;
  return true;
}

function assertLifecycleBoundary(cwd, sha, approval) {
  const probe = git(cwd, ["show", `${sha}:paseo.json`], { allowFailure: true });
  if (probe.status !== 0) return;
  let config;
  try {
    config = JSON.parse(probe.stdout);
  } catch {
    throw new Error("repository paseo.json is not trusted executable configuration");
  }
  const worktree = config?.worktree;
  if (worktree !== undefined && (worktree === null || typeof worktree !== "object" || Array.isArray(worktree))) {
    throw new Error("repository paseo.json worktree config is invalid and cannot be treated as inert");
  }
  const automaticExecutableSurface = hasConfiguredValue(worktree?.setup)
    || hasConfiguredValue(worktree?.teardown)
    || hasConfiguredValue(worktree?.terminals)
    || hasConfiguredValue(worktree?.servicePorts?.portScript);
  if (automaticExecutableSurface && !(approval?.humanApproved && approval?.authorityContained)) {
    throw new Error("installed Paseo worktree executable surface requires human approval and OS containment");
  }
}

function nullFields(output) {
  return output.split("\0").filter((field) => field.length > 0);
}

function scanWorktreeMaterial(worktreePath, policy) {
  const digest = createHash("sha256");
  const invisiblePaths = [];
  let entryCount = 0;
  const visitDirectory = (directoryPath, relativeDirectory) => {
    const children = readdirSync(directoryPath, { withFileTypes: true })
      .sort((left, right) => left.name.localeCompare(right.name));
    if (relativeDirectory && children.length === 0) invisiblePaths.push(relativeDirectory);
    for (const child of children) {
      if (!relativeDirectory && child.name === ".git") continue;
      if (child.name === ".git") throw new NeedsYouError("nested repository or submodule requires human recovery");
      const relativePath = relativeDirectory ? `${relativeDirectory}/${child.name}` : child.name;
      const absolutePath = join(directoryPath, child.name);
      const details = lstatSync(absolutePath);
      entryCount += 1;
      if (entryCount > policy.maxScanEntries) {
        throw new NeedsYouError("worktree material exceeds the inspection budget");
      }
      let type;
      if (details.isSymbolicLink()) {
        const stage = git(worktreePath, ["ls-files", "--stage", "--", relativePath]);
        if (!stage.startsWith("120000 ")) {
          throw new NeedsYouError("symbolic link or reparse point requires human recovery");
        }
        type = "symlink";
      } else if (details.isFile()) {
        type = "file";
      } else if (details.isDirectory()) {
        type = "directory";
      } else {
        throw new NeedsYouError("FIFO, socket, or special material requires human recovery");
      }
      digest.update(JSON.stringify({
        path: relativePath.replaceAll("\\", "/"),
        type,
        size: details.size,
        mode: details.mode & 0o777,
        mtimeMs: Math.trunc(details.mtimeMs),
      }));
      if (type === "directory") visitDirectory(absolutePath, relativePath.replaceAll("\\", "/"));
    }
  };
  visitDirectory(worktreePath, "");
  return { entryCount, invisiblePaths, metadataDigest: digest.digest("hex") };
}

function configuredFilterExecutable(worktreePath, driver) {
  for (const suffix of ["clean", "process"]) {
    const configured = git(worktreePath, ["config", "--get", `filter.${driver}.${suffix}`], { allowFailure: true });
    if (configured.status === 0 && configured.stdout.trim().length > 0) return true;
    if (![0, 1].includes(configured.status)) {
      throw new ExternalUnavailableError("Git filter configuration could not be inspected");
    }
  }
  return false;
}

function inspectMaterialSafety(worktreePath, policy) {
  const scan = secretSafeFilesystemOperation(
    () => scanWorktreeMaterial(worktreePath, policy),
    "worktree material could not be inspected safely",
  );
  const ignoredOutput = git(worktreePath, [
    "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "--no-empty-directory", "-z",
  ]);
  const gitIgnoredPaths = normalizedIgnoredRoots(
    nullFields(ignoredOutput).map((path) => path.replace(/\/$/, "")),
  );
  const invisiblePaths = normalizedIgnoredRoots(scan.invisiblePaths);
  const ignoredPaths = normalizedIgnoredRoots([...gitIgnoredPaths, ...invisiblePaths]);
  const material = git(worktreePath, ["ls-files", "--cached", "--others", "--exclude-standard", "-z"]);
  if (material.length > 0) {
    const attrs = git(worktreePath, ["check-attr", "-z", "--stdin", "filter"], { input: material });
    const fields = nullFields(attrs);
    assert.equal(fields.length % 3, 0, "unexpected git check-attr output");
    for (let index = 2; index < fields.length; index += 3) {
      if (fields[index] !== "unspecified"
          && fields[index] !== "unset"
          && configuredFilterExecutable(worktreePath, fields[index])) {
        throw new NeedsYouError("active Git clean filter requires approved containment");
      }
    }
  }
  return { ignoredPaths, gitIgnoredPaths, invisiblePaths, metadataDigest: scan.metadataDigest };
}

function assertSafeRelativePath(path) {
  assert(path.length > 0, "recovery path is empty");
  assert(!isAbsolute(path), "recovery path became absolute");
  const parts = path.replaceAll("\\", "/").split("/");
  assert(!parts.includes(".."), "recovery path escapes the worktree");
  return parts;
}

function recoverySourcePath(root, relativePath) {
  return join(root, ...assertSafeRelativePath(relativePath));
}

function ioBuffer(bufferOrBytes, fileBytes = Number.MAX_SAFE_INTEGER) {
  if (Buffer.isBuffer(bufferOrBytes)) return bufferOrBytes;
  const requested = bufferOrBytes ?? DEFAULT_IGNORED_RECOVERY_POLICY.streamChunkBytes;
  return Buffer.alloc(Math.max(1, Math.min(requested, fileBytes)));
}

function sha256File(path, bufferOrBytes = DEFAULT_IGNORED_RECOVERY_POLICY.streamChunkBytes) {
  const descriptor = openSync(path, "r");
  const digest = createHash("sha256");
  const details = statSync(path);
  const buffer = ioBuffer(bufferOrBytes, details.size);
  try {
    for (;;) {
      const bytes = readSync(descriptor, buffer, 0, buffer.length, null);
      if (bytes === 0) break;
      digest.update(buffer.subarray(0, bytes));
    }
  } finally {
    closeSync(descriptor);
  }
  return digest.digest("hex");
}

function streamCopyAndHash(sourcePath, outputPath, bufferOrBytes) {
  const source = openSync(sourcePath, "r");
  let output;
  const digest = createHash("sha256");
  const buffer = ioBuffer(bufferOrBytes, statSync(sourcePath).size);
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
        if (written <= 0) throw new NeedsYouError("ignored recovery streaming copy made no progress");
        offset += written;
      }
      copiedBytes += bytes;
    }
    fsyncSync(output);
  } finally {
    closeSync(source);
    if (output !== undefined) closeSync(output);
  }
  return { copiedBytes, sha256: digest.digest("hex") };
}

function secretSafeFilesystemOperation(operation, message) {
  try {
    return operation();
  } catch (error) {
    const filesystemCodes = ["EACCES", "EBUSY", "EDQUOT", "EIO", "EISDIR", "EMFILE", "ENFILE", "ENOENT", "ENOSPC", "ENOTDIR", "EPERM", "EROFS"];
    if (error instanceof RangeError || filesystemCodes.includes(error?.code)) {
      throw new NeedsYouError(message);
    }
    throw error;
  }
}

function normalizedIgnoredRoots(paths) {
  const sorted = [...new Set(paths)].sort((left, right) => left.length - right.length || left.localeCompare(right));
  return sorted.filter((candidate, index) => !sorted.slice(0, index)
    .some((parent) => candidate === parent || candidate.startsWith(`${parent}/`)));
}

function recoveryMetadataDigest(entries) {
  const digest = createHash("sha256");
  for (const entry of entries) {
    digest.update(JSON.stringify({
      path: entry.path,
      type: entry.type,
      mode: entry.mode,
      mtimeMs: entry.mtimeMs,
      size: entry.size ?? null,
    }));
  }
  return digest.digest("hex");
}

function recoveryContentDigest(entries) {
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

function sourceRecoveryContentDigest(worktreePath, entries, chunkBytes) {
  const largestFile = entries.reduce(
    (largest, entry) => entry.type === "file" ? Math.max(largest, entry.size) : largest,
    0,
  );
  const buffer = ioBuffer(chunkBytes, largestFile);
  const enriched = entries.map((entry) => {
    if (entry.type !== "file") return entry;
    return {
      ...entry,
      sha256: sha256File(recoverySourcePath(worktreePath, entry.path), buffer),
    };
  });
  return recoveryContentDigest(enriched);
}

function enumerateIgnoredRecovery(worktreePath, ignoredPaths, policy) {
  const entries = [];
  let totalBytes = 0;
  const visit = (relativePath) => {
    const sourcePath = recoverySourcePath(worktreePath, relativePath);
    const details = lstatSync(sourcePath);
    const base = {
      path: relativePath.replaceAll("\\", "/"),
      mode: details.mode & 0o777,
      mtimeMs: Math.trunc(details.mtimeMs),
    };
    if (details.isSymbolicLink()) {
      throw new NeedsYouError("ignored symbolic link requires human recovery");
    }
    if (details.isFile()) {
      if (details.size > policy.maxFileBytes) {
        throw new NeedsYouError("ignored file exceeds the automatic preservation policy");
      }
      totalBytes += details.size;
      if (totalBytes > policy.maxBytes) {
        throw new NeedsYouError("ignored recovery exceeds the automatic byte policy");
      }
      entries.push({ ...base, type: "file", size: details.size });
      if (entries.length > policy.maxEntries) {
        throw new NeedsYouError("ignored recovery exceeds the automatic entry policy");
      }
      return;
    }
    if (!details.isDirectory()) {
      throw new NeedsYouError("unsupported ignored material requires human recovery");
    }
    entries.push({ ...base, type: "directory" });
    if (entries.length > policy.maxEntries) {
      throw new NeedsYouError("ignored recovery exceeds the automatic entry policy");
    }
    const children = readdirSync(sourcePath, { withFileTypes: true })
      .map((entry) => entry.name)
      .sort();
    for (const child of children) visit(`${base.path}/${child}`);
  };
  for (const root of normalizedIgnoredRoots(ignoredPaths)) visit(root);
  return { entries, totalBytes, metadataDigest: recoveryMetadataDigest(entries) };
}

function ignoredRecoveryReservedBytes(plan) {
  return BigInt(plan.totalBytes
    + IGNORED_RECOVERY_FIXED_OVERHEAD_BYTES
    + (plan.entries?.length ?? 0) * IGNORED_RECOVERY_ENTRY_OVERHEAD_BYTES);
}

function assertIgnoredRecoveryDiskBudget(plan, availableBytes, minimumFreeBytes, bytesStillToWrite) {
  const requiredBytes = bytesStillToWrite ?? ignoredRecoveryReservedBytes(plan);
  if (availableBytes - requiredBytes < minimumFreeBytes) {
    throw new NeedsYouError("ignored recovery cannot preserve the disk safety threshold");
  }
  return requiredBytes;
}

function restrictRecoveryDirectory(path) {
  if (platform() !== "win32") {
    chmodSync(path, 0o700);
    return "posix-owner-only";
  }
  const script = [
    "$path = $env:DIRECTOR_RECOVERY_ACL_PATH",
    "if ([string]::IsNullOrWhiteSpace($path)) { exit 19 }",
    "$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User",
    "$acl = Get-Acl -LiteralPath $path",
    "$acl.SetAccessRuleProtection($true, $false)",
    "@($acl.Access) | ForEach-Object { [void]$acl.RemoveAccessRuleSpecific($_) }",
    "$rights = [System.Security.AccessControl.FileSystemRights]::FullControl",
    "$inheritance = [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [System.Security.AccessControl.InheritanceFlags]::ObjectInherit",
    "$propagation = [System.Security.AccessControl.PropagationFlags]::None",
    "$allow = [System.Security.AccessControl.AccessControlType]::Allow",
    "$rule = [System.Security.AccessControl.FileSystemAccessRule]::new($sid, $rights, $inheritance, $propagation, $allow)",
    "$acl.AddAccessRule($rule)",
    "Set-Acl -LiteralPath $path -AclObject $acl",
  ].join("; ");
  command("powershell.exe", ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script], {
    env: { ...selectedEnvironment(), DIRECTOR_RECOVERY_ACL_PATH: path },
  });
  return "windows-owner-only-acl";
}

function verifyRecoveryPermissions(path, permissionModel) {
  if (permissionModel === "posix-owner-only") {
    assert.equal(lstatSync(path).mode & 0o777, 0o700, "recovery directory permissions changed");
    return;
  }
  assert.equal(permissionModel, "windows-owner-only-acl");
  const script = [
    "$path = $env:DIRECTOR_RECOVERY_ACL_PATH",
    "if ([string]::IsNullOrWhiteSpace($path)) { exit 19 }",
    "$acl = Get-Acl -LiteralPath $path",
    "if (-not $acl.AreAccessRulesProtected) { exit 20 }",
    "$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value",
    "$rules = @($acl.Access)",
    "if ($rules.Count -ne 1) { exit 21 }",
    "$rule = $rules[0]",
    "if ($rule.AccessControlType -ne 'Allow') { exit 22 }",
    "if ($rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value -ne $sid) { exit 23 }",
    "if (($rule.InheritanceFlags -band [System.Security.AccessControl.InheritanceFlags]::ContainerInherit) -eq 0) { exit 24 }",
    "if (($rule.InheritanceFlags -band [System.Security.AccessControl.InheritanceFlags]::ObjectInherit) -eq 0) { exit 25 }",
    "if (($rule.FileSystemRights -band [System.Security.AccessControl.FileSystemRights]::FullControl) -ne [System.Security.AccessControl.FileSystemRights]::FullControl) { exit 26 }",
    "Write-Output 'restricted'",
  ].join("; ");
  assert.equal(command("powershell.exe", ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script], {
    env: { ...selectedEnvironment(), DIRECTOR_RECOVERY_ACL_PATH: path },
  }), "restricted");
}

function writeRestrictedFile(path, content) {
  writeFileSync(path, content, { mode: 0o600 });
  if (platform() !== "win32") chmodSync(path, 0o600);
}

function assertIgnoredRecoveryScope(manifest, state) {
  const recovery = state.ignoredRecovery;
  assert(recovery, "ignored recovery intent is absent");
  assert.equal(recovery.artifactPath, manifest.ignoredArtifactPath, "ignored artifact path changed");
  assert.equal(recovery.stagingPath, manifest.ignoredStagingPath, "ignored staging path changed");
  assert.equal(manifest.ignoredRecoveryRoot, resolve(manifest.ignoredRecoveryRoot), "ignored recovery root path changed");
  if (!sameIdentity(recovery.recoveryRootIdentity, identity(manifest.ignoredRecoveryRoot))) {
    throw new NeedsYouError("ignored recovery root ownership is unverified");
  }
  try {
    verifyRecoveryPermissions(manifest.ignoredRecoveryRoot, recovery.permissionModel);
  } catch {
    throw new NeedsYouError("ignored recovery root permissions are unverified");
  }
  assert.equal(recovery.taskId, manifest.taskId, "ignored recovery Task changed");
  assert.equal(recovery.runId, manifest.runId, "ignored recovery Run changed");
  assert.equal(recovery.nonce, manifest.nonce, "ignored recovery nonce changed");
  assert.equal(recovery.candidateSha, manifest.candidateSha, "ignored recovery Candidate changed");
  for (const field of ["maxBytes", "maxEntries", "maxFileBytes", "maxScanEntries", "streamChunkBytes", "minimumFreePercent"]) {
    assert.equal(recovery[field], manifest.ignoredRecoveryPolicy[field], "ignored recovery policy changed");
  }
}

function recoveryOwnerRecord(manifest) {
  return {
    schemaVersion: 1,
    taskId: manifest.taskId,
    runId: manifest.runId,
    nonce: manifest.nonce,
    candidateSha: manifest.candidateSha,
  };
}

function verifyRecoveryOwner(path, manifest) {
  const owner = JSON.parse(readFileSync(join(path, "owner.json"), "utf8"));
  assert.deepEqual(owner, recoveryOwnerRecord(manifest), "ignored recovery owner changed");
}

function verifyIgnoredArtifactUnchecked(manifest, state) {
  assertIgnoredRecoveryScope(manifest, state);
  const recovery = state.ignoredRecovery;
  const artifactLeaf = lstatSync(recovery.artifactPath);
  assert(!artifactLeaf.isSymbolicLink(), "ignored recovery artifact became a link");
  if (recovery.artifactIdentity) {
    assert(sameIdentity(recovery.artifactIdentity, identity(recovery.artifactPath)), "ignored recovery artifact identity changed");
  }
  verifyRecoveryPermissions(recovery.artifactPath, recovery.permissionModel);
  verifyRecoveryOwner(recovery.artifactPath, manifest);
  const manifestPath = join(recovery.artifactPath, "manifest.json");
  if (platform() !== "win32") {
    assert.equal(lstatSync(join(recovery.artifactPath, "owner.json")).mode & 0o777, 0o600);
    assert.equal(lstatSync(manifestPath).mode & 0o777, 0o600);
    assert.equal(lstatSync(join(recovery.artifactPath, "payload")).mode & 0o777, 0o700);
  }
  const manifestSha256 = sha256File(manifestPath);
  if (recovery.manifestSha256) assert.equal(manifestSha256, recovery.manifestSha256, "ignored recovery manifest changed");
  const record = JSON.parse(readFileSync(manifestPath, "utf8"));
  assert.deepEqual(record.owner, recoveryOwnerRecord(manifest));
  assert.equal(record.retentionUntilMs, recovery.retentionUntilMs);
  assert.equal(record.totalBytes, recovery.totalBytes);
  assert.equal(record.entries.length, recovery.entryCount);
  const metadataDigest = recoveryMetadataDigest(record.entries);
  const contentDigest = recoveryContentDigest(record.entries);
  if (recovery.metadataDigest) assert.equal(metadataDigest, recovery.metadataDigest, "ignored recovery metadata aggregate changed");
  if (recovery.contentDigest) assert.equal(contentDigest, recovery.contentDigest, "ignored recovery content aggregate changed");
  const largestFile = record.entries.reduce(
    (largest, entry) => entry.type === "file" ? Math.max(largest, entry.size) : largest,
    0,
  );
  const verificationBuffer = ioBuffer(recovery.streamChunkBytes, largestFile);
  for (const entry of record.entries) {
    assert(["file", "directory"].includes(entry.type), "ignored recovery entry type changed");
    const payloadPath = recoverySourcePath(join(recovery.artifactPath, "payload"), entry.path);
    if (entry.type === "directory") {
      const details = lstatSync(payloadPath);
      assert(details.isDirectory(), "recovery directory is absent");
      if (platform() !== "win32") assert.equal(details.mode & 0o777, 0o700, "recovery directory permissions changed");
    } else {
      const details = lstatSync(payloadPath);
      assert(details.isFile(), "recovery file is absent");
      assert.equal(details.size, entry.size, "recovery file size changed");
      assert.equal(sha256File(payloadPath, verificationBuffer), entry.sha256, "recovery file bytes changed");
      if (platform() !== "win32") assert.equal(details.mode & 0o777, 0o600, "recovery file permissions changed");
    }
  }
  return { record, manifestSha256, metadataDigest, contentDigest };
}

function verifyIgnoredArtifact(manifest, state) {
  try {
    return verifyIgnoredArtifactUnchecked(manifest, state);
  } catch (error) {
    if (error instanceof NeedsYouError || error instanceof ExternalUnavailableError) throw error;
    throw new NeedsYouError("ignored recovery artifact ownership or content is unverified");
  }
}

function ensureIgnoredRecovery(manifest, state, worktreePath, ignoredPaths, options) {
  assertRepositoryOwnership(manifest, state);
  const plan = secretSafeFilesystemOperation(
    () => enumerateIgnoredRecovery(worktreePath, ignoredPaths, manifest.ignoredRecoveryPolicy),
    "ignored recovery material could not be inspected safely",
  );
  if (!existsSync(manifest.ignoredRecoveryRoot)) {
    mkdirSync(manifest.ignoredRecoveryRoot, { recursive: true, mode: 0o700 });
  }
  const permissionModel = state.ignoredRecovery?.permissionModel
    ?? restrictRecoveryDirectory(manifest.ignoredRecoveryRoot);
  const disk = statfsSync(manifest.ignoredRecoveryRoot, { bigint: true });
  const availableBytes = options.availableBytesOverride ?? (disk.bavail * disk.bsize);
  const minimumFreeBytes = (disk.blocks * disk.bsize * BigInt(manifest.ignoredRecoveryPolicy.minimumFreePercent)) / 100n;
  const artifactAlreadyExists = existsSync(manifest.ignoredArtifactPath);
  const totalReservedBytes = ignoredRecoveryReservedBytes(plan);
  const bytesStillToWrite = artifactAlreadyExists ? 0n : totalReservedBytes;
  assertIgnoredRecoveryDiskBudget(plan, availableBytes, minimumFreeBytes, bytesStillToWrite);
  if (!state.ignoredRecovery) {
    state.ignoredRecovery = {
      status: "intent_recorded",
      cleanupPhase: "retained",
      artifactPath: manifest.ignoredArtifactPath,
      stagingPath: manifest.ignoredStagingPath,
      taskId: manifest.taskId,
      runId: manifest.runId,
      nonce: manifest.nonce,
      candidateSha: manifest.candidateSha,
      createdAtMs: Date.now(),
      retentionUntilMs: Date.now() + manifest.ignoredRecoveryPolicy.retentionMs,
      totalBytes: plan.totalBytes,
      entryCount: plan.entries.length,
      metadataDigest: plan.metadataDigest,
      sourceContentDigest: null,
      contentDigest: null,
      availableBytesBefore: availableBytes.toString(),
      minimumFreeBytes: minimumFreeBytes.toString(),
      reservedBytes: totalReservedBytes.toString(),
      bytesStillToWrite: bytesStillToWrite.toString(),
      maxBytes: manifest.ignoredRecoveryPolicy.maxBytes,
      maxEntries: manifest.ignoredRecoveryPolicy.maxEntries,
      maxFileBytes: manifest.ignoredRecoveryPolicy.maxFileBytes,
      maxScanEntries: manifest.ignoredRecoveryPolicy.maxScanEntries,
      streamChunkBytes: manifest.ignoredRecoveryPolicy.streamChunkBytes,
      minimumFreePercent: manifest.ignoredRecoveryPolicy.minimumFreePercent,
      permissionModel,
      recoveryRootIdentity: identity(manifest.ignoredRecoveryRoot),
      manifestSha256: null,
      artifactIdentity: null,
    };
    writeState(manifest, state);
  }
  assertIgnoredRecoveryScope(manifest, state);
  const recovery = state.ignoredRecovery;
  assert.equal(recovery.totalBytes, plan.totalBytes, "ignored recovery byte count changed");
  assert.equal(recovery.entryCount, plan.entries.length, "ignored recovery entry count changed");
  if (recovery.metadataDigest !== plan.metadataDigest) {
    throw new NeedsYouError("ignored material metadata changed after preservation intent");
  }
  recovery.availableBytesBefore = availableBytes.toString();
  recovery.minimumFreeBytes = minimumFreeBytes.toString();
  recovery.reservedBytes = totalReservedBytes.toString();
  recovery.bytesStillToWrite = bytesStillToWrite.toString();
  if (existsSync(recovery.artifactPath)) {
    const sourceContentDigest = secretSafeFilesystemOperation(
      () => sourceRecoveryContentDigest(worktreePath, plan.entries, recovery.streamChunkBytes),
      "ignored recovery material could not be hashed safely",
    );
    if (recovery.sourceContentDigest && recovery.sourceContentDigest !== sourceContentDigest) {
      throw new NeedsYouError("ignored material content changed after preservation intent");
    }
    const verified = secretSafeFilesystemOperation(
      () => verifyIgnoredArtifact(manifest, state),
      "ignored recovery artifact could not be verified safely",
    );
    if (verified.metadataDigest !== plan.metadataDigest) {
      throw new NeedsYouError("ignored material changed after preservation");
    }
    if (verified.contentDigest !== sourceContentDigest) {
      throw new NeedsYouError("ignored material content differs from the recovery artifact");
    }
    if (recovery.status !== "artifact_verified") {
      recovery.status = "artifact_verified";
      recovery.sourceContentDigest = sourceContentDigest;
      recovery.manifestSha256 = verified.manifestSha256;
      recovery.artifactIdentity = identity(recovery.artifactPath);
      recovery.contentDigest = verified.contentDigest;
    }
    writeState(manifest, state);
    return;
  }
  if (existsSync(recovery.stagingPath)) {
    try {
      verifyRecoveryOwner(recovery.stagingPath, manifest);
    } catch {
      throw new NeedsYouError("partial ignored recovery ownership is ambiguous");
    }
    rmSync(recovery.stagingPath, { recursive: true });
  }
  mkdirSync(recovery.stagingPath, { mode: 0o700 });
  restrictRecoveryDirectory(recovery.stagingPath);
  writeRestrictedFile(join(recovery.stagingPath, "owner.json"), `${JSON.stringify(recoveryOwnerRecord(manifest), null, 2)}\n`);
  const payloadRoot = join(recovery.stagingPath, "payload");
  mkdirSync(payloadRoot, { mode: 0o700 });
  if (platform() !== "win32") chmodSync(payloadRoot, 0o700);
  const artifactEntries = [];
  const largestFile = plan.entries.reduce(
    (largest, entry) => entry.type === "file" ? Math.max(largest, entry.size) : largest,
    0,
  );
  const copyBuffer = ioBuffer(recovery.streamChunkBytes, largestFile);
  for (const entry of plan.entries) {
    const sourcePath = recoverySourcePath(worktreePath, entry.path);
    const payloadPath = recoverySourcePath(payloadRoot, entry.path);
    if (entry.type === "directory") {
      mkdirSync(payloadPath, { recursive: true, mode: 0o700 });
      if (platform() !== "win32") chmodSync(payloadPath, 0o700);
      artifactEntries.push(entry);
    } else if (entry.type === "file") {
      mkdirSync(resolve(payloadPath, ".."), { recursive: true, mode: 0o700 });
      const copied = secretSafeFilesystemOperation(
        () => streamCopyAndHash(sourcePath, payloadPath, copyBuffer),
        "ignored recovery material could not be copied safely",
      );
      if (copied.copiedBytes !== entry.size) {
        throw new NeedsYouError("ignored recovery material changed during streaming copy");
      }
      if (platform() !== "win32") chmodSync(payloadPath, 0o600);
      artifactEntries.push({ ...entry, sha256: copied.sha256 });
    }
  }
  const postCopyPlan = secretSafeFilesystemOperation(
    () => enumerateIgnoredRecovery(worktreePath, ignoredPaths, manifest.ignoredRecoveryPolicy),
    "ignored recovery material could not be revalidated safely",
  );
  if (postCopyPlan.metadataDigest !== plan.metadataDigest) {
    throw new NeedsYouError("ignored material metadata changed during recovery creation");
  }
  const copiedContentDigest = recoveryContentDigest(artifactEntries);
  const record = {
    schemaVersion: 1,
    owner: recoveryOwnerRecord(manifest),
    createdAtMs: recovery.createdAtMs,
    retentionUntilMs: recovery.retentionUntilMs,
    totalBytes: plan.totalBytes,
    entries: artifactEntries,
  };
  writeRestrictedFile(join(recovery.stagingPath, "manifest.json"), `${JSON.stringify(record, null, 2)}\n`);
  renameSync(recovery.stagingPath, recovery.artifactPath);
  const verified = secretSafeFilesystemOperation(
    () => verifyIgnoredArtifact(manifest, state),
    "ignored recovery artifact could not be verified safely",
  );
  if (verified.contentDigest !== copiedContentDigest) {
    throw new NeedsYouError("ignored material changed during recovery creation");
  }
  if (options.interruptAfterIgnoredArtifact) {
    throw new ExpectedInterruption("simulated crash after ignored artifact but before result persistence");
  }
  recovery.status = "artifact_verified";
  recovery.sourceContentDigest = copiedContentDigest;
  recovery.manifestSha256 = verified.manifestSha256;
  recovery.artifactIdentity = identity(recovery.artifactPath);
  recovery.contentDigest = verified.contentDigest;
  writeState(manifest, state);
}

function restoreIgnoredRecovery(manifest, state, destination) {
  secretSafeFilesystemOperation(() => {
    const { record } = verifyIgnoredArtifact(manifest, state);
    mkdirSync(destination, { mode: 0o700 });
    const directories = record.entries.filter((entry) => entry.type === "directory");
    const largestFile = record.entries.reduce(
      (largest, entry) => entry.type === "file" ? Math.max(largest, entry.size) : largest,
      0,
    );
    const restoreBuffer = ioBuffer(state.ignoredRecovery.streamChunkBytes, largestFile);
    for (const entry of directories) mkdirSync(recoverySourcePath(destination, entry.path), { recursive: true });
    for (const entry of record.entries) {
      const outputPath = recoverySourcePath(destination, entry.path);
      if (entry.type === "file") {
        mkdirSync(resolve(outputPath, ".."), { recursive: true });
        const copied = streamCopyAndHash(
          recoverySourcePath(join(state.ignoredRecovery.artifactPath, "payload"), entry.path),
          outputPath,
          restoreBuffer,
        );
        if (copied.copiedBytes !== entry.size || copied.sha256 !== entry.sha256) {
          throw new NeedsYouError("ignored recovery restoration bytes are unverified");
        }
        chmodSync(outputPath, entry.mode);
        utimesSync(outputPath, new Date(entry.mtimeMs), new Date(entry.mtimeMs));
      }
    }
    for (const entry of [...directories].sort((left, right) => right.path.length - left.path.length)) {
      const outputPath = recoverySourcePath(destination, entry.path);
      chmodSync(outputPath, entry.mode);
      utimesSync(outputPath, new Date(entry.mtimeMs), new Date(entry.mtimeMs));
    }
  }, "ignored recovery could not be restored safely");
}

function cleanupIgnoredRecovery(manifest, state, nowMs, options = {}) {
  assertRepositoryOwnership(manifest, state);
  if (state.phase !== "complete") throw new NeedsYouError("ignored recovery is still required by active cleanup");
  assertIgnoredRecoveryScope(manifest, state);
  const recovery = state.ignoredRecovery;
  if (existsSync(recovery.artifactPath)) {
    secretSafeFilesystemOperation(
      () => verifyIgnoredArtifact(manifest, state),
      "ignored recovery artifact could not be verified safely",
    );
    if (nowMs < recovery.retentionUntilMs) {
      return { status: "retained", retentionUntilMs: recovery.retentionUntilMs };
    }
    recovery.cleanupPhase = "removal_ready";
    writeState(manifest, state);
    const pass = beginDestructivePass();
    const removeEffect = {
      kind: "ignored_artifact_remove",
      activePath: null,
      target: recovery.artifactPath,
    };
    const removeToken = issueDestructiveGateToken(manifest, state, pass, removeEffect);
    runGuardedArtifactRemoval(pass, removeToken, recovery.artifactPath);
    if (options.interruptAfterArtifactRemoval) {
      throw new ExpectedInterruption("simulated crash after ignored artifact removal before result persistence");
    }
  } else if (!['removal_ready', 'removed'].includes(recovery.cleanupPhase)) {
    throw new NeedsYouError("ignored recovery artifact disappeared unexpectedly");
  }
  recovery.cleanupPhase = "removed";
  writeState(manifest, state);
  return { status: "removed" };
}

function assertRecoveryRefsMayExpire(manifest, state) {
  assertRepositoryOwnership(manifest, state);
  if (state.phase !== "complete") {
    throw new NeedsYouError("Git recovery refs cannot expire before cleanup completes");
  }
  if (state.ignoredRecovery && state.ignoredRecovery.cleanupPhase !== "removed") {
    throw new NeedsYouError("private recovery artifact must expire before Git recovery refs");
  }
  return { status: "recovery_refs_may_expire" };
}

function observeLocalRef(manifest, state, ref) {
  assertRepositoryOwnership(manifest, state);
  validateRef(manifest.sourcePath, ref);
  const exists = git(manifest.sourcePath, ["show-ref", "--exists", ref], { allowFailure: true });
  if (exists.status === 2) return { kind: "absent" };
  if (exists.status !== 0) throw new ExternalUnavailableError("local ref observation failed");
  const observed = git(manifest.sourcePath, ["show-ref", "--verify", "--hash", ref]);
  return { kind: "present", sha: observed };
}

function classifyLsRemote(result, expectedRef) {
  if (result.status === 2) return { kind: "absent" };
  if (result.status !== 0) throw new ExternalUnavailableError("remote ref observation unavailable");
  const lines = result.stdout.trim().split(/\r?\n/).filter(Boolean);
  assert.equal(lines.length, 1, "remote ref observation was ambiguous");
  const [sha, ref] = lines[0].split(/\s+/);
  assert.equal(ref, expectedRef, "remote returned a different ref");
  assert.match(sha, /^[0-9a-f]{40}$/);
  return { kind: "present", sha };
}

function queryRemoteRef(cwd, remote, ref) {
  const result = git(cwd, ["ls-remote", "--exit-code", remote, ref], { allowFailure: true });
  return classifyLsRemote(result, ref);
}

function queryOwnedRemoteRef(manifest, state, ref) {
  assertRepositoryOwnership(manifest, state);
  assertRemoteExecutionPolicy(manifest);
  return queryRemoteRef(manifest.sourcePath, "origin", ref);
}

function verifyLiveIntegration(manifest, state) {
  assertRepositoryOwnership(manifest, state);
  assertRemoteExecutionPolicy(manifest);
  const live = queryOwnedRemoteRef(manifest, state, manifest.baseRef);
  if (live.kind !== "present") throw new ExternalUnavailableError("live base is absent");
  assertRepositoryOwnership(manifest, state);
  git(manifest.sourcePath, ["fetch", "--no-tags", "--no-write-fetch-head", "origin", live.sha]);
  assertRepositoryOwnership(manifest, state);
  const commit = git(manifest.sourcePath, ["cat-file", "-e", `${live.sha}^{commit}`], { allowFailure: true });
  if (commit.status !== 0) throw new ExternalUnavailableError("live base commit is unavailable locally");
  const ancestor = git(manifest.sourcePath, ["merge-base", "--is-ancestor", manifest.candidateSha, live.sha], { allowFailure: true });
  if (ancestor.status === 1) throw new NeedsYouError("Candidate is not integrated in the live base");
  if (ancestor.status !== 0) throw new ExternalUnavailableError("live base ancestry could not be verified");
  state.lastVerifiedLiveBase = live.sha;
  writeState(manifest, state);
  return live.sha;
}

function writeState(manifest, state) {
  assert(sameIdentity(manifest.stateDirIdentity, identity(manifest.stateDir)), "state directory identity changed");
  const serialized = `${JSON.stringify(state, null, 2)}\n`;
  writeRestrictedFile(manifest.intentTempPath, serialized);
  fsyncStateFile(manifest.intentTempPath);
  renameSync(manifest.intentTempPath, manifest.intentPath);
  if (platform() !== "win32") {
    const directory = openSync(manifest.stateDir, "r");
    try {
      fsyncSync(directory);
    } finally {
      closeSync(directory);
    }
  }
}

function fsyncStateFile(path, hostPlatform = platform(), operations = { openSync, fsyncSync, closeSync }) {
  const descriptor = operations.openSync(path, hostPlatform === "win32" ? "r+" : "r");
  try {
    operations.fsyncSync(descriptor);
  } finally {
    operations.closeSync(descriptor);
  }
}

function verifyStateFileFsyncContract() {
  for (const [hostPlatform, expectedFlag] of [["win32", "r+"], ["linux", "r"]]) {
    const calls = [];
    fsyncStateFile("cleanup-intent.next", hostPlatform, {
      openSync: (path, flag) => {
        calls.push(["open", path, flag]);
        return 17;
      },
      fsyncSync: (descriptor) => calls.push(["fsync", descriptor]),
      closeSync: (descriptor) => calls.push(["close", descriptor]),
    });
    assert.deepEqual(calls, [
      ["open", "cleanup-intent.next", expectedFlag],
      ["fsync", 17],
      ["close", 17],
    ]);
  }
  const fsyncFailure = Object.assign(new Error("simulated fsync failure"), { code: "EPERM" });
  let closedAfterFsyncFailure = false;
  assert.throws(
    () => fsyncStateFile("cleanup-intent.next", "win32", {
      openSync: (_path, flag) => {
        assert.equal(flag, "r+");
        return 19;
      },
      fsyncSync: () => { throw fsyncFailure; },
      closeSync: (descriptor) => {
        assert.equal(descriptor, 19);
        closedAfterFsyncFailure = true;
      },
    }),
    (error) => error === fsyncFailure,
  );
  assert(closedAfterFsyncFailure);
  return "state_file_fsync_platform_access_and_failure";
}

function readState(manifest) {
  try {
    return JSON.parse(readFileSync(manifest.intentPath, "utf8"));
  } catch {
    throw new NeedsYouError("cleanup intent is unavailable or invalid");
  }
}

function prospectiveTree(manifest, state, worktreePath) {
  assertRepositoryOwnership(manifest, state);
  inspectOwnedWorktree(manifest, worktreePath, state);
  const indexPath = join(manifest.stateDir, "prospective.index");
  rmSync(indexPath, { force: true });
  try {
    const env = { ...selectedEnvironment(), GIT_INDEX_FILE: indexPath };
    git(worktreePath, ["read-tree", manifest.candidateSha], { env });
    git(worktreePath, ["add", "-A", "--", "."], { env });
    return git(worktreePath, ["write-tree"], { env });
  } finally {
    rmSync(indexPath, { force: true });
  }
}

function currentIndexTree(manifest, state, worktreePath) {
  assertRepositoryOwnership(manifest, state);
  inspectOwnedWorktree(manifest, worktreePath, state);
  return git(worktreePath, ["write-tree"]);
}

function treeEntries(cwd, treeish) {
  const result = new Map();
  const output = git(cwd, ["ls-tree", "-r", "-z", "--full-tree", treeish]);
  for (const record of nullFields(output)) {
    const tab = record.indexOf("\t");
    assert(tab > 0, "unexpected tree record");
    const [mode, type, sha] = record.slice(0, tab).split(" ");
    result.set(record.slice(tab + 1), { mode, type, sha });
  }
  return result;
}

function assertByteExactTree(worktreePath, treeish) {
  const expected = treeEntries(worktreePath, treeish);
  const material = nullFields(git(worktreePath, ["ls-files", "--cached", "--others", "--exclude-standard", "-z"]));
  for (const path of material) {
    const absolute = recoverySourcePath(worktreePath, path);
    let details;
    try {
      details = lstatSync(absolute);
    } catch (error) {
      if (error?.code === "ENOENT") continue;
      throw error;
    }
    if (!details.isFile() && !details.isSymbolicLink()) continue;
    const entry = expected.get(path);
    if (!entry || entry.type !== "blob") {
      throw new NeedsYouError("Git recovery tree cannot represent worktree bytes exactly");
    }
    const rawSha = details.isSymbolicLink()
      ? git(worktreePath, ["hash-object", "--stdin"], { input: readlinkSync(absolute) })
      : git(worktreePath, ["hash-object", "--no-filters", "--", path]);
    if (details.isSymbolicLink() && entry.mode !== "120000") {
      throw new NeedsYouError("Git recovery tree cannot represent worktree bytes exactly");
    }
    if (rawSha !== entry.sha) {
      throw new NeedsYouError("Git normalization prevents byte-exact recovery");
    }
  }
}

function assertIndexFlagsRecoverable(worktreePath) {
  const records = nullFields(git(worktreePath, ["ls-files", "-v", "-z"]));
  for (const record of records) {
    if (record.length < 3 || record[1] !== " ") continue;
    const flag = record[0];
    const path = record.slice(2);
    if (flag === "S" && !existsSync(recoverySourcePath(worktreePath, path))) {
      throw new NeedsYouError("sparse or skip-worktree state omits tracked material");
    }
  }
}

function createSnapshot(manifest, state, worktreePath, tree, options = {}) {
  assertRepositoryOwnership(manifest, state);
  inspectOwnedWorktree(manifest, worktreePath, state);
  const safety = inspectMaterialSafety(worktreePath, manifest.ignoredRecoveryPolicy);
  if (safety.gitIgnoredPaths.length > 0) {
    throw new NeedsYouError("ignored material cannot enter a Git recovery snapshot");
  }
  if (state.snapshotSha) {
      const observed = observeLocalRef(manifest, state, state.recoveryRef);
      assert.equal(observed.kind, "present", "recovery ref disappeared");
      assert.equal(observed.sha, state.snapshotSha, "recovery ref changed");
      if (git(manifest.sourcePath, ["rev-parse", `${state.snapshotSha}^{tree}`]) !== tree) {
        throw new NeedsYouError("worktree changed after recovery snapshot");
      }
      assert.equal(git(manifest.sourcePath, ["rev-parse", `${state.snapshotSha}^`]), manifest.candidateSha, "recovery parent changed");
      assertByteExactTree(worktreePath, state.snapshotSha);
    return state.snapshotSha;
  }
  const existing = observeLocalRef(manifest, state, state.recoveryRef);
  if (existing.kind === "present") {
      const snapshotSha = existing.sha;
      if (git(manifest.sourcePath, ["rev-parse", `${snapshotSha}^{tree}`]) !== tree) {
        throw new NeedsYouError("worktree changed after recovery snapshot");
      }
      assert.equal(git(manifest.sourcePath, ["rev-parse", `${snapshotSha}^`]), manifest.candidateSha, "existing recovery parent changed");
      const body = git(manifest.sourcePath, ["show", "-s", "--format=%B", snapshotSha]);
      assert(body.includes(`Task: ${manifest.taskId}`), "existing recovery Task provenance changed");
      assert(body.includes(`Run: ${manifest.runId}`), "existing recovery Run provenance changed");
      assert(body.includes(`Ownership-Nonce: ${manifest.nonce}`), "existing recovery nonce changed");
      assertByteExactTree(worktreePath, snapshotSha);
      state.snapshotSha = snapshotSha;
      state.phase = "snapshot_verified";
      writeState(manifest, state);
    return snapshotSha;
  }
  const message = [
      "Director dirty-work recovery snapshot",
      "",
      `Task: ${manifest.taskId}`,
      `Run: ${manifest.runId}`,
      `Candidate: ${manifest.candidateSha}`,
      `Ownership-Nonce: ${manifest.nonce}`,
      "",
  ].join("\n");
  const snapshotSha = git(manifest.sourcePath, [
      "-c", "user.name=Director Recovery",
      "-c", "user.email=director-recovery.invalid",
      "commit-tree", tree, "-p", manifest.candidateSha,
  ], { input: message });
  assertRepositoryOwnership(manifest, state);
  assert.equal(observeLocalRef(manifest, state, state.recoveryRef).kind, "absent");
  git(manifest.sourcePath, ["update-ref", state.recoveryRef, snapshotSha, ZERO_SHA]);
  const created = observeLocalRef(manifest, state, state.recoveryRef);
  assert.equal(created.kind, "present");
  assert.equal(created.sha, snapshotSha);
  assertByteExactTree(worktreePath, snapshotSha);
  if (options.interruptAfterRecoveryRef) {
    throw new ExpectedInterruption("simulated crash after recovery ref but before state update");
  }
  state.snapshotSha = snapshotSha;
  state.phase = "snapshot_verified";
  writeState(manifest, state);
  return snapshotSha;
}

function createIndexSnapshot(manifest, state, indexTree, options = {}) {
  assertRepositoryOwnership(manifest, state);
  const verifyCommit = (snapshotSha) => {
    if (git(manifest.sourcePath, ["rev-parse", `${snapshotSha}^{tree}`]) !== indexTree) {
      throw new NeedsYouError("index changed after recovery snapshot");
    }
    assert.equal(git(manifest.sourcePath, ["rev-parse", `${snapshotSha}^`]), manifest.candidateSha, "index recovery parent changed");
    const body = git(manifest.sourcePath, ["show", "-s", "--format=%B", snapshotSha]);
    assert(body.includes(`Task: ${manifest.taskId}`), "index recovery Task provenance changed");
    assert(body.includes(`Run: ${manifest.runId}`), "index recovery Run provenance changed");
    assert(body.includes(`Ownership-Nonce: ${manifest.nonce}`), "index recovery nonce changed");
  };
  if (state.indexSnapshotSha) {
    const observed = observeLocalRef(manifest, state, state.indexRecoveryRef);
    assert.equal(observed.kind, "present", "index recovery ref disappeared");
    assert.equal(observed.sha, state.indexSnapshotSha, "index recovery ref changed");
    verifyCommit(state.indexSnapshotSha);
    return state.indexSnapshotSha;
  }
  const existing = observeLocalRef(manifest, state, state.indexRecoveryRef);
  if (existing.kind === "present") {
    verifyCommit(existing.sha);
    state.indexSnapshotSha = existing.sha;
    state.phase = "snapshot_verified";
    writeState(manifest, state);
    return existing.sha;
  }
  const message = [
    "Director staged-index recovery snapshot",
    "",
    `Task: ${manifest.taskId}`,
    `Run: ${manifest.runId}`,
    `Candidate: ${manifest.candidateSha}`,
    `Ownership-Nonce: ${manifest.nonce}`,
    "",
  ].join("\n");
  const snapshotSha = git(manifest.sourcePath, [
    "-c", "user.name=Director Recovery",
    "-c", "user.email=director-recovery.invalid",
    "commit-tree", indexTree, "-p", manifest.candidateSha,
  ], { input: message });
  assertRepositoryOwnership(manifest, state);
  assert.equal(observeLocalRef(manifest, state, state.indexRecoveryRef).kind, "absent");
  git(manifest.sourcePath, ["update-ref", state.indexRecoveryRef, snapshotSha, ZERO_SHA]);
  const created = observeLocalRef(manifest, state, state.indexRecoveryRef);
  assert.equal(created.kind, "present");
  assert.equal(created.sha, snapshotSha);
  verifyCommit(snapshotSha);
  if (options.interruptAfterIndexRecoveryRef) {
    throw new ExpectedInterruption("simulated crash after index recovery ref but before state update");
  }
  state.indexSnapshotSha = snapshotSha;
  state.phase = "snapshot_verified";
  writeState(manifest, state);
  return snapshotSha;
}

function snapshotContains(manifest, snapshotSha, path) {
  return git(manifest.sourcePath, ["cat-file", "-e", `${snapshotSha}:${path}`], { allowFailure: true }).status === 0;
}

function comparablePath(path) {
  const resolved = resolve(path);
  const expanded = existsSync(resolved) ? canonical(resolved) : resolved;
  const separatorsNormalized = expanded.replaceAll("\\", "/").replace(/\/+$/, "");
  return platform() === "win32" ? separatorsNormalized.toLowerCase() : separatorsNormalized;
}

function worktreeRegistrations(manifest, state) {
  assertRepositoryOwnership(manifest, state);
  const output = git(manifest.sourcePath, ["worktree", "list", "--porcelain", "-z"]);
  const registrations = [];
  let current = null;
  for (const field of output.split("\0")) {
    if (field.startsWith("worktree ")) {
      if (current) registrations.push(current);
      const path = field.slice("worktree ".length);
      current = { path, comparablePath: comparablePath(path), branch: null, head: null };
    } else if (current && field.startsWith("HEAD ")) {
      current.head = field.slice("HEAD ".length);
    } else if (current && field.startsWith("branch ")) {
      current.branch = field.slice("branch ".length);
    }
  }
  if (current) registrations.push(current);
  return registrations;
}

function assertOwnedRegistration(manifest, state, path) {
  const expectedPath = path === manifest.worktreePath
    ? comparablePath(manifest.worktreeIdentity.canonicalPath)
    : comparablePath(path);
  const matches = worktreeRegistrations(manifest, state)
    .filter((registration) => registration.comparablePath === expectedPath);
  assert.equal(matches.length, 1, "owned worktree registration identity mismatch");
  assert.equal(matches[0].branch, manifest.taskRef, "owned worktree registration branch changed");
  assert.equal(matches[0].head, manifest.candidateSha, "owned worktree registration HEAD changed");
  return matches[0];
}

function assertOwnedRegistrationsAbsent(manifest, state) {
  const ownedKeys = new Set([
    comparablePath(manifest.worktreeIdentity.canonicalPath),
    comparablePath(manifest.quarantinePath),
  ]);
  const matches = worktreeRegistrations(manifest, state)
    .filter((registration) => ownedKeys.has(registration.comparablePath)
      || (!existsSync(registration.path) && registration.branch === manifest.taskRef));
  assert.equal(matches.length, 0, "owned worktree registration remains after removal");
}

function assertNoOtherTaskRefConsumer(manifest, state) {
  const ownedKeys = new Set([
    comparablePath(manifest.worktreeIdentity.canonicalPath),
    comparablePath(manifest.quarantinePath),
  ]);
  const consumers = worktreeRegistrations(manifest, state)
    .filter((registration) => registration.branch === manifest.taskRef)
    .filter((registration) => !ownedKeys.has(registration.comparablePath));
  if (consumers.length > 0) {
    throw new NeedsYouError("Task ref has another registered worktree consumer");
  }
}

function persistRemovalReady(manifest, state, clean, prospectiveTreeSha, indexTreeSha, materialMetadataDigest) {
  const evidence = {
    candidateSha: manifest.candidateSha,
    worktreeIdentity: manifest.worktreeIdentity,
    clean,
    prospectiveTreeSha,
    indexTreeSha,
    materialMetadataDigest,
    snapshotSha: state.snapshotSha,
    indexSnapshotSha: state.indexSnapshotSha,
    ignoredRecoveryManifestSha256: state.ignoredRecovery?.manifestSha256 ?? null,
  };
  if (state.removalReady) assert.deepEqual(state.removalReady, evidence, "removal-ready evidence changed");
  state.removalReady = evidence;
  state.phase = "removal_ready";
  writeState(manifest, state);
}

function storedEvidenceFailure() {
  throw new NeedsYouError("pre-destructive recovery evidence is unverified");
}

function verifyBoundRecoveryRef(manifest, state, ref, expectedSha, expectedTree, label) {
  if (!expectedSha) return;
  const observed = observeLocalRef(manifest, state, ref);
  if (observed.kind !== "present" || observed.sha !== expectedSha) storedEvidenceFailure();
  const tree = git(manifest.sourcePath, ["rev-parse", `${expectedSha}^{tree}`], { allowFailure: true });
  const parent = git(manifest.sourcePath, ["rev-parse", `${expectedSha}^`], { allowFailure: true });
  const body = git(manifest.sourcePath, ["show", "-s", "--format=%B", expectedSha], { allowFailure: true });
  if (tree.status !== 0 || tree.stdout.trim() !== expectedTree
      || parent.status !== 0 || parent.stdout.trim() !== manifest.candidateSha
      || body.status !== 0) {
    storedEvidenceFailure();
  }
  const provenance = body.stdout.split(/\r?\n/);
  for (const expected of [
    `Task: ${manifest.taskId}`,
    `Run: ${manifest.runId}`,
    `Candidate: ${manifest.candidateSha}`,
    `Ownership-Nonce: ${manifest.nonce}`,
  ]) {
    if (!provenance.includes(expected)) storedEvidenceFailure();
  }
  assert.equal(label === "worktree" || label === "index", true, "unknown recovery evidence kind");
}

function verifyBoundIgnoredRecovery(manifest, state, activePath, safety) {
  const recovery = state.ignoredRecovery;
  const readyManifest = state.removalReady.ignoredRecoveryManifestSha256;
  if (!recovery) {
    if (readyManifest !== null || (safety?.ignoredPaths.length ?? 0) > 0) storedEvidenceFailure();
    return;
  }
  if (recovery.status !== "artifact_verified"
      || recovery.manifestSha256 !== readyManifest
      || !recovery.artifactIdentity) {
    storedEvidenceFailure();
  }
  const verified = verifyIgnoredArtifact(manifest, state);
  if (verified.manifestSha256 !== readyManifest
      || verified.metadataDigest !== recovery.metadataDigest
      || verified.contentDigest !== recovery.contentDigest) {
    storedEvidenceFailure();
  }
  if (!activePath) return;
  if (!safety || safety.ignoredPaths.length === 0) storedEvidenceFailure();
  const plan = secretSafeFilesystemOperation(
    () => enumerateIgnoredRecovery(activePath, safety.ignoredPaths, manifest.ignoredRecoveryPolicy),
    "pre-destructive recovery evidence is unverified",
  );
  if (plan.totalBytes !== recovery.totalBytes
      || plan.entries.length !== recovery.entryCount
      || plan.metadataDigest !== recovery.metadataDigest) {
    storedEvidenceFailure();
  }
  const sourceContentDigest = secretSafeFilesystemOperation(
    () => sourceRecoveryContentDigest(activePath, plan.entries, recovery.streamChunkBytes),
    "pre-destructive recovery evidence is unverified",
  );
  if (sourceContentDigest !== recovery.sourceContentDigest
      || sourceContentDigest !== verified.contentDigest) {
    storedEvidenceFailure();
  }
  const disk = statfsSync(manifest.ignoredRecoveryRoot, { bigint: true });
  const availableBytes = disk.bavail * disk.bsize;
  const minimumFreeBytes = (disk.blocks * disk.bsize * BigInt(recovery.minimumFreePercent)) / 100n;
  assertIgnoredRecoveryDiskBudget(plan, availableBytes, minimumFreeBytes, 0n);
}

function verifyPreDestructiveGate(manifest, state, effect) {
  try {
    assertRepositoryOwnership(manifest, state);
    if (!state.removalReady) storedEvidenceFailure();
    const ready = state.removalReady;
    if (ready.candidateSha !== manifest.candidateSha
        || ready.snapshotSha !== state.snapshotSha
        || ready.indexSnapshotSha !== state.indexSnapshotSha) {
      storedEvidenceFailure();
    }
    const candidateTree = git(manifest.sourcePath, ["rev-parse", `${manifest.candidateSha}^{tree}`]);
    if ((ready.prospectiveTreeSha !== candidateTree) !== Boolean(state.snapshotSha)
        || (ready.indexTreeSha !== candidateTree) !== Boolean(state.indexSnapshotSha)) {
      storedEvidenceFailure();
    }
    let safety = null;
    if (effect.activePath) {
      inspectOwnedWorktree(manifest, effect.activePath, state);
      assertOwnedRegistration(manifest, state, effect.activePath);
      safety = inspectMaterialSafety(effect.activePath, manifest.ignoredRecoveryPolicy);
      assertIndexFlagsRecoverable(effect.activePath);
      if (safety.metadataDigest !== ready.materialMetadataDigest) storedEvidenceFailure();
      const indexTree = currentIndexTree(manifest, state, effect.activePath);
      const currentTree = prospectiveTree(manifest, state, effect.activePath);
      if (indexTree !== ready.indexTreeSha || currentTree !== ready.prospectiveTreeSha) storedEvidenceFailure();
      assertByteExactTree(effect.activePath, currentTree);
      if (ready.clean !== (currentTree === candidateTree && indexTree === candidateTree)) storedEvidenceFailure();
    } else {
      assertOwnedRegistrationsAbsent(manifest, state);
    }
    verifyBoundRecoveryRef(
      manifest, state, state.recoveryRef, state.snapshotSha,
      ready.prospectiveTreeSha, "worktree",
    );
    verifyBoundRecoveryRef(
      manifest, state, state.indexRecoveryRef, state.indexSnapshotSha,
      ready.indexTreeSha, "index",
    );
    verifyBoundIgnoredRecovery(manifest, state, effect.activePath, safety);
    if (["worktree_move", "worktree_remove"].includes(effect.kind)) {
      if (!effect.activePath) storedEvidenceFailure();
      if (effect.kind === "worktree_move" && effect.activePath !== manifest.worktreePath) storedEvidenceFailure();
      if (ready.clean) verifyLiveIntegration(manifest, state);
    } else if (effect.kind === "local_ref_delete") {
      if (effect.activePath || state.localDelete?.status !== "intent_recorded") storedEvidenceFailure();
      verifyLiveIntegration(manifest, state);
      assertNoOtherTaskRefConsumer(manifest, state);
      const observed = observeLocalRef(manifest, state, manifest.taskRef);
      if (effect.ref !== manifest.taskRef || effect.expectedOid !== manifest.candidateSha
          || observed.kind !== "present" || observed.sha !== effect.expectedOid) {
        storedEvidenceFailure();
      }
    } else if (effect.kind === "remote_ref_delete") {
      if (effect.activePath || state.remoteDelete?.status !== "intent_recorded") storedEvidenceFailure();
      verifyLiveIntegration(manifest, state);
      const observed = queryOwnedRemoteRef(manifest, state, manifest.remoteTaskRef);
      if (effect.ref !== manifest.remoteTaskRef || effect.expectedOid !== manifest.candidateSha
          || observed.kind !== "present" || observed.sha !== effect.expectedOid) {
        storedEvidenceFailure();
      }
    } else if (effect.kind === "ignored_artifact_remove") {
      if (effect.activePath || state.phase !== "complete"
          || effect.target !== state.ignoredRecovery?.artifactPath) {
        storedEvidenceFailure();
      }
      assertCompletedCleanupTerminal(manifest, state);
    } else {
      storedEvidenceFailure();
    }
  } catch (error) {
    if (error instanceof NeedsYouError
        || error instanceof ExternalUnavailableError
        || error instanceof GitCommandError) {
      throw error;
    }
    throw new NeedsYouError("pre-destructive ownership or recovery evidence is unverified");
  }
}

function beginDestructivePass() {
  return { id: Symbol("destructive-pass"), nextSequence: 1, latestSequence: null };
}

function expectedDestructiveGitArgs(manifest, effect) {
  if (effect.kind === "worktree_move") {
    return ["worktree", "move", "--", manifest.worktreePath, manifest.quarantinePath];
  }
  if (effect.kind === "worktree_remove") {
    return ["worktree", "remove", "--force", "--", effect.activePath];
  }
  if (effect.kind === "local_ref_delete") {
    return ["update-ref", "-d", manifest.taskRef, manifest.candidateSha];
  }
  if (effect.kind === "remote_ref_delete") {
    return [
      "push", `--force-with-lease=${manifest.remoteTaskRef}:${manifest.candidateSha}`,
      "origin", `:${manifest.remoteTaskRef}`,
    ];
  }
  return null;
}

function issueDestructiveGateToken(manifest, state, pass, effect) {
  assert(pass?.id, "destructive pass is absent");
  verifyPreDestructiveGate(manifest, state, effect);
  const sequence = pass.nextSequence++;
  pass.latestSequence = sequence;
  return {
    passId: pass.id,
    sequence,
    effect,
    expectedCwd: manifest.sourcePath,
    expectedArgs: expectedDestructiveGitArgs(manifest, effect),
    consumed: false,
  };
}

function consumeDestructiveGateToken(pass, token, effectKind) {
  if (!token || token.passId !== pass?.id || token.consumed
      || token.sequence !== pass.latestSequence || token.effect.kind !== effectKind) {
    throw new NeedsYouError("destructive command lacks a fresh recovery-evidence gate");
  }
  token.consumed = true;
  pass.latestSequence = null;
}

function runGuardedDestructiveGit(pass, token, cwd, args, options = {}) {
  consumeDestructiveGateToken(pass, token, token?.effect?.kind);
  if (cwd !== token.expectedCwd || !token.expectedArgs
      || JSON.stringify(args) !== JSON.stringify(token.expectedArgs)) {
    throw new NeedsYouError("destructive command differs from its recovery-evidence gate");
  }
  return git(cwd, args, options);
}

function runGuardedArtifactRemoval(pass, token, path) {
  consumeDestructiveGateToken(pass, token, "ignored_artifact_remove");
  assert.equal(path, token.effect.target, "guarded artifact target changed");
  rmSync(path, { recursive: true });
}

function reconcileWorktreeRemoval(manifest, state, options, pass = beginDestructivePass()) {
  const originalExists = existsSync(manifest.worktreePath);
  const quarantineExists = existsSync(manifest.quarantinePath);
  if (originalExists && quarantineExists) {
    throw new NeedsYouError("quarantine path ownership is ambiguous");
  }
  let activePath = originalExists ? manifest.worktreePath : quarantineExists ? manifest.quarantinePath : null;
  if (activePath) {
    if (state.phase !== "removal_ready") {
      if (!["intent_recorded", "snapshot_verified"].includes(state.phase) || state.removalReady) {
        throw new NeedsYouError("cleanup phase conflicts with a present worktree");
      }
      inspectOwnedWorktree(manifest, activePath, state);
      assertOwnedRegistration(manifest, state, activePath);
      const safety = inspectMaterialSafety(activePath, manifest.ignoredRecoveryPolicy);
      assertIndexFlagsRecoverable(activePath);
      const candidateTree = git(manifest.sourcePath, ["rev-parse", `${manifest.candidateSha}^{tree}`]);
      const indexTree = currentIndexTree(manifest, state, activePath);
      const currentTree = prospectiveTree(manifest, state, activePath);
      const worktreeClean = currentTree === candidateTree;
      const indexDirty = indexTree !== candidateTree;
      const clean = worktreeClean && !indexDirty;
      assertByteExactTree(activePath, currentTree);
      if (safety.gitIgnoredPaths.length > 0 && !clean) {
        throw new NeedsYouError("ignored material with other dirty work requires human recovery");
      }
      if (safety.ignoredPaths.length > 0) {
        if (clean) verifyLiveIntegration(manifest, state);
        ensureIgnoredRecovery(manifest, state, activePath, safety.ignoredPaths, options);
      }
      if (!worktreeClean) createSnapshot(manifest, state, activePath, currentTree, options);
      else if (state.snapshotSha) createSnapshot(manifest, state, activePath, currentTree, options);
      if (indexDirty) createIndexSnapshot(manifest, state, indexTree, options);
      else if (state.indexSnapshotSha) createIndexSnapshot(manifest, state, indexTree, options);
      if (options.interruptAfterSnapshot) throw new ExpectedInterruption("simulated crash after durable snapshot");
      if (clean) verifyLiveIntegration(manifest, state);
      persistRemovalReady(manifest, state, clean, currentTree, indexTree, safety.metadataDigest);
    } else if (!state.removalReady) {
      storedEvidenceFailure();
    }
    if (options.interruptAfterRemovalReady) {
      throw new ExpectedInterruption("simulated crash after removal-ready persistence");
    }
    if (activePath === manifest.worktreePath) {
      const moveEffect = { kind: "worktree_move", activePath };
      const moveToken = issueDestructiveGateToken(manifest, state, pass, moveEffect);
      if (options.beforeWorktreeMove) options.beforeWorktreeMove();
      runGuardedDestructiveGit(
        pass, moveToken, manifest.sourcePath,
        ["worktree", "move", "--", manifest.worktreePath, manifest.quarantinePath],
      );
      activePath = manifest.quarantinePath;
      assertOwnedRegistration(manifest, state, activePath);
      if (options.interruptAfterQuarantineMove) {
        throw new ExpectedInterruption("simulated crash after quarantine move");
      }
    }
    const removeEffect = { kind: "worktree_remove", activePath };
    const removeToken = issueDestructiveGateToken(manifest, state, pass, removeEffect);
    runGuardedDestructiveGit(
      pass, removeToken, manifest.sourcePath,
      ["worktree", "remove", "--force", "--", activePath],
    );
    assert(!existsSync(manifest.worktreePath));
    assert(!existsSync(manifest.quarantinePath));
    assertOwnedRegistrationsAbsent(manifest, state);
    if (options.interruptAfterWorktreeRemoval) {
      throw new ExpectedInterruption("simulated crash after worktree removal but before result persistence");
    }
    state.phase = "worktree_removed";
    writeState(manifest, state);
    return;
  }
  if (!["removal_ready", "worktree_removed", "local_delete_ready", "local_delete_refused", "local_ref_removed", "remote_delete_ready", "remote_delete_refused", "remote_ref_removed", "complete"].includes(state.phase)) {
    throw new Error("worktree vanished without verified removal-ready evidence");
  }
  assert(state.removalReady, "missing removal-ready evidence");
  assertOwnedRegistrationsAbsent(manifest, state);
  if (state.phase === "removal_ready") {
    state.phase = "worktree_removed";
    writeState(manifest, state);
  }
}

function refDeletionRecord(manifest, ref, status) {
  return {
    status,
    expectedSha: manifest.candidateSha,
    taskRef: ref,
    nonce: manifest.nonce,
  };
}

function issueRefDeletionTokenOrRecordRefusal(manifest, state, pass, effect, record, refusedPhase) {
  try {
    return issueDestructiveGateToken(manifest, state, pass, effect);
  } catch (error) {
    record.status = "refused_before_dispatch";
    state.phase = refusedPhase;
    writeState(manifest, state);
    throw error;
  }
}

function removeLocalRefExactly(manifest, state, options = {}, pass = beginDestructivePass()) {
  verifyLiveIntegration(manifest, state);
  assertNoOtherTaskRefConsumer(manifest, state);
  const observed = observeLocalRef(manifest, state, manifest.taskRef);
  if (state.localDelete?.status === "intent_recorded") {
    if (observed.kind === "present") {
      throw new NeedsYouError("local Task ref recreation or prior deletion is ambiguous");
    }
    state.localDelete.status = "confirmed_absent";
    state.phase = "local_ref_removed";
    writeState(manifest, state);
    return;
  }
  if (state.localDelete?.status === "confirmed_absent") {
    if (observed.kind === "present") {
      throw new NeedsYouError("local Task ref recreation after confirmed absence is ambiguous");
    }
    state.phase = "local_ref_removed";
    writeState(manifest, state);
    return;
  }
  if (state.localDelete?.status === "refused_before_dispatch") {
    if (observed.kind !== "present" || observed.sha !== manifest.candidateSha) {
      throw new NeedsYouError("local Task ref changed after pre-dispatch refusal");
    }
    state.localDelete.status = "intent_recorded";
    state.phase = "local_delete_ready";
    writeState(manifest, state);
  } else {
    if (observed.kind === "absent") {
      state.localDelete = refDeletionRecord(manifest, manifest.taskRef, "confirmed_absent");
      state.phase = "local_ref_removed";
      writeState(manifest, state);
      return;
    }
    assert.equal(observed.sha, manifest.candidateSha, "local Task ref changed");
    assertRepositoryOwnership(manifest, state);
    verifyLiveIntegration(manifest, state);
    assertNoOtherTaskRefConsumer(manifest, state);
    const justInTime = observeLocalRef(manifest, state, manifest.taskRef);
    assert.equal(justInTime.kind, "present");
    assert.equal(justInTime.sha, manifest.candidateSha, "local Task ref raced");
    state.localDelete = refDeletionRecord(manifest, manifest.taskRef, "intent_recorded");
    state.phase = "local_delete_ready";
    writeState(manifest, state);
  }
  const deleteEffect = {
    kind: "local_ref_delete",
    activePath: null,
    ref: manifest.taskRef,
    expectedOid: manifest.candidateSha,
  };
  const deleteToken = issueRefDeletionTokenOrRecordRefusal(
    manifest, state, pass, deleteEffect, state.localDelete, "local_delete_refused",
  );
  runGuardedDestructiveGit(
    pass, deleteToken, manifest.sourcePath,
    ["update-ref", "-d", manifest.taskRef, manifest.candidateSha],
  );
  if (options.interruptAfterLocalDelete) {
    throw new ExpectedInterruption("simulated crash after local deletion but before result persistence");
  }
  assertRepositoryOwnership(manifest, state);
  assert.equal(observeLocalRef(manifest, state, manifest.taskRef).kind, "absent", "local ref deletion was not confirmed");
  state.localDelete.status = "confirmed_absent";
  state.phase = "local_ref_removed";
  writeState(manifest, state);
}

function removeRemoteRefExactly(manifest, state, options = {}, pass = beginDestructivePass()) {
  verifyLiveIntegration(manifest, state);
  const observed = queryOwnedRemoteRef(manifest, state, manifest.remoteTaskRef);
  if (state.remoteDelete?.status === "intent_recorded") {
    if (observed.kind === "present") {
      throw new NeedsYouError("remote Task ref recreation or prior deletion is ambiguous");
    }
    state.remoteDelete.status = "confirmed_absent";
    state.phase = "remote_ref_removed";
    writeState(manifest, state);
    return;
  }
  if (state.remoteDelete?.status === "confirmed_absent") {
    if (observed.kind === "present") {
      throw new NeedsYouError("remote Task ref recreation after confirmed absence is ambiguous");
    }
    state.phase = "remote_ref_removed";
    writeState(manifest, state);
    return;
  }
  if (state.remoteDelete?.status === "refused_before_dispatch") {
    if (observed.kind !== "present" || observed.sha !== manifest.candidateSha) {
      throw new NeedsYouError("remote Task ref changed after pre-dispatch refusal");
    }
    state.remoteDelete.status = "intent_recorded";
    state.phase = "remote_delete_ready";
    writeState(manifest, state);
  } else {
    if (observed.kind === "absent") {
      state.remoteDelete = refDeletionRecord(manifest, manifest.remoteTaskRef, "confirmed_absent");
      state.phase = "remote_ref_removed";
      writeState(manifest, state);
      return;
    }
    assert.equal(observed.sha, manifest.candidateSha, "remote Task ref changed");
    if (options.beforeRemoteDelete) options.beforeRemoteDelete();
    assertRepositoryOwnership(manifest, state);
    verifyLiveIntegration(manifest, state);
    const justInTime = queryOwnedRemoteRef(manifest, state, manifest.remoteTaskRef);
    assert.equal(justInTime.kind, "present", "remote Task ref disappeared before deletion");
    assert.equal(justInTime.sha, manifest.candidateSha, "remote Task ref raced");
    state.remoteDelete = refDeletionRecord(manifest, manifest.remoteTaskRef, "intent_recorded");
    state.phase = "remote_delete_ready";
    writeState(manifest, state);
  }
  const deleteEffect = {
    kind: "remote_ref_delete",
    activePath: null,
    ref: manifest.remoteTaskRef,
    expectedOid: manifest.candidateSha,
  };
  const deleteToken = issueRefDeletionTokenOrRecordRefusal(
    manifest, state, pass, deleteEffect, state.remoteDelete, "remote_delete_refused",
  );
  if (options.beforeLeasePush) options.beforeLeasePush();
  const deletion = runGuardedDestructiveGit(
    pass, deleteToken, manifest.sourcePath,
    [
      "push", `--force-with-lease=${manifest.remoteTaskRef}:${manifest.candidateSha}`,
      "origin", `:${manifest.remoteTaskRef}`,
    ],
    { allowFailure: true },
  );
  assert.equal(deletion.status, 0, "remote expected-head deletion failed");
  if (options.interruptAfterRemoteDelete) {
    throw new ExpectedInterruption("simulated crash after remote deletion but before result persistence");
  }
  assertRepositoryOwnership(manifest, state);
  assert.equal(queryOwnedRemoteRef(manifest, state, manifest.remoteTaskRef).kind, "absent", "remote deletion was not confirmed");
  state.remoteDelete.status = "confirmed_absent";
  state.phase = "remote_ref_removed";
  writeState(manifest, state);
}

function assertCompletedCleanupTerminal(manifest, state) {
  assert.equal(state.localDelete?.status, "confirmed_absent", "completed cleanup lacks confirmed local deletion");
  assert.equal(state.remoteDelete?.status, "confirmed_absent", "completed cleanup lacks confirmed remote deletion");
  const local = observeLocalRef(manifest, state, manifest.taskRef);
  const remote = queryOwnedRemoteRef(manifest, state, manifest.remoteTaskRef);
  if (local.kind === "present" || remote.kind === "present") {
    throw new NeedsYouError("completed cleanup ref recreation is ambiguous");
  }
}

function reconcileCleanup(manifest, options = {}) {
  const state = readState(manifest);
  const pass = beginDestructivePass();
  assertRepositoryOwnership(manifest, state);
  if (state.phase === "complete") {
    assertCompletedCleanupTerminal(manifest, state);
    return state;
  }
  reconcileWorktreeRemoval(manifest, state, options, pass);
  removeLocalRefExactly(manifest, state, options, pass);
  removeRemoteRefExactly(manifest, state, options, pass);
  assert.equal(state.localDelete?.status, "confirmed_absent", "local Task ref absence is unconfirmed");
  assert.equal(state.remoteDelete?.status, "confirmed_absent", "remote Task ref absence is unconfirmed");
  state.phase = "complete";
  writeState(manifest, state);
  return state;
}

function makeIntent(manifest) {
  return {
    schemaVersion: 2,
    operationId: `cleanup-${manifest.taskId}-${manifest.runId}`,
    taskId: manifest.taskId,
    runId: manifest.runId,
    nonce: manifest.nonce,
    phase: "intent_recorded",
    expectedCandidate: manifest.candidateSha,
    taskRef: manifest.taskRef,
    remoteTaskRef: manifest.remoteTaskRef,
    baseRef: manifest.baseRef,
    recoveryRef: manifest.recoveryRef,
    indexRecoveryRef: manifest.indexRecoveryRef,
    snapshotSha: null,
    indexSnapshotSha: null,
    ignoredRecovery: null,
    localOriginAuthority: manifest.localOriginAuthority,
    localDelete: null,
    remoteDelete: null,
    removalReady: null,
    lastVerifiedLiveBase: null,
  };
}

function validatedIgnoredRecoveryPolicy(overrides) {
  const policy = { ...DEFAULT_IGNORED_RECOVERY_POLICY, ...overrides };
  for (const field of ["maxBytes", "maxEntries", "maxFileBytes", "maxScanEntries", "streamChunkBytes", "retentionMs", "minimumFreePercent"]) {
    assert(Number.isSafeInteger(policy[field]) && policy[field] > 0, "ignored recovery policy is invalid");
  }
  assert(policy.maxBytes <= DEFAULT_IGNORED_RECOVERY_POLICY.maxBytes, "unproven ignored recovery byte expansion is blocked");
  assert(policy.maxFileBytes <= DEFAULT_IGNORED_RECOVERY_POLICY.maxFileBytes, "unproven ignored recovery file expansion is blocked");
  assert(policy.maxFileBytes <= policy.maxBytes, "ignored file bound exceeds aggregate bound");
  assert(policy.maxEntries <= DEFAULT_IGNORED_RECOVERY_POLICY.maxEntries, "unproven ignored recovery expansion is blocked");
  assert(policy.maxScanEntries <= DEFAULT_IGNORED_RECOVERY_POLICY.maxScanEntries, "unproven worktree scan expansion is blocked");
  assert(policy.streamChunkBytes <= DEFAULT_IGNORED_RECOVERY_POLICY.streamChunkBytes, "unproven recovery buffer expansion is blocked");
  assert(policy.retentionMs <= DEFAULT_IGNORED_RECOVERY_POLICY.retentionMs, "unproven recovery retention expansion is blocked");
  assert(policy.minimumFreePercent >= DEFAULT_IGNORED_RECOVERY_POLICY.minimumFreePercent, "ignored recovery free-space floor cannot be weakened");
  assert(policy.minimumFreePercent <= 100, "ignored recovery free-space percentage is invalid");
  return policy;
}

function makeManifest(input) {
  const remote = remoteFacts(input.sourcePath);
  const canonicalWorktreeRoot = canonical(input.worktreeRoot);
  const manifest = {
    schemaVersion: 2,
    taskId: input.taskId,
    runId: input.runId,
    nonce: input.nonce,
    sourcePath: resolve(input.sourcePath),
    sourceIdentity: identity(input.sourcePath),
    commonDirIdentity: identity(commonDir(input.sourcePath)),
    worktreeRoot: canonicalWorktreeRoot,
    worktreePath: resolve(input.worktreePath),
    quarantinePath: resolve(canonicalWorktreeRoot, `.director-quarantine-${input.nonce}`),
    worktreeIdentity: identity(input.worktreePath),
    remoteUrl: remote.url,
    remoteIdentity: remote.identity,
    remoteTransport: remote.transport,
    localOriginAuthority: {
      humanApproved: input.localOriginAuthority?.humanApproved === true,
      contained: input.localOriginAuthority?.contained === true,
    },
    taskRef: input.taskRef,
    remoteTaskRef: input.taskRef,
    baseRef: MAIN_REF,
    baseSha: input.baseSha,
    candidateSha: input.candidateSha,
    recoveryRef: input.recoveryRef,
    indexRecoveryRef: `${input.recoveryRef}-index`,
    ignoredRecoveryPolicy: validatedIgnoredRecoveryPolicy(input.ignoredRecoveryPolicy),
    ignoredRecoveryRoot: resolve(input.ignoredRecoveryRoot),
    ignoredArtifactPath: resolve(input.ignoredRecoveryRoot, `${input.taskId}-${input.runId}-${input.nonce}`),
    ignoredStagingPath: resolve(input.ignoredRecoveryRoot, `.${input.taskId}-${input.runId}-${input.nonce}.partial`),
    stateDir: input.stateDir,
    stateDirIdentity: identity(input.stateDir),
    intentPath: join(input.stateDir, "cleanup-intent.json"),
    intentTempPath: join(input.stateDir, ".cleanup-intent.next"),
  };
  writeFileSync(join(input.stateDir, "ownership.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  writeState(manifest, makeIntent(manifest));
  return manifest;
}

function waitForFile(path, child, timeoutMs = 10_000) {
  const start = Date.now();
  while (!existsSync(path)) {
    if (Date.now() - start > timeoutMs) throw new Error("child readiness timed out");
    const wait = spawnSync(process.execPath, ["-e", "setTimeout(() => {}, 20)"], { shell: false });
    assert.equal(wait.status, 0);
    assert.equal(child.exitCode, null, "child exited before signaling readiness");
  }
}

function windowsExclusiveLock(path, readyPath) {
  const escapedPath = path.replaceAll("'", "''");
  const escapedReady = readyPath.replaceAll("'", "''");
  const script = [
    `$stream = [System.IO.File]::Open('${escapedPath}', [System.IO.FileMode]::Open, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)`,
    `[System.IO.File]::WriteAllText('${escapedReady}', 'ready')`,
    "Start-Sleep -Seconds 30",
    "$stream.Dispose()",
  ].join("; ");
  return spawn("powershell.exe", ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script], {
    env: selectedEnvironment(),
    shell: false,
    stdio: "ignore",
    windowsHide: true,
  });
}

function startAuthFixture(readyPath) {
  return spawn(process.execPath, [process.argv[1], "--auth-fixture", readyPath], {
    env: selectedEnvironment(),
    shell: false,
    stdio: "ignore",
    windowsHide: true,
  });
}

function startUnixSocketFixture(socketPath, readyPath) {
  const script = [
    "const net = require('node:net')",
    "const fs = require('node:fs')",
    "const server = net.createServer(() => {})",
    "server.listen(process.argv[1], () => fs.writeFileSync(process.argv[2], 'ready'))",
    "process.on('SIGTERM', () => server.close(() => process.exit(0)))",
  ].join("; ");
  return spawn(process.execPath, ["-e", script, socketPath, readyPath], {
    env: selectedEnvironment(),
    shell: false,
    stdio: "ignore",
    windowsHide: true,
  });
}

async function runAuthFixture(readyPath) {
  const server = createServer((_request, response) => {
    response.statusCode = 401;
    response.setHeader("WWW-Authenticate", "Basic realm=director-fixture");
    response.end("authentication required");
  });
  await new Promise((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  assert(address && typeof address === "object");
  writeFileSync(readyPath, `${JSON.stringify({ port: address.port })}\n`);
  await new Promise((resolveStop) => {
    const stop = () => server.close(resolveStop);
    process.once("SIGTERM", stop);
    process.once("SIGINT", stop);
  });
}

function waitUntilUnlocked(path, timeoutMs = 5_000) {
  const start = Date.now();
  for (;;) {
    try {
      const fd = openSync(path, "r+");
      closeSync(fd);
      return;
    } catch (error) {
      if (Date.now() - start > timeoutMs) throw error;
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 25);
    }
  }
}

async function stopChild(child) {
  if (!child || child.exitCode !== null) return;
  child.kill();
  const start = Date.now();
  while (child.exitCode === null && Date.now() - start < 2_000) {
    await new Promise((resolveWait) => setTimeout(resolveWait, 25));
  }
  if (child.exitCode !== null) return;
  if (platform() === "win32") {
    spawnSync("taskkill.exe", ["/PID", String(child.pid), "/T", "/F"], {
      env: selectedEnvironment(), shell: false, stdio: "ignore", windowsHide: true,
    });
  } else {
    child.kill("SIGKILL");
  }
}

function activeOwnedPath(manifest) {
  const paths = [manifest.worktreePath, manifest.quarantinePath].filter(existsSync);
  assert.equal(paths.length, 1, "expected exactly one recoverable worktree path");
  return paths[0];
}

function assertRecoveryContent(manifest, snapshotSha) {
  assert.equal(git(manifest.sourcePath, ["show", `${snapshotSha}:tracked.txt`]), "dirty tracked");
  assert.equal(git(manifest.sourcePath, ["show", `${snapshotSha}:untracked.txt`]), "dirty untracked");
  assert.equal(git(manifest.sourcePath, ["show", `${snapshotSha}:staged.txt`]), "dirty staged");
  assert.equal(git(manifest.sourcePath, ["show", `${snapshotSha}:nested/untracked.txt`]), "nested dirty");
}

async function run() {
  const assertions = [];
  const root = mkdtempSync(join(tmpdir(), "director-m0.6-"));
  let rootRemoved = false;
  let heldFd;
  let cleanHeldFd;
  let lockChild;
  let authChild;
  let specialChild;
  try {
    const remote = join(root, "remote.git");
    const source = join(root, "source");
    const worktreeRoot = join(root, "worktrees");
    const taskPath = join(worktreeRoot, "task");
    const reviewPath = join(worktreeRoot, "review");
    const unknownPath = join(worktreeRoot, "unknown");
    const foreignSource = join(root, "foreign-source");
    const foreignPath = join(worktreeRoot, "foreign-worktree");
    const taskStateDir = join(root, "state", "dirty-run");
    const cleanStateDir = join(root, "state", "clean-run");
    const cleanLockStateDir = join(root, "state", "clean-lock-run");
    const ignoredRecoveryRoot = join(root, "ignored-recovery");
    const hookSentinel = join(root, "hook-must-not-run");
    const originHookSentinel = join(root, "origin-hook-must-not-run");
    disabledHooksPath = join(root, "disabled-hooks");
    gitConfigPath = join(root, "empty.gitconfig");
    mkdirSync(worktreeRoot, { recursive: true });
    mkdirSync(taskStateDir, { recursive: true });
    mkdirSync(cleanStateDir, { recursive: true });
    mkdirSync(cleanLockStateDir, { recursive: true });
    mkdirSync(disabledHooksPath, { recursive: true });
    writeFileSync(gitConfigPath, "");

    git(root, ["init", "--bare", "--initial-branch=main", remote]);
    git(root, ["clone", "--", remote, source]);
    writeFileSync(join(source, "tracked.txt"), "base\n");
    const lifecycleCases = [
      { worktree: { setup: ["must-not-run"] } },
      { worktree: { teardown: "must-not-run" } },
      { worktree: { terminals: [{ name: "automatic", command: "must-not-run" }] } },
      {
        worktree: { servicePorts: { portScript: "must-not-run" } },
        scripts: { api: { type: "service", command: "must-not-run", port: 41000 } },
      },
    ];
    for (const [index, config] of lifecycleCases.entries()) {
      writeFileSync(join(source, "paseo.json"), `${JSON.stringify(config)}\n`);
      git(source, ["add", "--", "tracked.txt", "paseo.json"]);
      git(source, ["-c", "user.name=Fixture", "-c", "user.email=fixture.invalid", "commit", "-m", `lifecycle case ${index + 1}`]);
      assert.throws(
        () => assertLifecycleBoundary(source, git(source, ["rev-parse", "HEAD"])),
        /installed Paseo worktree executable surface/,
      );
    }
    assertions.push("all_installed_lifecycle_surfaces_refused");
    assertions.push(verifyStateFileFsyncContract());
    writeFileSync(join(source, "paseo.json"), `${JSON.stringify({
      worktree: { setup: [], teardown: [], terminals: [], servicePorts: { range: "41000-41010" } },
    })}\n`);
    writeFileSync(join(source, ".gitignore"), "private-token.txt\nignored-dir/\n");
    writeFileSync(join(source, ".gitattributes"), "*.payload filter=evil\n*.bin filter=lfs\n");
    writeFileSync(join(source, "ordinary.bin"), "inactive filter bytes\n");
    writeFileSync(join(source, "committed-link"), "tracked.txt");
    git(source, ["add", "--", "paseo.json", ".gitignore", ".gitattributes", "ordinary.bin"]);
    const committedLinkBlob = git(source, ["hash-object", "-w", "--stdin"], { input: "tracked.txt" });
    git(source, ["update-index", "--add", "--cacheinfo", `120000,${committedLinkBlob},committed-link`]);
    git(source, ["-c", "user.name=Fixture", "-c", "user.email=fixture.invalid", "commit", "-m", "disable automatic lifecycle"]);
    if (platform() !== "win32") {
      rmSync(join(source, "committed-link"));
      symlinkSync("tracked.txt", join(source, "committed-link"));
    }
    const baseSha = git(source, ["rev-parse", "HEAD"]);
    assertLifecycleBoundary(source, baseSha);
    git(source, ["push", "-u", "origin", "main"]);

    const repositoryHooks = join(root, "repository-hooks");
    mkdirSync(repositoryHooks);
    const postIndexHook = join(repositoryHooks, "post-index-change");
    writeFileSync(postIndexHook, `#!/bin/sh\ntouch '${hookSentinel.replaceAll("'", "'\\''")}'\n`);
    chmodSync(postIndexHook, 0o755);
    git(source, ["config", "--local", "core.hooksPath", repositoryHooks]);
    git(source, ["config", "--local", "core.fsmonitor", "true"]);
    git(source, ["config", "--local", "filter.evil.clean", "must-not-run"]);
    assert.equal(command("git", ["config", "--file", join(source, ".git", "config"), "--get", "core.hooksPath"]), repositoryHooks);
    assert.equal(command("git", ["config", "--file", join(source, ".git", "config"), "--get", "core.fsmonitor"]), "true");
    assert.equal(command("git", ["config", "--file", join(source, ".git", "config"), "--get", "filter.evil.clean"]), "must-not-run");

    validateRef(source, TASK_REF);
    git(source, ["worktree", "add", "-b", TASK_REF.slice("refs/heads/".length), "--", taskPath, baseSha]);
    writeFileSync(join(taskPath, "candidate.txt"), "candidate\n");
    git(taskPath, ["add", "--", "candidate.txt"]);
    git(taskPath, ["-c", "user.name=Fixture", "-c", "user.email=fixture.invalid", "commit", "-m", "candidate"]);
    assert(!existsSync(hookSentinel), "repository hook executed despite the snapshot command boundary");
    const candidateSha = git(taskPath, ["rev-parse", "HEAD"]);
    git(taskPath, ["push", "-u", "origin", TASK_REF.slice("refs/heads/".length)]);
    const manifest = makeManifest({
      taskId: "dir-m0.6", runId: "run-1", nonce: "contract-nonce", sourcePath: source,
      worktreeRoot, worktreePath: taskPath, taskRef: TASK_REF, baseSha, candidateSha,
      recoveryRef: RECOVERY_REF, ignoredRecoveryRoot, stateDir: taskStateDir,
      localOriginAuthority: DISPOSABLE_LOCAL_ORIGIN_AUTHORITY,
    });
    let state = JSON.parse(readFileSync(manifest.intentPath, "utf8"));
    const originHooks = join(root, "origin-hooks");
    mkdirSync(originHooks);
    const preReceiveHook = join(originHooks, "pre-receive");
    writeFileSync(preReceiveHook, `#!/bin/sh\ntouch '${originHookSentinel.replaceAll("'", "'\\''")}'\n`);
    chmodSync(preReceiveHook, 0o755);
    git(remote, ["config", "core.hooksPath", originHooks]);
    for (const localOriginAuthority of [
      { humanApproved: false, contained: false },
      { humanApproved: true, contained: false },
      { humanApproved: false, contained: true },
    ]) {
      const refusedLocalOriginManifest = { ...manifest, localOriginAuthority };
      const refusedLocalOriginState = { ...structuredClone(state), localOriginAuthority };
      assert.throws(
        () => removeRemoteRefExactly(refusedLocalOriginManifest, refusedLocalOriginState),
        (error) => error instanceof NeedsYouError
          && error.message === "local-path origin effects require human approval and containment",
      );
    }
    const fileOriginUrl = pathToFileURL(remote).href;
    git(source, ["remote", "set-url", "origin", fileOriginUrl]);
    const fileOriginAuthority = { humanApproved: false, contained: false };
    const refusedFileOriginManifest = {
      ...manifest,
      remoteUrl: fileOriginUrl,
      localOriginAuthority: fileOriginAuthority,
    };
    const refusedFileOriginState = {
      ...structuredClone(state),
      localOriginAuthority: fileOriginAuthority,
    };
    assert.throws(
      () => removeRemoteRefExactly(refusedFileOriginManifest, refusedFileOriginState),
      (error) => error instanceof NeedsYouError
        && error.message === "local-path origin effects require human approval and containment",
    );
    git(source, ["remote", "set-url", "origin", remote]);
    assert(!existsSync(originHookSentinel));
    assert(existsSync(manifest.worktreePath));
    assert.equal(git(source, ["rev-parse", TASK_REF]), candidateSha);
    assert.equal(git(remote, ["rev-parse", TASK_REF]), candidateSha);
    git(remote, ["config", "--unset", "core.hooksPath"]);
    rmSync(originHooks, { recursive: true });
    assertions.push("local_origin_effects_require_approval_and_containment");
    assertions.push("file_url_origin_refused_consistently");

    git(source, ["update-ref", TOKEN_GUARD_REF, candidateSha, ZERO_SHA]);
    assert.throws(
      () => runGuardedDestructiveGit(
        beginDestructivePass(), null, source,
        ["update-ref", "-d", TOKEN_GUARD_REF, candidateSha],
      ),
      (error) => error instanceof NeedsYouError
        && error.message === "destructive command lacks a fresh recovery-evidence gate",
    );
    assert.equal(git(source, ["rev-parse", TOKEN_GUARD_REF]), candidateSha);
    git(source, ["update-ref", "-d", TOKEN_GUARD_REF, candidateSha]);
    assertions.push("destructive_commands_require_fresh_gate_token");

    const repairedDestructiveStages = [];

    const exerciseStoredEvidenceMutationTable = (
      assertionName, targetManifest, cases, assertNoDeletion,
      attempt = () => reconcileCleanup(targetManifest),
    ) => {
      for (const evidenceCase of cases) {
        const before = readState(targetManifest);
        const mutation = evidenceCase.mutate(before);
        try {
          let error;
          try {
            attempt();
            assert.fail(`stored evidence case ${evidenceCase.name} reached a destructive command`);
          } catch (caught) {
            error = caught;
          }
          assert(error instanceof NeedsYouError, `stored evidence case ${evidenceCase.name} did not park`);
          assertNoDeletion(readState(targetManifest));
          mutation.assertPreserved?.();
        } finally {
          mutation.restore();
        }
      }
      assertions.push(assertionName);
    };

    const gitRecoveryEvidenceCases = (targetManifest, stage) => [
      {
        name: "worktree recovery ref missing",
        mutate: (current) => {
          git(source, ["update-ref", "-d", current.recoveryRef, current.snapshotSha]);
          return {
            assertPreserved: () => assert.equal(observeLocalRef(targetManifest, current, current.recoveryRef).kind, "absent"),
            restore: () => git(source, ["update-ref", current.recoveryRef, current.snapshotSha, ZERO_SHA]),
          };
        },
      },
      {
        name: "worktree recovery ref expected OID changed",
        mutate: (current) => {
          git(source, ["update-ref", current.recoveryRef, baseSha, current.snapshotSha]);
          return {
            assertPreserved: () => assert.equal(git(source, ["rev-parse", current.recoveryRef]), baseSha),
            restore: () => git(source, ["update-ref", current.recoveryRef, current.snapshotSha, baseSha]),
          };
        },
      },
      {
        name: "index recovery ref missing",
        mutate: (current) => {
          git(source, ["update-ref", "-d", current.indexRecoveryRef, current.indexSnapshotSha]);
          return {
            assertPreserved: () => assert.equal(observeLocalRef(targetManifest, current, current.indexRecoveryRef).kind, "absent"),
            restore: () => git(source, ["update-ref", current.indexRecoveryRef, current.indexSnapshotSha, ZERO_SHA]),
          };
        },
      },
      {
        name: "index recovery ref expected OID changed",
        mutate: (current) => {
          git(source, ["update-ref", current.indexRecoveryRef, baseSha, current.indexSnapshotSha]);
          return {
            assertPreserved: () => assert.equal(git(source, ["rev-parse", current.indexRecoveryRef]), baseSha),
            restore: () => git(source, ["update-ref", current.indexRecoveryRef, current.indexSnapshotSha, baseSha]),
          };
        },
      },
      ...artifactEnvelopeEvidenceCases(targetManifest, stage),
    ];

    function artifactEnvelopeEvidenceCases(targetManifest, stage, payloadPath = null) {
      const artifactPath = targetManifest.ignoredArtifactPath;
      const manifestPath = join(artifactPath, "manifest.json");
      const ownerPath = join(artifactPath, "owner.json");
      const cases = [
        {
          name: "private artifact manifest changed",
          mutate: () => {
            const original = readFileSync(manifestPath);
            writeFileSync(manifestPath, Buffer.concat([original, Buffer.from(" ")]));
            return {
              assertPreserved: () => assert(existsSync(manifestPath)),
              restore: () => writeFileSync(manifestPath, original),
            };
          },
        },
        {
          name: "private artifact owner changed",
          mutate: () => {
            const original = readFileSync(ownerPath);
            writeFileSync(ownerPath, `${JSON.stringify({ foreign: true })}\n`);
            return {
              assertPreserved: () => assert(existsSync(ownerPath)),
              restore: () => writeFileSync(ownerPath, original),
            };
          },
        },
        {
          name: "private artifact identity recreated",
          mutate: () => {
            const parked = `${artifactPath}.${stage}.parked`;
            renameSync(artifactPath, parked);
            mkdirSync(artifactPath, { mode: 0o700 });
            writeFileSync(join(artifactPath, "unknown-sentinel"), "preserve unknown artifact\n");
            return {
              assertPreserved: () => assert.equal(
                readFileSync(join(artifactPath, "unknown-sentinel"), "utf8"),
                "preserve unknown artifact\n",
              ),
              restore: () => {
                rmSync(artifactPath, { recursive: true });
                renameSync(parked, artifactPath);
              },
            };
          },
        },
      ];
      if (payloadPath) {
        cases.unshift({
          name: "private artifact payload changed",
          mutate: () => {
            const original = readFileSync(payloadPath);
            const changed = Buffer.from(original);
            changed[0] ^= 1;
            writeFileSync(payloadPath, changed);
            return {
              assertPreserved: () => assert.deepEqual(readFileSync(payloadPath), changed),
              restore: () => writeFileSync(payloadPath, original),
            };
          },
        });
      }
      return cases;
    }
    inspectOwnedWorktree(manifest, taskPath, state);
    assertions.push("task_branch_and_worktree_owned");
    assertOwnedRegistration(manifest, state, taskPath);
    assertions.push("worktree_registration_identity_verified");
    const ordinarySafety = inspectMaterialSafety(taskPath, manifest.ignoredRecoveryPolicy);
    assert.equal(ordinarySafety.gitIgnoredPaths.length, 0);
    assert.equal(treeEntries(taskPath, candidateSha).get("committed-link").mode, "120000");
    assertByteExactTree(taskPath, candidateSha);
    assertions.push("committed_symlink_and_inactive_filter_supported");

    writeRestrictedFile(manifest.intentTempPath, "partial-state");
    assert.deepEqual(readState(manifest), state);
    rmSync(manifest.intentTempPath);
    writeRestrictedFile(manifest.intentPath, "truncated");
    assert.throws(() => reconcileCleanup(manifest), /cleanup intent is unavailable or invalid/);
    assert(existsSync(manifest.worktreePath));
    assert.equal(git(source, ["rev-parse", TASK_REF]), candidateSha);
    writeState(manifest, state);
    assert.deepEqual(readState(manifest), state);
    assertions.push("atomic_intent_write_and_corruption_fail_closed");

    git(source, ["worktree", "add", "--detach", "--", reviewPath, candidateSha]);
    assert.equal(git(reviewPath, ["rev-parse", "HEAD"]), candidateSha);
    assert.notEqual(git(reviewPath, ["symbolic-ref", "-q", "HEAD"], { allowFailure: true }).status, 0);
    assert.equal(commonDir(reviewPath), manifest.commonDirIdentity.canonicalPath);
    git(source, ["worktree", "remove", "--", reviewPath]);
    assertions.push("detached_review_exact_candidate");

    expectRefusal(assertions, "source_and_common_dir_refused", () => {
      inspectOwnedWorktree(manifest, source, state);
    }, /requested path differs/);
    git(source, ["worktree", "add", "-b", "unknown-owner", "--", unknownPath, baseSha]);
    writeFileSync(join(unknownPath, "unknown-sentinel.txt"), "preserve\n");
    expectRefusal(assertions, "unknown_worktree_refused", () => {
      inspectOwnedWorktree(manifest, unknownPath, state);
    }, /requested path differs/);
    const lexicalAlias = `${taskPath}${sep}..${sep}task`;
    expectRefusal(assertions, "lexical_alias_refused", () => {
      inspectOwnedWorktree(manifest, lexicalAlias, state);
    }, /requested path differs/);
    const aliasPath = join(worktreeRoot, "task-alias");
    symlinkSync(taskPath, aliasPath, platform() === "win32" ? "junction" : "dir");
    expectRefusal(assertions, "canonical_alias_refused", () => {
      inspectOwnedWorktree(manifest, aliasPath, state);
    }, /requested path differs/);
    rmSync(aliasPath, { force: true });

    const parkedPath = join(worktreeRoot, "task-parked");
    renameSync(taskPath, parkedPath);
    symlinkSync(unknownPath, taskPath, platform() === "win32" ? "junction" : "dir");
    expectRefusal(assertions, "path_swap_refused", () => {
      inspectOwnedWorktree(manifest, taskPath, state);
    }, /symlink or junction/);
    assert(existsSync(join(unknownPath, "unknown-sentinel.txt")));
    rmSync(taskPath, { force: true });
    renameSync(parkedPath, taskPath);

    mkdirSync(manifest.quarantinePath);
    writeFileSync(join(manifest.quarantinePath, "foreign-sentinel"), "preserve\n");
    let quarantineSquatError;
    try {
      reconcileCleanup(manifest);
      assert.fail("quarantine squatting must stop cleanup");
    } catch (error) {
      quarantineSquatError = error;
    }
    assert(quarantineSquatError instanceof NeedsYouError);
    assert.equal(quarantineSquatError.message, "quarantine path ownership is ambiguous");
    assert(existsSync(join(manifest.quarantinePath, "foreign-sentinel")));
    assert(existsSync(manifest.worktreePath));
    rmSync(manifest.quarantinePath, { recursive: true });
    assertions.push("quarantine_squatting_needs_you");

    git(source, ["worktree", "lock", "--reason", "dir-m0.6 contract", "--", taskPath]);
    assert.notEqual(git(source, ["worktree", "remove", "--force", "--", taskPath], { allowFailure: true }).status, 0);
    assert(existsSync(taskPath));
    git(source, ["worktree", "unlock", "--", taskPath]);
    assertions.push("administrative_lock_refused");

    git(root, ["clone", "--", remote, foreignSource]);
    git(foreignSource, ["worktree", "add", "--detach", "--", foreignPath, baseSha]);
    const foreignManifest = { ...manifest, worktreePath: resolve(foreignPath), worktreeIdentity: identity(foreignPath) };
    expectRefusal(assertions, "foreign_common_dir_refused", () => {
      inspectOwnedWorktree(foreignManifest, foreignPath, state);
    }, /Git common directory changed/);

    assert.throws(() => removeLocalRefExactly(manifest, state), /Candidate is not integrated in the live base/);
    assert.throws(() => removeRemoteRefExactly(manifest, state), /Candidate is not integrated in the live base/);
    assert.equal(git(source, ["rev-parse", TASK_REF]), candidateSha);
    assert.equal(git(remote, ["rev-parse", TASK_REF]), candidateSha);
    assertions.push("unintegrated_refs_refused");

    assert.throws(() => reconcileCleanup(manifest), /Candidate is not integrated in the live base/);
    state = readState(manifest);
    assert.equal(state.phase, "intent_recorded");
    assert.equal(state.removalReady, null);
    assert(existsSync(manifest.worktreePath));
    assertOwnedRegistration(manifest, state, manifest.worktreePath);
    assert.equal(observeLocalRef(manifest, state, TASK_REF).sha, candidateSha);
    assert.equal(queryOwnedRemoteRef(manifest, state, TASK_REF).sha, candidateSha);
    assertions.push("unintegrated_clean_worktree_preserved");

    git(source, ["merge", "--ff-only", candidateSha]);
    git(source, ["push", "origin", "main"]);
    const candidateTree = git(source, ["rev-parse", `${candidateSha}^{tree}`]);
    const racedSha = git(source, [
      "-c", "user.name=Fixture", "-c", "user.email=fixture.invalid",
      "commit-tree", candidateTree, "-p", candidateSha,
    ], { input: "remote race\n" });
    assert.throws(() => removeRemoteRefExactly(manifest, state, {
      beforeRemoteDelete: () => git(source, ["push", "--force", "origin", `${racedSha}:${TASK_REF}`]),
    }), /remote Task ref raced/);
    assert.equal(git(remote, ["rev-parse", TASK_REF]), racedSha);
    assertions.push("remote_compare_delete_race_refused");
    git(source, ["push", "--force", "origin", `${candidateSha}:${TASK_REF}`]);

    const exerciseHiddenIndexEdit = (ref, slug, flag, editedBytes, assertionName) => {
      const hiddenPath = join(worktreeRoot, `hidden-${slug}`);
      const hiddenStateDir = join(root, "state", `hidden-${slug}`);
      mkdirSync(hiddenStateDir, { recursive: true });
      git(source, ["worktree", "add", "-b", ref.slice("refs/heads/".length), "--", hiddenPath, candidateSha]);
      git(hiddenPath, ["push", "-u", "origin", ref.slice("refs/heads/".length)]);
      const hiddenManifest = makeManifest({
        taskId: "dir-m0.6", runId: `hidden-${slug}`, nonce: `hidden-${slug}-nonce`, sourcePath: source,
        worktreeRoot, worktreePath: hiddenPath, taskRef: ref, baseSha, candidateSha,
        recoveryRef: `refs/director/recovery/dir-m0.6/hidden-${slug}/hidden-${slug}-nonce`,
        ignoredRecoveryRoot, stateDir: hiddenStateDir,
        localOriginAuthority: DISPOSABLE_LOCAL_ORIGIN_AUTHORITY,
      });
      git(hiddenPath, ["update-index", flag, "--", "tracked.txt"]);
      writeFileSync(join(hiddenPath, "tracked.txt"), editedBytes);
      assert.equal(git(hiddenPath, ["status", "--porcelain=v1", "--untracked-files=all"]), "");
      assert.throws(() => reconcileCleanup(hiddenManifest, { interruptAfterSnapshot: true }), ExpectedInterruption);
      const hiddenState = readState(hiddenManifest);
      assert.equal(hiddenState.phase, "snapshot_verified");
      assert.equal(git(hiddenManifest.sourcePath, ["show", `${hiddenState.snapshotSha}:tracked.txt`]), editedBytes.trim());
      assert.notEqual(git(hiddenManifest.sourcePath, ["rev-parse", `${hiddenState.snapshotSha}^{tree}`]), git(hiddenManifest.sourcePath, ["rev-parse", `${candidateSha}^{tree}`]));
      assert(existsSync(hiddenManifest.worktreePath));
      assertOwnedRegistration(hiddenManifest, hiddenState, hiddenManifest.worktreePath);
      assert.equal(observeLocalRef(hiddenManifest, hiddenState, ref).sha, candidateSha);
      assert.equal(queryOwnedRemoteRef(hiddenManifest, hiddenState, ref).sha, candidateSha);
      const hiddenCompleted = reconcileCleanup(hiddenManifest);
      assert.equal(hiddenCompleted.phase, "complete");
      const hiddenRestore = join(worktreeRoot, `hidden-${slug}-restored`);
      git(source, ["worktree", "add", "--detach", "--", hiddenRestore, hiddenCompleted.snapshotSha]);
      assert.equal(readFileSync(join(hiddenRestore, "tracked.txt"), "utf8"), editedBytes);
      git(source, ["worktree", "remove", "--force", "--", hiddenRestore]);
      assertions.push(assertionName);
    };
    exerciseHiddenIndexEdit(ASSUME_REF, "assume", "--assume-unchanged", "assume-hidden exact bytes\n", "assume_unchanged_edit_recovered");
    exerciseHiddenIndexEdit(SKIP_REF, "skip", "--skip-worktree", "skip-worktree exact bytes\n", "skip_worktree_edit_recovered");

    const stagedOnlyPath = join(worktreeRoot, "staged-only");
    const stagedOnlyStateDir = join(root, "state", "staged-only");
    mkdirSync(stagedOnlyStateDir, { recursive: true });
    git(source, ["worktree", "add", "-b", STAGED_ONLY_REF.slice("refs/heads/".length), "--", stagedOnlyPath, candidateSha]);
    git(stagedOnlyPath, ["push", "-u", "origin", STAGED_ONLY_REF.slice("refs/heads/".length)]);
    const stagedOnlyManifest = makeManifest({
      taskId: "dir-m0.6", runId: "staged-only", nonce: "staged-only-nonce", sourcePath: source,
      worktreeRoot, worktreePath: stagedOnlyPath, taskRef: STAGED_ONLY_REF, baseSha, candidateSha,
      recoveryRef: "refs/director/recovery/dir-m0.6/staged-only/staged-only-nonce",
      ignoredRecoveryRoot, stateDir: stagedOnlyStateDir,
      localOriginAuthority: DISPOSABLE_LOCAL_ORIGIN_AUTHORITY,
    });
    writeFileSync(join(stagedOnlyPath, "tracked.txt"), "staged index exact bytes\n");
    git(stagedOnlyPath, ["add", "--", "tracked.txt"]);
    writeFileSync(join(stagedOnlyPath, "tracked.txt"), "base\n");
    assert.equal(git(stagedOnlyPath, ["status", "--porcelain=v1", "--untracked-files=all"]), "MM tracked.txt");
    const stagedIndexTree = currentIndexTree(stagedOnlyManifest, readState(stagedOnlyManifest), stagedOnlyPath);
    assert.notEqual(stagedIndexTree, candidateTree);
    assert.throws(
      () => reconcileCleanup(stagedOnlyManifest, { interruptAfterIndexRecoveryRef: true }),
      ExpectedInterruption,
    );
    let stagedOnlyState = readState(stagedOnlyManifest);
    assert.equal(stagedOnlyState.phase, "intent_recorded");
    assert.equal(stagedOnlyState.indexSnapshotSha, null);
    const indexEffectSha = observeLocalRef(
      stagedOnlyManifest,
      stagedOnlyState,
      stagedOnlyManifest.indexRecoveryRef,
    ).sha;
    assert.equal(git(source, ["show", `${indexEffectSha}:tracked.txt`]), "staged index exact bytes");
    assert.equal(git(stagedOnlyPath, ["show", ":tracked.txt"]), "staged index exact bytes");
    assert.equal(readFileSync(join(stagedOnlyPath, "tracked.txt"), "utf8"), "base\n");
    assertOwnedRegistration(stagedOnlyManifest, stagedOnlyState, stagedOnlyPath);
    assert.equal(observeLocalRef(stagedOnlyManifest, stagedOnlyState, STAGED_ONLY_REF).sha, candidateSha);
    assert.equal(queryOwnedRemoteRef(stagedOnlyManifest, stagedOnlyState, STAGED_ONLY_REF).sha, candidateSha);
    const stagedOnlyCompleted = reconcileCleanup(stagedOnlyManifest);
    stagedOnlyState = readState(stagedOnlyManifest);
    assert.equal(stagedOnlyCompleted.phase, "complete");
    assert.equal(stagedOnlyState.indexSnapshotSha, indexEffectSha);
    assert.equal(observeLocalRef(stagedOnlyManifest, stagedOnlyState, stagedOnlyManifest.indexRecoveryRef).sha, indexEffectSha);
    const stagedRestorePath = join(worktreeRoot, "staged-only-restored");
    git(source, ["worktree", "add", "--detach", "--", stagedRestorePath, indexEffectSha]);
    assert.equal(readFileSync(join(stagedRestorePath, "tracked.txt"), "utf8"), "staged index exact bytes\n");
    git(source, ["worktree", "remove", "--force", "--", stagedRestorePath]);
    assertions.push("staged_index_only_edit_recovered");

    const stagedIndexCases = [
      {
        slug: "addition",
        ref: STAGED_ADDITION_REF,
        assertionName: "staged_index_only_addition_recovered",
        prepare: (path) => {
          writeFileSync(join(path, "index-only-added.txt"), "index-only addition bytes\n");
          git(path, ["add", "--", "index-only-added.txt"]);
          rmSync(join(path, "index-only-added.txt"));
        },
        assertSnapshot: (snapshotSha) => assert.equal(
          git(source, ["show", `${snapshotSha}:index-only-added.txt`]),
          "index-only addition bytes",
        ),
        assertRestored: (path) => assert.equal(
          readFileSync(join(path, "index-only-added.txt"), "utf8"),
          "index-only addition bytes\n",
        ),
      },
      {
        slug: "deletion",
        ref: STAGED_DELETION_REF,
        assertionName: "staged_index_only_deletion_recovered",
        prepare: (path) => git(path, ["rm", "--cached", "--", "tracked.txt"]),
        assertSnapshot: (snapshotSha) => assert(!snapshotContains(manifest, snapshotSha, "tracked.txt")),
        assertRestored: (path) => assert(!existsSync(join(path, "tracked.txt"))),
      },
    ];
    for (const stagedCase of stagedIndexCases) {
      const stagedPath = join(worktreeRoot, `staged-${stagedCase.slug}`);
      const stagedStateDir = join(root, "state", `staged-${stagedCase.slug}`);
      mkdirSync(stagedStateDir, { recursive: true });
      git(source, ["worktree", "add", "-b", stagedCase.ref.slice("refs/heads/".length), "--", stagedPath, candidateSha]);
      git(stagedPath, ["push", "-u", "origin", stagedCase.ref.slice("refs/heads/".length)]);
      const stagedManifest = makeManifest({
        taskId: "dir-m0.6", runId: `staged-${stagedCase.slug}`, nonce: `staged-${stagedCase.slug}-nonce`,
        sourcePath: source, worktreeRoot, worktreePath: stagedPath, taskRef: stagedCase.ref,
        baseSha, candidateSha,
        recoveryRef: `refs/director/recovery/dir-m0.6/staged-${stagedCase.slug}/staged-${stagedCase.slug}-nonce`,
        ignoredRecoveryRoot, stateDir: stagedStateDir,
        localOriginAuthority: DISPOSABLE_LOCAL_ORIGIN_AUTHORITY,
      });
      stagedCase.prepare(stagedPath);
      assert.notEqual(git(stagedPath, ["status", "--porcelain=v1", "--untracked-files=all"]), "");
      const currentState = readState(stagedManifest);
      assert.notEqual(currentIndexTree(stagedManifest, currentState, stagedPath), candidateTree);
      assert.throws(
        () => reconcileCleanup(stagedManifest, { interruptAfterIndexRecoveryRef: true }),
        ExpectedInterruption,
      );
      const effectState = readState(stagedManifest);
      const snapshotSha = observeLocalRef(stagedManifest, effectState, stagedManifest.indexRecoveryRef).sha;
      stagedCase.assertSnapshot(snapshotSha);
      assert(existsSync(stagedManifest.worktreePath));
      assert.equal(observeLocalRef(stagedManifest, effectState, stagedCase.ref).sha, candidateSha);
      assert.equal(queryOwnedRemoteRef(stagedManifest, effectState, stagedCase.ref).sha, candidateSha);
      const stagedCompleted = reconcileCleanup(stagedManifest);
      assert.equal(stagedCompleted.phase, "complete");
      assert.equal(stagedCompleted.indexSnapshotSha, snapshotSha);
      const restoredPath = join(worktreeRoot, `staged-${stagedCase.slug}-restored`);
      git(source, ["worktree", "add", "--detach", "--", restoredPath, snapshotSha]);
      stagedCase.assertRestored(restoredPath);
      git(source, ["worktree", "remove", "--force", "--", restoredPath]);
      assertions.push(stagedCase.assertionName);
    }

    const sparsePath = join(worktreeRoot, "sparse-missing");
    const sparseStateDir = join(root, "state", "sparse-missing");
    mkdirSync(sparseStateDir, { recursive: true });
    git(source, ["worktree", "add", "-b", SPARSE_REF.slice("refs/heads/".length), "--", sparsePath, candidateSha]);
    git(sparsePath, ["push", "-u", "origin", SPARSE_REF.slice("refs/heads/".length)]);
    const sparseManifest = makeManifest({
      taskId: "dir-m0.6", runId: "sparse-missing", nonce: "sparse-missing-nonce", sourcePath: source,
      worktreeRoot, worktreePath: sparsePath, taskRef: SPARSE_REF, baseSha, candidateSha,
      recoveryRef: "refs/director/recovery/dir-m0.6/sparse-missing/sparse-missing-nonce",
      ignoredRecoveryRoot, stateDir: sparseStateDir,
      localOriginAuthority: DISPOSABLE_LOCAL_ORIGIN_AUTHORITY,
    });
    git(sparsePath, ["update-index", "--skip-worktree", "--", "tracked.txt"]);
    rmSync(join(sparsePath, "tracked.txt"));
    assert.equal(git(sparsePath, ["status", "--porcelain=v1", "--untracked-files=all"]), "");
    assert.throws(() => reconcileCleanup(sparseManifest), /sparse or skip-worktree state omits tracked material/);
    const sparseState = readState(sparseManifest);
    assert(existsSync(sparseManifest.worktreePath));
    assertOwnedRegistration(sparseManifest, sparseState, sparseManifest.worktreePath);
    assert.equal(observeLocalRef(sparseManifest, sparseState, SPARSE_REF).sha, candidateSha);
    assert.equal(queryOwnedRemoteRef(sparseManifest, sparseState, SPARSE_REF).sha, candidateSha);
    writeFileSync(join(sparsePath, "tracked.txt"), "base\n");
    git(sparsePath, ["update-index", "--no-skip-worktree", "--", "tracked.txt"]);
    assert.equal(reconcileCleanup(sparseManifest).phase, "complete");
    assertions.push("sparse_missing_material_needs_you_then_recovers");

    const normalizationPath = join(worktreeRoot, "normalization");
    const normalizationStateDir = join(root, "state", "normalization");
    mkdirSync(normalizationStateDir, { recursive: true });
    git(source, ["worktree", "add", "-b", NORMALIZATION_REF.slice("refs/heads/".length), "--", normalizationPath, candidateSha]);
    git(normalizationPath, ["push", "-u", "origin", NORMALIZATION_REF.slice("refs/heads/".length)]);
    const normalizationManifest = makeManifest({
      taskId: "dir-m0.6", runId: "normalization", nonce: "normalization-nonce", sourcePath: source,
      worktreeRoot, worktreePath: normalizationPath, taskRef: NORMALIZATION_REF, baseSha, candidateSha,
      recoveryRef: "refs/director/recovery/dir-m0.6/normalization/normalization-nonce",
      ignoredRecoveryRoot, stateDir: normalizationStateDir,
      localOriginAuthority: DISPOSABLE_LOCAL_ORIGIN_AUTHORITY,
    });
    writeFileSync(join(normalizationPath, ".gitattributes"), "*.payload filter=evil\ntracked.txt text eol=lf\n");
    writeFileSync(join(normalizationPath, "tracked.txt"), "base\r\n");
    assert.throws(() => reconcileCleanup(normalizationManifest), /Git normalization prevents byte-exact recovery/);
    const normalizationState = readState(normalizationManifest);
    assert(existsSync(normalizationManifest.worktreePath));
    assert.equal(readFileSync(join(normalizationPath, "tracked.txt"), "utf8"), "base\r\n");
    assert.equal(observeLocalRef(normalizationManifest, normalizationState, NORMALIZATION_REF).sha, candidateSha);
    assert.equal(observeLocalRef(normalizationManifest, normalizationState, normalizationManifest.recoveryRef).kind, "absent");
    writeFileSync(join(normalizationPath, ".gitattributes"), "*.payload filter=evil\n");
    writeFileSync(join(normalizationPath, "tracked.txt"), "base\n");
    assert.equal(reconcileCleanup(normalizationManifest).phase, "complete");
    assertions.push("eol_normalization_needs_you_without_false_recovery");

    assert.equal(queryRemoteRef(source, "origin", "refs/heads/absent-contract-ref").kind, "absent");
    assert.throws(() => classifyLsRemote({ status: 128, stdout: "", stderr: "auth" }, TASK_REF), ExternalUnavailableError);
    assertions.push("ls_remote_statuses_distinguished");
    const authReady = join(root, "auth-ready.json");
    authChild = startAuthFixture(authReady);
    waitForFile(authReady, authChild);
    const authPort = JSON.parse(readFileSync(authReady, "utf8")).port;
    const authUrl = `http://127.0.0.1:${authPort}/repo.git`;
    assert.throws(() => queryRemoteRef(source, authUrl, TASK_REF), ExternalUnavailableError);
    await stopChild(authChild);
    authChild = undefined;
    assert.throws(() => queryRemoteRef(source, authUrl, TASK_REF), ExternalUnavailableError);
    assertions.push("offline_and_auth_errors_refused");

    const cleanPath = join(worktreeRoot, "clean-task");
    git(source, ["worktree", "add", "-b", CLEAN_REF.slice("refs/heads/".length), "--", cleanPath, candidateSha]);
    git(cleanPath, ["push", "-u", "origin", CLEAN_REF.slice("refs/heads/".length)]);
    const cleanManifest = makeManifest({
      taskId: "dir-m0.6", runId: "clean-run", nonce: "clean-nonce", sourcePath: source,
      worktreeRoot, worktreePath: cleanPath, taskRef: CLEAN_REF, baseSha, candidateSha,
      recoveryRef: CLEAN_RECOVERY_REF, ignoredRecoveryRoot, stateDir: cleanStateDir,
      localOriginAuthority: DISPOSABLE_LOCAL_ORIGIN_AUTHORITY,
    });
    const cleanIgnoredFile = join(cleanPath, "private-token.txt");
    const cleanIgnoredDirectory = join(cleanPath, "ignored-dir");
    const cleanIgnoredNestedFile = join(cleanIgnoredDirectory, "value.txt");
    const cleanIgnoredEmptyDirectory = join(cleanIgnoredDirectory, "empty");
    writeFileSync(cleanIgnoredFile, "ignored secret-like fixture bytes\n");
    mkdirSync(cleanIgnoredDirectory);
    mkdirSync(cleanIgnoredEmptyDirectory);
    writeFileSync(cleanIgnoredNestedFile, "ignored directory fixture bytes\n");
    const realisticIgnoredDirectory = join(cleanIgnoredDirectory, "realistic-tree");
    mkdirSync(realisticIgnoredDirectory);
    for (let index = 0; index < 8; index += 1) {
      writeFileSync(join(realisticIgnoredDirectory, `entry-${String(index).padStart(4, "0")}.dat`), "x");
    }
    chmodSync(cleanIgnoredFile, 0o640);
    chmodSync(cleanIgnoredDirectory, 0o750);
    chmodSync(cleanIgnoredEmptyDirectory, 0o710);
    chmodSync(cleanIgnoredNestedFile, 0o600);
    const ignoredTimestamp = new Date("2024-01-02T03:04:05.000Z");
    utimesSync(cleanIgnoredFile, ignoredTimestamp, ignoredTimestamp);
    utimesSync(cleanIgnoredNestedFile, ignoredTimestamp, ignoredTimestamp);
    utimesSync(cleanIgnoredEmptyDirectory, ignoredTimestamp, ignoredTimestamp);
    utimesSync(cleanIgnoredDirectory, ignoredTimestamp, ignoredTimestamp);
    const originalIgnoredMetadata = {
      fileMode: statSync(cleanIgnoredFile).mode & 0o777,
      fileMtimeMs: Math.trunc(statSync(cleanIgnoredFile).mtimeMs),
      nestedMode: statSync(cleanIgnoredNestedFile).mode & 0o777,
      nestedMtimeMs: Math.trunc(statSync(cleanIgnoredNestedFile).mtimeMs),
      directoryMode: statSync(cleanIgnoredDirectory).mode & 0o777,
      directoryMtimeMs: Math.trunc(statSync(cleanIgnoredDirectory).mtimeMs),
      emptyDirectoryMode: statSync(cleanIgnoredEmptyDirectory).mode & 0o777,
      emptyDirectoryMtimeMs: Math.trunc(statSync(cleanIgnoredEmptyDirectory).mtimeMs),
    };
    assert.throws(
      () => assertIgnoredRecoveryDiskBudget({ totalBytes: 1 }, 100n, 100n),
      /ignored recovery cannot preserve the disk safety threshold/,
    );
    assert.throws(
      () => validatedIgnoredRecoveryPolicy({ maxEntries: DEFAULT_IGNORED_RECOVERY_POLICY.maxEntries + 1 }),
      /unproven ignored recovery expansion is blocked/,
    );
    assertions.push("unproven_recovery_policy_expansion_blocked");
    assert.equal(
      validatedIgnoredRecoveryPolicy({ maxBytes: DEFAULT_IGNORED_RECOVERY_POLICY.maxBytes }).maxBytes,
      DEFAULT_IGNORED_RECOVERY_POLICY.maxBytes,
    );
    assert.throws(
      () => validatedIgnoredRecoveryPolicy({ maxBytes: DEFAULT_IGNORED_RECOVERY_POLICY.maxBytes + 1 }),
      /unproven ignored recovery byte expansion is blocked/,
    );
    assertions.push("ignored_recovery_max_bytes_boundary_enforced");
    assert.equal(
      validatedIgnoredRecoveryPolicy({ maxFileBytes: DEFAULT_IGNORED_RECOVERY_POLICY.maxFileBytes }).maxFileBytes,
      DEFAULT_IGNORED_RECOVERY_POLICY.maxFileBytes,
    );
    assert.throws(
      () => validatedIgnoredRecoveryPolicy({ maxFileBytes: DEFAULT_IGNORED_RECOVERY_POLICY.maxFileBytes + 1 }),
      /unproven ignored recovery file expansion is blocked/,
    );
    assertions.push("ignored_recovery_max_file_bytes_boundary_enforced");
    assert.equal(
      validatedIgnoredRecoveryPolicy({ minimumFreePercent: 10 }).minimumFreePercent,
      DEFAULT_IGNORED_RECOVERY_POLICY.minimumFreePercent,
    );
    assert.throws(
      () => validatedIgnoredRecoveryPolicy({ minimumFreePercent: 9 }),
      /ignored recovery free-space floor cannot be weakened/,
    );
    assertions.push("ignored_recovery_minimum_free_percent_boundary_enforced");
    assert.equal(
      validatedIgnoredRecoveryPolicy({ retentionMs: DEFAULT_IGNORED_RECOVERY_POLICY.retentionMs }).retentionMs,
      DEFAULT_IGNORED_RECOVERY_POLICY.retentionMs,
    );
    assert.throws(
      () => validatedIgnoredRecoveryPolicy({ retentionMs: DEFAULT_IGNORED_RECOVERY_POLICY.retentionMs + 1 }),
      /unproven recovery retention expansion is blocked/,
    );
    assertions.push("ignored_recovery_retention_boundary_enforced");
    assertions.push("ignored_recovery_disk_pressure_refused");
    assert.equal(git(cleanPath, ["status", "--porcelain=v1", "--untracked-files=all"]), "");
    const oversizedIgnoredFile = join(cleanIgnoredDirectory, "oversized.bin");
    writeFileSync(oversizedIgnoredFile, "");
    truncateSync(oversizedIgnoredFile, cleanManifest.ignoredRecoveryPolicy.maxFileBytes + 1);
    let oversizedError;
    try {
      reconcileCleanup(cleanManifest);
      assert.fail("oversized ignored material must stop before reading");
    } catch (error) {
      assert(error instanceof NeedsYouError);
      oversizedError = error;
    }
    assert.equal(oversizedError.message, "ignored file exceeds the automatic preservation policy");
    assert(!oversizedError.message.includes("oversized.bin"));
    assert.equal(readState(cleanManifest).ignoredRecovery, null);
    assert(existsSync(oversizedIgnoredFile));
    assert(!existsSync(cleanManifest.ignoredArtifactPath));
    rmSync(oversizedIgnoredFile);
    utimesSync(cleanIgnoredDirectory, ignoredTimestamp, ignoredTimestamp);
    assertions.push("oversized_sparse_ignored_needs_you_before_read");

    mkdirSync(cleanManifest.ignoredArtifactPath, { recursive: true, mode: 0o700 });
    restrictRecoveryDirectory(cleanManifest.ignoredArtifactPath);
    writeRestrictedFile(join(cleanManifest.ignoredArtifactPath, "owner.json"), `${JSON.stringify({ foreign: true })}\n`);
    writeRestrictedFile(join(cleanManifest.ignoredArtifactPath, "foreign-sentinel.txt"), "foreign owner data\n");
    assert.throws(
      () => reconcileCleanup(cleanManifest),
      (error) => error instanceof NeedsYouError
        && error.message === "ignored recovery artifact ownership or content is unverified",
    );
    assert.equal(readFileSync(join(cleanManifest.ignoredArtifactPath, "foreign-sentinel.txt"), "utf8"), "foreign owner data\n");
    assert(existsSync(cleanManifest.worktreePath));
    rmSync(cleanManifest.ignoredArtifactPath, { recursive: true });
    assertions.push("foreign_owner_recovery_squat_needs_you");

    assert.throws(() => reconcileCleanup(cleanManifest, { interruptAfterIgnoredArtifact: true }), ExpectedInterruption);
    let cleanState = JSON.parse(readFileSync(cleanManifest.intentPath, "utf8"));
    assert.equal(cleanState.phase, "intent_recorded");
    assert.equal(cleanState.ignoredRecovery.status, "intent_recorded");
    assert(cleanState.ignoredRecovery.entryCount > 8);
    assert.equal(cleanManifest.ignoredRecoveryPolicy.maxEntries, 10_000);
    assert.equal(cleanManifest.ignoredRecoveryPolicy.streamChunkBytes, 64 * 1024);
    assert(cleanManifest.ignoredRecoveryPolicy.maxBytes >= 2 * 1024 * 1024 * 1024);
    assert(existsSync(cleanManifest.ignoredArtifactPath));
    assert(existsSync(cleanManifest.worktreePath));
    assert(!JSON.stringify(cleanState).includes("private-token.txt"));
    assert(!JSON.stringify(cleanState).includes("ignored-dir"));
    verifyIgnoredArtifact(cleanManifest, cleanState);
    assertions.push("ignored_recovery_artifact_effect_crash_reconciled");

    const cleanIgnoredPaths = inspectMaterialSafety(cleanPath, cleanManifest.ignoredRecoveryPolicy).ignoredPaths;
    ensureIgnoredRecovery(cleanManifest, cleanState, cleanPath, cleanIgnoredPaths, {});
    cleanState = JSON.parse(readFileSync(cleanManifest.intentPath, "utf8"));
    assert.equal(cleanState.ignoredRecovery.status, "artifact_verified");
    const parkedIgnoredArtifact = join(root, "ignored-artifact-owned-parked");
    renameSync(cleanManifest.ignoredArtifactPath, parkedIgnoredArtifact);
    mkdirSync(cleanManifest.ignoredArtifactPath, { mode: 0o700 });
    writeFileSync(join(cleanManifest.ignoredArtifactPath, "unknown-sentinel.txt"), "preserve unknown artifact\n");
    assert.throws(
      () => reconcileCleanup(cleanManifest),
      (error) => error instanceof NeedsYouError
        && error.message === "ignored recovery artifact ownership or content is unverified",
    );
    assert.equal(readFileSync(join(cleanManifest.ignoredArtifactPath, "unknown-sentinel.txt"), "utf8"), "preserve unknown artifact\n");
    assert(existsSync(cleanManifest.worktreePath));
    rmSync(cleanManifest.ignoredArtifactPath, { recursive: true });
    renameSync(parkedIgnoredArtifact, cleanManifest.ignoredArtifactPath);
    verifyIgnoredArtifact(cleanManifest, cleanState);
    assertions.push("ignored_recovery_replacement_refused");

    writeFileSync(cleanIgnoredFile, "Ignored secret-like fixture bytes\n");
    chmodSync(cleanIgnoredFile, originalIgnoredMetadata.fileMode);
    utimesSync(cleanIgnoredFile, ignoredTimestamp, ignoredTimestamp);
    let contentChangeError;
    try {
      ensureIgnoredRecovery(cleanManifest, cleanState, cleanPath, cleanIgnoredPaths, {});
      assert.fail("changed ignored bytes must stop cleanup");
    } catch (error) {
      assert(error instanceof NeedsYouError);
      contentChangeError = error;
    }
    assert.equal(contentChangeError.message, "ignored material content changed after preservation intent");
    assert(!contentChangeError.message.includes("private-token.txt"));
    assert(!contentChangeError.message.includes(cleanState.ignoredRecovery.contentDigest));
    writeFileSync(cleanIgnoredFile, "ignored secret-like fixture bytes\n");
    chmodSync(cleanIgnoredFile, originalIgnoredMetadata.fileMode);
    utimesSync(cleanIgnoredFile, ignoredTimestamp, ignoredTimestamp);
    assertions.push("ignored_content_change_path_free_needs_you");

    const retryMinimum = BigInt(cleanState.ignoredRecovery.minimumFreeBytes);
    ensureIgnoredRecovery(
      cleanManifest,
      cleanState,
      cleanPath,
      cleanIgnoredPaths,
      { availableBytesOverride: retryMinimum + 1n },
    );
    assert.equal(cleanState.ignoredRecovery.bytesStillToWrite, "0");
    assert(existsSync(cleanManifest.ignoredArtifactPath));
    assert(existsSync(cleanManifest.worktreePath));
    assertions.push("ignored_recovery_retry_charges_only_remaining_bytes");

    assert.throws(() => reconcileCleanup(cleanManifest, { interruptAfterRemovalReady: true }), ExpectedInterruption);
    cleanState = readState(cleanManifest);
    assert.equal(cleanState.phase, "removal_ready");
    const ignoredPayloadPath = join(cleanManifest.ignoredArtifactPath, "payload", "private-token.txt");
    const tokenBindingPass = beginDestructivePass();
    const tokenBindingEffect = { kind: "worktree_move", activePath: cleanManifest.worktreePath };
    const tokenBinding = issueDestructiveGateToken(cleanManifest, cleanState, tokenBindingPass, tokenBindingEffect);
    assert.throws(
      () => runGuardedDestructiveGit(
        tokenBindingPass, tokenBinding, cleanManifest.sourcePath,
        ["worktree", "remove", "--force", "--", cleanManifest.worktreePath],
      ),
      (error) => error instanceof NeedsYouError
        && error.message === "destructive command differs from its recovery-evidence gate",
    );
    assert.throws(
      () => runGuardedDestructiveGit(
        tokenBindingPass, tokenBinding, cleanManifest.sourcePath,
        ["worktree", "move", "--", cleanManifest.worktreePath, cleanManifest.quarantinePath],
      ),
      (error) => error instanceof NeedsYouError
        && error.message === "destructive command lacks a fresh recovery-evidence gate",
    );
    assert(existsSync(cleanManifest.worktreePath));
    assert(!existsSync(cleanManifest.quarantinePath));
    assertions.push("destructive_gate_token_binds_exact_command_once");
    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_artifact_before_move_table",
      cleanManifest,
      artifactEnvelopeEvidenceCases(cleanManifest, "clean-before-move", ignoredPayloadPath),
      (current) => {
        assert.equal(current.phase, "removal_ready");
        assert(existsSync(cleanManifest.worktreePath));
        assert(!existsSync(cleanManifest.quarantinePath));
        assert.equal(readFileSync(cleanIgnoredFile, "utf8"), "ignored secret-like fixture bytes\n");
        assertOwnedRegistration(cleanManifest, current, cleanManifest.worktreePath);
        assert.equal(observeLocalRef(cleanManifest, current, CLEAN_REF).sha, candidateSha);
        assert.equal(queryOwnedRemoteRef(cleanManifest, current, CLEAN_REF).sha, candidateSha);
      },
    );
    cleanState = readState(cleanManifest);
    verifyIgnoredArtifact(cleanManifest, cleanState);

    assert.throws(() => reconcileCleanup(cleanManifest, { interruptAfterQuarantineMove: true }), ExpectedInterruption);
    cleanState = readState(cleanManifest);
    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_artifact_before_remove_table",
      cleanManifest,
      artifactEnvelopeEvidenceCases(cleanManifest, "clean-before-remove", ignoredPayloadPath),
      (current) => {
        assert.equal(current.phase, "removal_ready");
        assert(!existsSync(cleanManifest.worktreePath));
        assert(existsSync(cleanManifest.quarantinePath));
        assertOwnedRegistration(cleanManifest, current, cleanManifest.quarantinePath);
        assert.equal(observeLocalRef(cleanManifest, current, CLEAN_REF).sha, candidateSha);
        assert.equal(queryOwnedRemoteRef(cleanManifest, current, CLEAN_REF).sha, candidateSha);
      },
    );

    assert.throws(() => reconcileCleanup(cleanManifest, { interruptAfterWorktreeRemoval: true }), ExpectedInterruption);
    cleanState = JSON.parse(readFileSync(cleanManifest.intentPath, "utf8"));
    assert.equal(cleanState.phase, "removal_ready");
    assert.equal(cleanState.removalReady.clean, true);
    assert.equal(cleanState.ignoredRecovery.status, "artifact_verified");
    assert.equal(cleanState.removalReady.ignoredRecoveryManifestSha256, cleanState.ignoredRecovery.manifestSha256);
    assert(!existsSync(cleanManifest.worktreePath));
    assert(!existsSync(cleanManifest.quarantinePath));
    assertions.push("clean_removal_ready_crash_reconciled");

    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_artifact_before_local_delete_table",
      cleanManifest,
      artifactEnvelopeEvidenceCases(cleanManifest, "clean-before-local-delete", ignoredPayloadPath),
      (current) => {
        assert.equal(current.phase, "local_delete_refused");
        assert.equal(current.localDelete.status, "refused_before_dispatch");
        assert(!existsSync(cleanManifest.worktreePath));
        assert(!existsSync(cleanManifest.quarantinePath));
        assertOwnedRegistrationsAbsent(cleanManifest, current);
        assert.equal(observeLocalRef(cleanManifest, current, CLEAN_REF).sha, candidateSha);
        assert.equal(queryOwnedRemoteRef(cleanManifest, current, CLEAN_REF).sha, candidateSha);
      },
    );

    const ignoredRestorePath = join(root, "ignored-restored");
    restoreIgnoredRecovery(cleanManifest, cleanState, ignoredRestorePath);
    assert.equal(readFileSync(join(ignoredRestorePath, "private-token.txt"), "utf8"), "ignored secret-like fixture bytes\n");
    assert.equal(readFileSync(join(ignoredRestorePath, "ignored-dir", "value.txt"), "utf8"), "ignored directory fixture bytes\n");
    assert(lstatSync(join(ignoredRestorePath, "ignored-dir", "empty")).isDirectory());
    assert.equal(readdirSync(join(ignoredRestorePath, "ignored-dir", "realistic-tree")).length, 8);
    const restoredFile = statSync(join(ignoredRestorePath, "private-token.txt"));
    const restoredNested = statSync(join(ignoredRestorePath, "ignored-dir", "value.txt"));
    const restoredDirectory = statSync(join(ignoredRestorePath, "ignored-dir"));
    const restoredEmptyDirectory = statSync(join(ignoredRestorePath, "ignored-dir", "empty"));
    if (platform() !== "win32") {
      assert.equal(restoredFile.mode & 0o777, originalIgnoredMetadata.fileMode);
      assert.equal(restoredNested.mode & 0o777, originalIgnoredMetadata.nestedMode);
      assert.equal(restoredDirectory.mode & 0o777, originalIgnoredMetadata.directoryMode);
      assert.equal(restoredEmptyDirectory.mode & 0o777, originalIgnoredMetadata.emptyDirectoryMode);
    }
    assert.equal(Math.trunc(restoredFile.mtimeMs), originalIgnoredMetadata.fileMtimeMs);
    assert.equal(Math.trunc(restoredNested.mtimeMs), originalIgnoredMetadata.nestedMtimeMs);
    assert.equal(Math.trunc(restoredDirectory.mtimeMs), originalIgnoredMetadata.directoryMtimeMs);
    assert.equal(Math.trunc(restoredEmptyDirectory.mtimeMs), originalIgnoredMetadata.emptyDirectoryMtimeMs);
    rmSync(ignoredRestorePath, { recursive: true });
    assertions.push("clean_ignored_material_preserved_removed_and_restored");

    const activeConsumerPath = join(worktreeRoot, "unknown-active-consumer");
    git(source, ["worktree", "add", "--", activeConsumerPath, CLEAN_REF.slice("refs/heads/".length)]);
    writeFileSync(join(activeConsumerPath, "consumer-sentinel.txt"), "must remain intact\n");
    const consumerRegistration = worktreeRegistrations(cleanManifest, cleanState)
      .find((registration) => registration.comparablePath === comparablePath(activeConsumerPath));
    assert(consumerRegistration, "active consumer registration was not found");
    assert.equal(consumerRegistration.branch, CLEAN_REF);
    assert.equal(consumerRegistration.head, candidateSha);
    assert.throws(() => reconcileCleanup(cleanManifest), /Task ref has another registered worktree consumer/);
    assert.equal(git(source, ["rev-parse", CLEAN_REF]), candidateSha);
    assert.equal(git(activeConsumerPath, ["symbolic-ref", "-q", "HEAD"]), CLEAN_REF);
    assert.equal(git(activeConsumerPath, ["rev-parse", "HEAD"]), candidateSha);
    assert.equal(readFileSync(join(activeConsumerPath, "candidate.txt"), "utf8"), "candidate\n");
    assert.equal(readFileSync(join(activeConsumerPath, "consumer-sentinel.txt"), "utf8"), "must remain intact\n");
    git(source, ["worktree", "remove", "--force", "--", activeConsumerPath]);
    assert.equal(git(source, ["rev-parse", CLEAN_REF]), candidateSha);
    assertions.push("active_task_ref_consumer_refused");

    const sourceParked = join(root, "source-owned-parked");
    renameSync(source, sourceParked);
    git(root, ["clone", "--", remote, source]);
    git(source, ["update-ref", CLEAN_REF, candidateSha, ZERO_SHA]);
    assert.throws(() => reconcileCleanup(cleanManifest), /source identity changed/);
    assert.equal(git(source, ["rev-parse", CLEAN_REF]), candidateSha);
    rmSync(source, { recursive: true, force: true });
    renameSync(sourceParked, source);
    assertRepositoryOwnership(cleanManifest, cleanState);
    assertions.push("source_replacement_after_removal_refused");

    const commonPath = join(source, ".git");
    const commonParked = join(root, "common-owned-parked");
    renameSync(commonPath, commonParked);
    git(source, ["init"]);
    git(source, ["remote", "add", "origin", remote]);
    git(source, ["fetch", "--no-tags", "--no-write-fetch-head", "origin", candidateSha]);
    git(source, ["update-ref", CLEAN_REF, candidateSha, ZERO_SHA]);
    assert.throws(() => reconcileCleanup(cleanManifest), /Git common-directory identity changed|Git common directory changed/);
    assert.equal(git(source, ["rev-parse", CLEAN_REF]), candidateSha);
    rmSync(commonPath, { recursive: true, force: true });
    renameSync(commonParked, commonPath);
    assertRepositoryOwnership(cleanManifest, cleanState);
    assertions.push("common_dir_replacement_after_removal_refused");

    const unavailableRemoteParked = join(root, "remote-unavailable-parked.git");
    renameSync(remote, unavailableRemoteParked);
    assert.throws(() => reconcileCleanup(cleanManifest), ExternalUnavailableError);
    assert.equal(git(source, ["rev-parse", CLEAN_REF]), candidateSha);
    renameSync(unavailableRemoteParked, remote);
    assertRepositoryOwnership(cleanManifest, cleanState);
    assertions.push("origin_unavailable_classified");

    const remoteParked = join(root, "remote-owned-parked.git");
    renameSync(remote, remoteParked);
    git(root, ["init", "--bare", "--initial-branch=main", remote]);
    git(source, ["push", "origin", `${candidateSha}:${MAIN_REF}`, `${candidateSha}:${CLEAN_REF}`]);
    assert.throws(() => reconcileCleanup(cleanManifest), /origin identity changed/);
    assert.equal(git(remote, ["rev-parse", CLEAN_REF]), candidateSha);
    rmSync(remote, { recursive: true, force: true });
    renameSync(remoteParked, remote);
    assertRepositoryOwnership(cleanManifest, cleanState);
    assertions.push("origin_replacement_after_removal_refused");

    git(remote, ["update-ref", MAIN_REF, baseSha, candidateSha]);
    assert.throws(() => reconcileCleanup(cleanManifest), /Candidate is not integrated in the live base/);
    assert.equal(git(source, ["rev-parse", CLEAN_REF]), candidateSha);
    assert.equal(git(remote, ["rev-parse", CLEAN_REF]), candidateSha);
    git(remote, ["update-ref", MAIN_REF, candidateSha, baseSha]);
    assertions.push("live_base_rewrite_refused");

    cleanState = readState(cleanManifest);
    assert.throws(
      () => removeLocalRefExactly(cleanManifest, cleanState, { interruptAfterLocalDelete: true }),
      ExpectedInterruption,
    );
    cleanState = readState(cleanManifest);
    assert.equal(cleanState.phase, "local_delete_ready");
    assert.equal(cleanState.localDelete.status, "intent_recorded");
    assert.equal(observeLocalRef(cleanManifest, cleanState, CLEAN_REF).kind, "absent");
    assert.equal(queryOwnedRemoteRef(cleanManifest, cleanState, CLEAN_REF).sha, candidateSha);
    removeLocalRefExactly(cleanManifest, cleanState);
    assert.equal(cleanState.phase, "local_ref_removed");
    assert.equal(cleanState.localDelete.status, "confirmed_absent");
    assert.equal(observeLocalRef(cleanManifest, cleanState, CLEAN_REF).kind, "absent");
    assert.equal(queryOwnedRemoteRef(cleanManifest, cleanState, CLEAN_REF).sha, candidateSha);
    assertions.push("local_delete_effect_retry_reconciled");

    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_artifact_before_remote_delete_table",
      cleanManifest,
      artifactEnvelopeEvidenceCases(cleanManifest, "clean-before-remote-delete", ignoredPayloadPath),
      (current) => {
        assert.equal(current.phase, "remote_delete_refused");
        assert.equal(current.remoteDelete.status, "refused_before_dispatch");
        assert(!existsSync(cleanManifest.worktreePath));
        assert(!existsSync(cleanManifest.quarantinePath));
        assertOwnedRegistrationsAbsent(cleanManifest, current);
        assert.equal(observeLocalRef(cleanManifest, current, CLEAN_REF).kind, "absent");
        assert.equal(queryOwnedRemoteRef(cleanManifest, current, CLEAN_REF).sha, candidateSha);
      },
    );

    cleanState = readState(cleanManifest);
    const stateBeforeLeaseFault = structuredClone(cleanState);
    assert.throws(() => removeRemoteRefExactly(cleanManifest, cleanState, {
      beforeLeasePush: () => git(source, ["push", "--force", "origin", `${racedSha}:${CLEAN_REF}`]),
    }), /remote expected-head deletion failed/);
    assert.equal(git(remote, ["rev-parse", CLEAN_REF]), racedSha);
    assert.equal(readState(cleanManifest).remoteDelete.status, "intent_recorded");
    assertions.push("force_with_lease_race_refused");
    git(source, ["push", "--force", "origin", `${candidateSha}:${CLEAN_REF}`]);
    cleanState = stateBeforeLeaseFault;
    writeState(cleanManifest, cleanState);

    assert.throws(() => reconcileCleanup(cleanManifest, { interruptAfterRemoteDelete: true }), ExpectedInterruption);
    cleanState = JSON.parse(readFileSync(cleanManifest.intentPath, "utf8"));
    assert.equal(cleanState.phase, "remote_delete_ready");
    assert.equal(cleanState.remoteDelete.status, "intent_recorded");
    assert.equal(queryRemoteRef(source, "origin", CLEAN_REF).kind, "absent");
    git(source, ["push", "origin", `${candidateSha}:${CLEAN_REF}`]);
    assert.throws(() => reconcileCleanup(cleanManifest), /remote Task ref recreation or prior deletion is ambiguous/);
    assert.equal(queryRemoteRef(source, "origin", CLEAN_REF).sha, candidateSha);
    git(source, ["push", `--force-with-lease=${CLEAN_REF}:${candidateSha}`, "origin", `:${CLEAN_REF}`]);
    assertions.push("remote_same_sha_recreation_needs_you");
    const cleanCompleted = reconcileCleanup(cleanManifest);
    assert.equal(cleanCompleted.phase, "complete");
    assert.equal(reconcileCleanup(cleanManifest).phase, "complete");
    assertions.push("remote_delete_effect_retry_reconciled");

    git(source, ["update-ref", CLEAN_REF, candidateSha, ZERO_SHA]);
    git(source, ["push", "origin", `${candidateSha}:${CLEAN_REF}`]);
    assert.throws(
      () => reconcileCleanup(cleanManifest),
      (error) => error instanceof NeedsYouError
        && error.message === "completed cleanup ref recreation is ambiguous",
    );
    const terminalState = readState(cleanManifest);
    assert.equal(terminalState.phase, "complete");
    assert.equal(terminalState.localDelete.status, "confirmed_absent");
    assert.equal(terminalState.remoteDelete.status, "confirmed_absent");
    assert.equal(observeLocalRef(cleanManifest, terminalState, CLEAN_REF).sha, candidateSha);
    assert.equal(queryOwnedRemoteRef(cleanManifest, terminalState, CLEAN_REF).sha, candidateSha);
    git(source, ["update-ref", "-d", CLEAN_REF, candidateSha]);
    git(source, [
      "push", `--force-with-lease=${CLEAN_REF}:${candidateSha}`,
      "origin", `:${CLEAN_REF}`,
    ]);
    assert.equal(reconcileCleanup(cleanManifest).phase, "complete");
    assertions.push("completed_ref_recreation_needs_you");

    assert.deepEqual(
      cleanupIgnoredRecovery(cleanManifest, cleanCompleted, cleanCompleted.ignoredRecovery.retentionUntilMs - 1),
      { status: "retained", retentionUntilMs: cleanCompleted.ignoredRecovery.retentionUntilMs },
    );
    assert(existsSync(cleanManifest.ignoredArtifactPath));
    assert.throws(() => cleanupIgnoredRecovery(
      cleanManifest,
      cleanCompleted,
      cleanCompleted.ignoredRecovery.retentionUntilMs + 1,
      { interruptAfterArtifactRemoval: true },
    ), ExpectedInterruption);
    let retainedState = JSON.parse(readFileSync(cleanManifest.intentPath, "utf8"));
    assert.equal(retainedState.ignoredRecovery.cleanupPhase, "removal_ready");
    assert(!existsSync(cleanManifest.ignoredArtifactPath));
    cleanupIgnoredRecovery(cleanManifest, retainedState, retainedState.ignoredRecovery.retentionUntilMs + 1);
    retainedState = JSON.parse(readFileSync(cleanManifest.intentPath, "utf8"));
    assert.equal(retainedState.ignoredRecovery.cleanupPhase, "removed");
    cleanupIgnoredRecovery(cleanManifest, retainedState, retainedState.ignoredRecovery.retentionUntilMs + 1);
    assert(!existsSync(cleanManifest.ignoredStagingPath));
    assertions.push("ignored_recovery_retention_cleanup_idempotent");

    const cleanLockPath = join(worktreeRoot, "clean-lock-task");
    git(source, ["worktree", "add", "-b", CLEAN_LOCK_REF.slice("refs/heads/".length), "--", cleanLockPath, candidateSha]);
    git(cleanLockPath, ["push", "-u", "origin", CLEAN_LOCK_REF.slice("refs/heads/".length)]);
    const cleanLockManifest = makeManifest({
      taskId: "dir-m0.6", runId: "clean-lock-run", nonce: "clean-lock-nonce", sourcePath: source,
      worktreeRoot, worktreePath: cleanLockPath, taskRef: CLEAN_LOCK_REF, baseSha, candidateSha,
      recoveryRef: CLEAN_LOCK_RECOVERY_REF, ignoredRecoveryRoot, stateDir: cleanLockStateDir,
      localOriginAuthority: DISPOSABLE_LOCAL_ORIGIN_AUTHORITY,
    });
    assert.throws(() => reconcileCleanup(cleanLockManifest, { interruptAfterRemovalReady: true }), ExpectedInterruption);
    let cleanLockState = JSON.parse(readFileSync(cleanLockManifest.intentPath, "utf8"));
    assert.equal(cleanLockState.phase, "removal_ready");
    assert.equal(cleanLockState.removalReady.clean, true);
    assert.equal(cleanLockState.removalReady.snapshotSha, null);
    assertOwnedRegistration(cleanLockManifest, cleanLockState, cleanLockManifest.worktreePath);
    assertions.push("clean_lock_removal_ready_persisted");

    const cleanTrackedMetadata = statSync(join(cleanLockPath, "tracked.txt"));
    writeFileSync(join(cleanLockPath, "tracked.txt"), "edit\n");
    utimesSync(
      join(cleanLockPath, "tracked.txt"),
      cleanTrackedMetadata.atime,
      cleanTrackedMetadata.mtime,
    );
    let sameMetadataError;
    try {
      reconcileCleanup(cleanLockManifest);
      assert.fail("same-metadata edit must stop removal-ready resume");
    } catch (error) {
      sameMetadataError = error;
    }
    assert(sameMetadataError instanceof NeedsYouError);
    assert.equal(sameMetadataError.message, "pre-destructive recovery evidence is unverified");
    assert.equal(readFileSync(join(cleanLockPath, "tracked.txt"), "utf8"), "edit\n");
    cleanLockState = readState(cleanLockManifest);
    assert.equal(cleanLockState.phase, "removal_ready");
    assertOwnedRegistration(cleanLockManifest, cleanLockState, cleanLockManifest.worktreePath);
    assert.equal(observeLocalRef(cleanLockManifest, cleanLockState, CLEAN_LOCK_REF).sha, candidateSha);
    assert.equal(queryOwnedRemoteRef(cleanLockManifest, cleanLockState, CLEAN_LOCK_REF).sha, candidateSha);
    writeFileSync(join(cleanLockPath, "tracked.txt"), "base\n");
    utimesSync(
      join(cleanLockPath, "tracked.txt"),
      cleanTrackedMetadata.atime,
      cleanTrackedMetadata.mtime,
    );
    assertions.push("removal_ready_same_metadata_edit_refused");

    if (platform() === "win32") {
      const stateBeforeCleanLock = JSON.parse(readFileSync(cleanLockManifest.intentPath, "utf8"));
      const readyPath = join(cleanLockStateDir, "lock-ready");
      let removalLockError;
      try {
        reconcileCleanup(cleanLockManifest, {
          beforeWorktreeMove: () => {
            lockChild = windowsExclusiveLock(join(cleanLockPath, "tracked.txt"), readyPath);
            waitForFile(readyPath, lockChild);
          },
        });
        assert.fail("FileShare.None must stop the clean worktree move");
      } catch (error) {
        removalLockError = error;
      }
      assert(removalLockError instanceof GitCommandError);
      assert.equal(removalLockError.message, "git worktree move exited 128");
      cleanLockState = JSON.parse(readFileSync(cleanLockManifest.intentPath, "utf8"));
      assert.deepEqual(cleanLockState, stateBeforeCleanLock, "clean lock failure changed durable intent");
      assert.equal(cleanLockState.phase, "removal_ready");
      assert(existsSync(cleanLockManifest.worktreePath));
      assert(!existsSync(cleanLockManifest.quarantinePath));
      assertOwnedRegistration(cleanLockManifest, cleanLockState, cleanLockManifest.worktreePath);
      assert.equal(statSync(join(cleanLockManifest.worktreePath, "tracked.txt")).size, 5);
      assert.equal(readFileSync(join(cleanLockManifest.worktreePath, "candidate.txt"), "utf8"), "candidate\n");
      assert.equal(git(source, ["rev-parse", CLEAN_LOCK_REF]), candidateSha);
      assert.equal(queryRemoteRef(source, "origin", CLEAN_LOCK_REF).sha, candidateSha);
      assert.equal(git(source, ["show", `${candidateSha}:tracked.txt`]), "base");
      await stopChild(lockChild);
      lockChild = undefined;
      waitUntilUnlocked(join(cleanLockManifest.worktreePath, "tracked.txt"));
      assert.equal(readFileSync(join(cleanLockManifest.worktreePath, "tracked.txt"), "utf8"), "base\n");
      assertions.push("windows_clean_removal_lock_fail_closed");
    }

    assert.throws(() => reconcileCleanup(cleanLockManifest, {
      interruptAfterWorktreeRemoval: true,
      beforeWorktreeMove: platform() === "win32" ? undefined : () => {
        cleanHeldFd = openSync(join(cleanLockManifest.worktreePath, "tracked.txt"), "r");
      },
    }), ExpectedInterruption);
    cleanLockState = JSON.parse(readFileSync(cleanLockManifest.intentPath, "utf8"));
    assert.equal(cleanLockState.phase, "removal_ready");
    assert(!existsSync(cleanLockManifest.worktreePath));
    assert(!existsSync(cleanLockManifest.quarantinePath));
    assertOwnedRegistrationsAbsent(cleanLockManifest, cleanLockState);
    if (cleanHeldFd !== undefined) {
      const cleanBuffer = Buffer.alloc(5);
      const cleanBytes = readSync(cleanHeldFd, cleanBuffer, 0, cleanBuffer.length, 0);
      assert.equal(cleanBuffer.subarray(0, cleanBytes).toString(), "base\n");
      closeSync(cleanHeldFd);
      cleanHeldFd = undefined;
      assertions.push("linux_clean_open_handle_removal");
    }
    const cleanLockCompleted = reconcileCleanup(cleanLockManifest);
    assert.equal(cleanLockCompleted.phase, "complete");
    assert.equal(reconcileCleanup(cleanLockManifest).phase, "complete");
    assert.equal(observeLocalRef(cleanLockManifest, cleanLockCompleted, CLEAN_LOCK_REF).kind, "absent");
    assert.equal(queryOwnedRemoteRef(cleanLockManifest, cleanLockCompleted, CLEAN_LOCK_REF).kind, "absent");
    assertions.push("clean_lock_retry_completed");

    writeFileSync(join(taskPath, "tracked.txt"), "dirty tracked\n");
    writeFileSync(join(taskPath, "untracked.txt"), "dirty untracked\n");
    writeFileSync(join(taskPath, "staged.txt"), "dirty staged\n");
    git(taskPath, ["add", "--", "staged.txt"]);
    mkdirSync(join(taskPath, "nested"));
    writeFileSync(join(taskPath, "nested", "untracked.txt"), "nested dirty\n");
    mkdirSync(join(taskPath, "empty-untracked"));

    writeFileSync(join(taskPath, "private-token.txt"), "fixture secret-like material\n");
    mkdirSync(join(taskPath, "ignored-dir"));
    writeFileSync(join(taskPath, "ignored-dir", "value.txt"), "ignored directory material\n");
    let ignoredError;
    try {
      reconcileCleanup(manifest);
      assert.fail("ignored material must stop cleanup");
    } catch (error) {
      assert(error instanceof NeedsYouError);
      ignoredError = error;
    }
    assert(!ignoredError.message.includes("private-token.txt"));
    assert(!ignoredError.message.includes("value.txt"));
    assert(existsSync(join(taskPath, "private-token.txt")));
    assert(existsSync(join(taskPath, "ignored-dir", "value.txt")));
    assert.equal(observeLocalRef(manifest, state, RECOVERY_REF).kind, "absent");
    rmSync(join(taskPath, "private-token.txt"));
    rmSync(join(taskPath, "ignored-dir"), { recursive: true });
    assertions.push("ignored_material_needs_you");

    writeFileSync(join(taskPath, "private-token.txt"), "coexisting ignored material\n");
    const nestedRepo = join(taskPath, "nested-repository");
    git(taskPath, ["init", "--", nestedRepo]);
    writeFileSync(join(nestedRepo, "uncommitted.txt"), "nested repository material\n");
    const submoduleMarker = join(taskPath, "submodule-marker");
    mkdirSync(submoduleMarker);
    writeFileSync(join(submoduleMarker, ".git"), "gitdir: ../nested-repository/.git\n");
    writeFileSync(join(submoduleMarker, "value.txt"), "submodule-like material\n");
    let nestedError;
    try {
      reconcileCleanup(manifest);
      assert.fail("nested repository material must stop cleanup");
    } catch (error) {
      assert(error instanceof NeedsYouError);
      nestedError = error;
    }
    assert.equal(nestedError.message, "nested repository or submodule requires human recovery");
    assert(!nestedError.message.includes("nested-repository"));
    assert(existsSync(join(nestedRepo, ".git")));
    assert(existsSync(join(nestedRepo, "uncommitted.txt")));
    assert(existsSync(join(submoduleMarker, ".git")));
    assert(existsSync(join(taskPath, "private-token.txt")));
    assert(!existsSync(manifest.ignoredArtifactPath));
    rmSync(nestedRepo, { recursive: true });
    assert.throws(() => reconcileCleanup(manifest), /nested repository or submodule requires human recovery/);
    assert(existsSync(join(submoduleMarker, ".git")));
    rmSync(join(taskPath, "private-token.txt"));
    rmSync(submoduleMarker, { recursive: true });
    assertions.push("nested_repository_and_ignored_coexistence_needs_you");

    const specialIgnoredDirectory = join(taskPath, "ignored-dir");
    mkdirSync(specialIgnoredDirectory);
    const externalSpecialTarget = join(root, "external-special-target.txt");
    writeFileSync(externalSpecialTarget, "external target must remain\n");
    if (platform() !== "win32") {
      const ignoredSymlink = join(specialIgnoredDirectory, "symlink");
      symlinkSync(externalSpecialTarget, ignoredSymlink);
      assert.throws(() => reconcileCleanup(manifest), /symbolic link or reparse point requires human recovery/);
      assert.equal(readFileSync(externalSpecialTarget, "utf8"), "external target must remain\n");
      assert(lstatSync(ignoredSymlink).isSymbolicLink());
      rmSync(ignoredSymlink);
    }
    const specialPath = join(specialIgnoredDirectory, platform() === "win32" ? "reparse" : "fifo");
    if (platform() === "win32") {
      symlinkSync(root, specialPath, "junction");
    } else {
      command("mkfifo", [specialPath]);
    }
    let specialError;
    try {
      reconcileCleanup(manifest);
      assert.fail("special material must stop cleanup");
    } catch (error) {
      assert(error instanceof NeedsYouError);
      specialError = error;
    }
    assert(!specialError.message.includes("ignored-dir"));
    assert(existsSync(specialPath));
    assert(!existsSync(manifest.ignoredArtifactPath));
    rmSync(specialPath, { force: true });
    if (platform() !== "win32") {
      const socketPath = join(specialIgnoredDirectory, "socket");
      const socketReady = join(taskStateDir, "socket-ready");
      specialChild = startUnixSocketFixture(socketPath, socketReady);
      waitForFile(socketReady, specialChild);
      assert.throws(() => reconcileCleanup(manifest), /FIFO, socket, or special material requires human recovery/);
      assert(existsSync(socketPath));
      await stopChild(specialChild);
      specialChild = undefined;
      rmSync(socketPath, { force: true });
    }
    rmSync(specialIgnoredDirectory, { recursive: true });
    assert.equal(readFileSync(externalSpecialTarget, "utf8"), "external target must remain\n");
    rmSync(externalSpecialTarget);
    assertions.push(platform() === "win32"
      ? "windows_reparse_material_needs_you"
      : "linux_symlink_fifo_and_socket_material_needs_you");

    writeFileSync(join(taskPath, "unsafe.payload"), "filter input\n");
    assert.throws(() => reconcileCleanup(manifest), /active Git clean filter requires approved containment/);
    assert(existsSync(join(taskPath, "unsafe.payload")));
    assert(!existsSync(hookSentinel));
    rmSync(join(taskPath, "unsafe.payload"));
    assertions.push("snapshot_filter_refused_and_hooks_disabled");

    assert.throws(() => reconcileCleanup(manifest, { interruptAfterRecoveryRef: true }), ExpectedInterruption);
    const effectOnlyState = JSON.parse(readFileSync(manifest.intentPath, "utf8"));
    assert.equal(effectOnlyState.phase, "intent_recorded");
    assert.equal(effectOnlyState.snapshotSha, null);
    const effectOnlySnapshotSha = observeLocalRef(manifest, effectOnlyState, RECOVERY_REF).sha;
    assertRecoveryContent(manifest, effectOnlySnapshotSha);
    createSnapshot(manifest, effectOnlyState, taskPath, prospectiveTree(manifest, effectOnlyState, taskPath));
    assert.equal(effectOnlyState.snapshotSha, effectOnlySnapshotSha);
    assert(!existsSync(hookSentinel));
    assertions.push("recovery_ref_effect_crash_reconciled");

    writeFileSync(join(taskPath, "late-change.txt"), "must not be lost\n");
    assert.throws(() => reconcileCleanup(manifest), /worktree changed after recovery snapshot/);
    assert(existsSync(join(taskPath, "late-change.txt")));
    assert(!snapshotContains(manifest, effectOnlySnapshotSha, "late-change.txt"));
    rmSync(join(taskPath, "late-change.txt"));
    assertions.push("post_snapshot_change_refused");

    assert.throws(() => reconcileCleanup(manifest, { interruptAfterSnapshot: true }), ExpectedInterruption);
    state = JSON.parse(readFileSync(manifest.intentPath, "utf8"));
    assert.equal(state.phase, "snapshot_verified");
    assertRecoveryContent(manifest, state.snapshotSha);
    assertions.push("dirty_snapshot_verified");
    assert.equal(state.ignoredRecovery.status, "artifact_verified");
    const dirtyEmptyRestore = join(root, "dirty-empty-restored");
    restoreIgnoredRecovery(manifest, state, dirtyEmptyRestore);
    assert(lstatSync(join(dirtyEmptyRestore, "empty-untracked")).isDirectory());
    assert.equal(readdirSync(join(dirtyEmptyRestore, "empty-untracked")).length, 0);
    rmSync(dirtyEmptyRestore, { recursive: true });
    assertions.push("dirty_empty_directory_preserved");

    if (platform() === "win32") {
      const stateBeforeLock = JSON.parse(readFileSync(manifest.intentPath, "utf8"));
      const readyPath = join(taskStateDir, "lock-ready");
      lockChild = windowsExclusiveLock(join(taskPath, "tracked.txt"), readyPath);
      waitForFile(readyPath, lockChild);
      let lockError;
      try {
        reconcileCleanup(manifest);
        assert.fail("FileShare.None must stop snapshot recomputation");
      } catch (error) {
        lockError = error;
      }
      assert(lockError instanceof GitCommandError);
      assert.equal(lockError.message, "git add exited 128");
      state = JSON.parse(readFileSync(manifest.intentPath, "utf8"));
      assert.deepEqual(state, stateBeforeLock, "exclusive-lock failure changed durable intent");
      assert.equal(state.phase, "snapshot_verified");
      const recoverablePath = activeOwnedPath(manifest);
      assert.equal(recoverablePath, manifest.worktreePath);
      assert(!existsSync(manifest.quarantinePath));
      assertOwnedRegistration(manifest, state, recoverablePath);
      assert.equal(statSync(join(recoverablePath, "tracked.txt")).size, 14);
      assert.equal(readFileSync(join(recoverablePath, "untracked.txt"), "utf8"), "dirty untracked\n");
      inspectOwnedWorktree(manifest, recoverablePath, state);
      assert.equal(observeLocalRef(manifest, state, RECOVERY_REF).sha, effectOnlySnapshotSha);
      assertRecoveryContent(manifest, effectOnlySnapshotSha);
      await stopChild(lockChild);
      lockChild = undefined;
      waitUntilUnlocked(join(recoverablePath, "tracked.txt"));
      assert.equal(readFileSync(join(recoverablePath, "tracked.txt"), "utf8"), "dirty tracked\n");
      assertions.push("windows_dirty_snapshot_lock_fail_closed");
    } else {
      heldFd = openSync(join(taskPath, "tracked.txt"), "r");
    }

    assert.throws(() => reconcileCleanup(manifest, { interruptAfterRemovalReady: true }), ExpectedInterruption);
    state = readState(manifest);
    assert.equal(state.phase, "removal_ready");
    assert(state.snapshotSha);
    assert(state.indexSnapshotSha);
    assert.equal(state.ignoredRecovery.status, "artifact_verified");
    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_before_move_table",
      manifest,
      gitRecoveryEvidenceCases(manifest, "before-move"),
      (current) => {
        assert.equal(current.phase, "removal_ready");
        assert(existsSync(manifest.worktreePath));
        assert(!existsSync(manifest.quarantinePath));
        assertOwnedRegistration(manifest, current, manifest.worktreePath);
        assert.equal(observeLocalRef(manifest, current, TASK_REF).sha, candidateSha);
        assert.equal(queryOwnedRemoteRef(manifest, current, TASK_REF).sha, candidateSha);
      },
    );

    assert.throws(() => reconcileCleanup(manifest, { interruptAfterQuarantineMove: true }), ExpectedInterruption);
    state = JSON.parse(readFileSync(manifest.intentPath, "utf8"));
    assert.equal(state.phase, "removal_ready");
    assert(!existsSync(manifest.worktreePath));
    assert(existsSync(manifest.quarantinePath));
    assertOwnedRegistration(manifest, state, manifest.quarantinePath);
    repairedDestructiveStages.push("worktree_move");
    writeFileSync(join(manifest.quarantinePath, "late-quarantine-change.txt"), "must survive refusal\n");
    assert.throws(() => reconcileCleanup(manifest), /pre-destructive recovery evidence is unverified/);
    assert.equal(readFileSync(join(manifest.quarantinePath, "late-quarantine-change.txt"), "utf8"), "must survive refusal\n");
    assert.equal(observeLocalRef(manifest, state, RECOVERY_REF).sha, effectOnlySnapshotSha);
    rmSync(join(manifest.quarantinePath, "late-quarantine-change.txt"));
    assertions.push("quarantine_move_effect_crash_reconciled");

    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_before_remove_table",
      manifest,
      gitRecoveryEvidenceCases(manifest, "before-remove"),
      (current) => {
        assert.equal(current.phase, "removal_ready");
        assert(!existsSync(manifest.worktreePath));
        assert(existsSync(manifest.quarantinePath));
        assertOwnedRegistration(manifest, current, manifest.quarantinePath);
        assert.equal(observeLocalRef(manifest, current, TASK_REF).sha, candidateSha);
        assert.equal(queryOwnedRemoteRef(manifest, current, TASK_REF).sha, candidateSha);
      },
    );

    assert.throws(() => reconcileCleanup(manifest, { interruptAfterWorktreeRemoval: true }), ExpectedInterruption);
    state = JSON.parse(readFileSync(manifest.intentPath, "utf8"));
    assert.equal(state.phase, "removal_ready");
    assert(!existsSync(manifest.worktreePath));
    assert(!existsSync(manifest.quarantinePath));
    assert.equal(observeLocalRef(manifest, state, RECOVERY_REF).sha, effectOnlySnapshotSha);
    repairedDestructiveStages.push("worktree_remove");
    assertions.push("worktree_removal_effect_crash_reconciled");

    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_before_local_delete_table",
      manifest,
      gitRecoveryEvidenceCases(manifest, "before-local-delete"),
      (current) => {
        assert.equal(current.phase, "local_delete_refused");
        assert.equal(current.localDelete.status, "refused_before_dispatch");
        assert(!existsSync(manifest.worktreePath));
        assert(!existsSync(manifest.quarantinePath));
        assertOwnedRegistrationsAbsent(manifest, current);
        assert.equal(observeLocalRef(manifest, current, TASK_REF).sha, candidateSha);
        assert.equal(queryOwnedRemoteRef(manifest, current, TASK_REF).sha, candidateSha);
      },
    );
    if (heldFd !== undefined) {
      const buffer = Buffer.alloc(13);
      const bytes = readSync(heldFd, buffer, 0, buffer.length, 0);
      assert.equal(buffer.subarray(0, bytes).toString(), "dirty tracked");
      closeSync(heldFd);
      heldFd = undefined;
      assertions.push("linux_dirty_open_handle_recovery");
    }

    state = readState(manifest);
    removeLocalRefExactly(manifest, state);
    assert.equal(state.localDelete.status, "confirmed_absent");
    assert.equal(observeLocalRef(manifest, state, TASK_REF).kind, "absent");
    assert.equal(queryOwnedRemoteRef(manifest, state, TASK_REF).sha, candidateSha);
    repairedDestructiveStages.push("local_ref_delete");
    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_before_remote_delete_table",
      manifest,
      gitRecoveryEvidenceCases(manifest, "before-remote-delete"),
      (current) => {
        assert.equal(current.phase, "remote_delete_refused");
        assert.equal(current.remoteDelete.status, "refused_before_dispatch");
        assert(!existsSync(manifest.worktreePath));
        assert(!existsSync(manifest.quarantinePath));
        assertOwnedRegistrationsAbsent(manifest, current);
        assert.equal(observeLocalRef(manifest, current, TASK_REF).kind, "absent");
        assert.equal(queryOwnedRemoteRef(manifest, current, TASK_REF).sha, candidateSha);
      },
    );

    const completed = reconcileCleanup(manifest);
    assert.equal(completed.phase, "complete");
    assert.equal(completed.snapshotSha, effectOnlySnapshotSha);
    assert(!existsSync(taskPath));
    assert(!existsSync(manifest.quarantinePath));
    repairedDestructiveStages.push("remote_ref_delete");
    assertions.push("owned_worktree_removed");
    assert.equal(observeLocalRef(manifest, completed, TASK_REF).kind, "absent");
    assertions.push("exact_local_ref_removed");
    assert.equal(queryOwnedRemoteRef(manifest, completed, TASK_REF).kind, "absent");
    assertions.push("exact_remote_ref_removed");

    assert.throws(
      () => assertRecoveryRefsMayExpire(manifest, readState(manifest)),
      /private recovery artifact must expire before Git recovery refs/,
    );
    exerciseStoredEvidenceMutationTable(
      "stored_evidence_gate_before_retention_remove_table",
      manifest,
      gitRecoveryEvidenceCases(manifest, "before-retention-remove"),
      (current) => {
        assert.equal(current.phase, "complete");
        assert(existsSync(manifest.ignoredArtifactPath));
        assert.equal(observeLocalRef(manifest, current, TASK_REF).kind, "absent");
        assert.equal(queryOwnedRemoteRef(manifest, current, TASK_REF).kind, "absent");
      },
      () => {
        const current = readState(manifest);
        cleanupIgnoredRecovery(manifest, current, current.ignoredRecovery.retentionUntilMs + 1);
      },
    );
    state = readState(manifest);
    assert.deepEqual(
      cleanupIgnoredRecovery(manifest, state, state.ignoredRecovery.retentionUntilMs + 1),
      { status: "removed" },
    );
    assert.deepEqual(assertRecoveryRefsMayExpire(manifest, state), { status: "recovery_refs_may_expire" });
    assertions.push("recovery_ref_expiry_ordered_after_artifact");
    repairedDestructiveStages.push("retained_artifact_remove");
    assert.deepEqual(repairedDestructiveStages, [
      "worktree_move",
      "worktree_remove",
      "local_ref_delete",
      "remote_ref_delete",
      "retained_artifact_remove",
    ]);
    assertions.push("stored_evidence_repair_then_resume_all_stages");

    assert(existsSync(source));
    assert(sameIdentity(manifest.sourceIdentity, identity(source)));
    assert(sameIdentity(manifest.commonDirIdentity, identity(commonDir(source))));
    assert(sameIdentity(manifest.remoteIdentity, remoteFacts(source).identity));
    assert(existsSync(join(unknownPath, "unknown-sentinel.txt")));
    assert(existsSync(foreignPath));
    assert.equal(observeLocalRef(manifest, completed, RECOVERY_REF).sha, completed.snapshotSha);
    assertions.push("source_and_unknown_resources_preserved");

    const restorePath = join(worktreeRoot, "restored-snapshot");
    git(source, ["worktree", "add", "--detach", "--", restorePath, completed.snapshotSha]);
    assert.equal(readFileSync(join(restorePath, "tracked.txt"), "utf8"), "dirty tracked\n");
    assert.equal(readFileSync(join(restorePath, "untracked.txt"), "utf8"), "dirty untracked\n");
    assert.equal(readFileSync(join(restorePath, "staged.txt"), "utf8"), "dirty staged\n");
    assert.equal(readFileSync(join(restorePath, "nested", "untracked.txt"), "utf8"), "nested dirty\n");
    git(source, ["worktree", "remove", "--force", "--", restorePath]);
    assertions.push("snapshot_restored_after_removal");
    assert.equal(reconcileCleanup(manifest).phase, "complete");
    assert.equal(reconcileCleanup(manifest).phase, "complete");
    assertions.push("cleanup_idempotent");

    git(foreignSource, ["worktree", "remove", "--force", "--", foreignPath]);
    git(source, ["worktree", "remove", "--force", "--", unknownPath]);
    rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
    rootRemoved = true;
    assert(!existsSync(root));
    assertions.push("temporary_root_removed");
    assert.deepEqual(assertions, EXPECTED_ASSERTIONS);

    process.stdout.write(`${JSON.stringify({
      result: "pass",
      versions: {
        node: process.version,
        git: command("git", ["--version"]).trim(),
        platform: platform(),
        release: release(),
        arch: process.arch,
        paseoSchema: PASEO_SCHEMA,
      },
      coverage: {
        commonAssertions: assertions.filter((name) => !name.startsWith("linux_") && !name.startsWith("windows_")).length,
        platformAssertions: assertions.filter((name) => name.startsWith(`${platform()}_`)),
      },
      assertions,
    }, null, 2)}\n`);
  } finally {
    if (heldFd !== undefined) closeSync(heldFd);
    if (cleanHeldFd !== undefined) closeSync(cleanHeldFd);
    await stopChild(lockChild);
    await stopChild(authChild);
    await stopChild(specialChild);
    if (!rootRemoved) rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
  }
}

if (process.argv[2] === "--auth-fixture") {
  await runAuthFixture(process.argv[3]);
} else if (process.argv[2] === "--state-fsync-fixture") {
  process.stdout.write(`${JSON.stringify({
    result: "pass",
    assertion: verifyStateFileFsyncContract(),
  }, null, 2)}\n`);
} else {
  await run();
}
