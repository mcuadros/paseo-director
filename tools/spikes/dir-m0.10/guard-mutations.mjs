#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { access, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const sourcePath = join(dirname(fileURLToPath(import.meta.url)), "effect-contract.mjs");
const mutations = [
  { id: "observation-epoch", expectedCode: "OBSERVATION_EPOCH_MISMATCH" },
  { id: "observation-holder", expectedCode: "OBSERVATION_HOLDER_MISMATCH" },
  { id: "observation-single-use", expectedCode: "OBSERVATION_ALREADY_CONSUMED" },
  { id: "observation-max-age", expectedCode: "OBSERVATION_EXPIRED" },
  { id: "observation-attempt-binding", expectedCode: "OBSERVATION_ATTEMPT_MISMATCH" },
  { id: "session-commit-mode", expectedCode: "UNSAFE_SESSION_COMMIT_MODE" },
  {
    id: "workspace-lifecycle-admission",
    expectedCode: "WORKTREE_LIFECYCLE_ADMISSION_MISSING",
  },
  { id: "operational-limits", expectedCode: "OPERATIONAL_LIMIT_EXCEEDED" },
];

function deleteGuard(source, id) {
  const startMarker = `// MUTATION_GUARD_START ${id}`;
  const endMarker = `// MUTATION_GUARD_END ${id}`;
  const start = source.indexOf(startMarker);
  const end = source.indexOf(endMarker);
  assert.notEqual(start, -1, `missing start marker ${id}`);
  assert.notEqual(end, -1, `missing end marker ${id}`);
  assert.ok(end > start, `reversed markers ${id}`);
  return `${source.slice(0, start)}// deleted guard: ${id}${source.slice(end + endMarker.length)}`;
}

async function main() {
  const root = await mkdtemp(join(tmpdir(), "director-m010-mutations-"));
  const source = await readFile(sourcePath, "utf8");
  const killed = [];
  try {
    for (const mutation of mutations) {
      const mutatedPath = join(root, `${mutation.id}.mjs`);
      await writeFile(mutatedPath, deleteGuard(source, mutation.id), { mode: 0o600 });
      const syntax = spawnSync(process.execPath, ["--check", mutatedPath], {
        cwd: root,
        encoding: "utf8",
        timeout: 30_000,
        maxBuffer: 1_048_576,
      });
      assert.equal(syntax.error, undefined, `${mutation.id} syntax process did not start`);
      assert.equal(syntax.status, 0, `${mutation.id} mutation did not parse`);
      const result = spawnSync(process.execPath, [mutatedPath], {
        cwd: root,
        encoding: "utf8",
        timeout: 30_000,
        maxBuffer: 1_048_576,
      });
      assert.equal(result.error, undefined, `${mutation.id} test process did not start`);
      assert.equal(result.signal, null, `${mutation.id} terminated by ${result.signal}`);
      assert.equal(result.status, 1, `${mutation.id} unexpectedly survived`);
      assert.match(result.stderr, new RegExp(mutation.expectedCode));
      killed.push({ id: mutation.id, exitStatus: result.status, expectedCode: mutation.expectedCode });
    }
  } finally {
    await rm(root, { recursive: true });
  }

  let temporaryRootAbsent = false;
  try {
    await access(root);
  } catch (error) {
    if (error.code === "ENOENT") temporaryRootAbsent = true;
    else throw error;
  }
  assert.equal(temporaryRootAbsent, true);
  process.stdout.write(
    `${JSON.stringify(
      {
        task: "dir-m0.10",
        outcome: "pass",
        mutationKind: "delete_guard",
        killed,
        survivors: mutations.length - killed.length,
        temporaryRootAbsent,
      },
      null,
      2,
    )}\n`,
  );
}

main().catch((error) => {
  process.stderr.write(`${error.stack ?? error}\n`);
  process.exitCode = 1;
});
