// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  existsSync,
  lstatSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { Worker } from "node:worker_threads";

import {
  CoordinatorError,
  canonicalJson,
  digest,
  githubRepositoryIdentity,
  persistPrivateJson,
  readJsonFile,
  withProcessIdentityLock,
} from "./internal.mjs";

test("shared canonical JSON, digest, and GitHub identity are deterministic", () => {
  assert.equal(canonicalJson({ z: 1, a: { d: 2, c: 3 } }), '{"a":{"c":3,"d":2},"z":1}');
  assert.equal(digest({ b: 2, a: 1 }), digest({ a: 1, b: 2 }));
  for (const url of [
    "https://github.com/acme/director.git",
    "ssh://git@github.com/acme/director.git",
    "git@github.com:acme/director.git",
  ]) {
    assert.equal(githubRepositoryIdentity(url), "acme/director");
  }
  assert.equal(githubRepositoryIdentity("file:///tmp/repository"), null);
});

test("shared private JSON persistence is atomic, private, and readable", () => {
  const root = mkdtempSync(join(tmpdir(), "director-internal-state-"));
  const path = join(root, "state.json");
  try {
    persistPrivateJson(path, { schemaVersion: 1, value: "first" });
    assert.equal(lstatSync(path).mode & 0o777, 0o600);
    assert.deepEqual(readJsonFile(path, "state"), {
      schemaVersion: 1,
      value: "first",
    });
    persistPrivateJson(path, { schemaVersion: 1, value: "second" });
    assert.equal(JSON.parse(readFileSync(path, "utf8")).value, "second");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("shared process-identity lock serializes one exact binding", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-internal-lock-"));
  const lockPath = join(root, "state.lock");
  const configuration = {
    lockPath,
    bindingHash: "a".repeat(64),
    label: "test lock",
    busyCode: "TEST_BUSY",
    invalidCode: "TEST_INVALID",
    replacedCode: "TEST_REPLACED",
    processCode: "TEST_PROCESS",
  };
  let entered;
  let release;
  const entry = new Promise((resolvePromise) => {
    entered = resolvePromise;
  });
  const released = new Promise((resolvePromise) => {
    release = resolvePromise;
  });
  try {
    const first = withProcessIdentityLock(configuration, async () => {
      entered();
      await released;
      return "complete";
    });
    await entry;
    await assert.rejects(
      withProcessIdentityLock(configuration, async () => "duplicate"),
      (error) => error instanceof CoordinatorError && error.code === "TEST_BUSY",
    );
    release();
    assert.equal(await first, "complete");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("concurrent stale-lock reclamation has one winner and preserves its effect record", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-internal-stale-lock-"));
  const lockPath = join(root, "state.lock");
  const statePath = join(root, "state.json");
  const bindingHash = "b".repeat(64);
  const configuration = {
    lockPath,
    bindingHash,
    label: "stale test lock",
    busyCode: "TEST_BUSY",
    invalidCode: "TEST_INVALID",
    replacedCode: "TEST_REPLACED",
    processCode: "TEST_PROCESS",
  };
  writeFileSync(
    lockPath,
    `${canonicalJson({
      schemaVersion: 1,
      pid: 2_147_483_647,
      processStartTime: "0",
      nonce: "c".repeat(32),
      bindingHash,
    })}\n`,
    { mode: 0o600 },
  );
  try {
    const barrier = new SharedArrayBuffer(8);
    const values = new Int32Array(barrier);
    const messages = [];
    const workers = ["first", "second"].map(
      (effect) =>
        new Worker(new URL("./lock-contender.fixture.mjs", import.meta.url), {
          workerData: {
            barrier,
            configuration,
            effect,
            statePath,
          },
        }),
    );
    const completions = workers.map(
      (worker) =>
        new Promise((resolvePromise, rejectPromise) => {
          worker.on("message", (message) => {
            messages.push(message);
            if (
              messages.filter(({ status }) =>
                status === "ready"
              ).length === 2
            ) {
              Atomics.store(values, 0, 1);
              Atomics.notify(values, 0, 2);
            }
            if (
              messages.filter(({ status }) =>
                status === "entered" || status === "rejected"
              ).length === 2
            ) {
              Atomics.store(values, 1, 1);
              Atomics.notify(values, 1, 2);
            }
          });
          worker.once("error", rejectPromise);
          worker.once("exit", (code) => {
            if (code === 0) resolvePromise();
            else rejectPromise(new Error(`lock contender exited ${code}`));
          });
        }),
    );
    await Promise.all(completions);
    assert.equal(
      messages.filter(({ status }) => status === "entered").length,
      1,
    );
    assert.equal(
      messages.filter(({ status }) => status === "fulfilled").length,
      1,
    );
    assert.deepEqual(
      messages
        .filter(({ status }) => status === "rejected")
        .map(({ code }) => code),
      ["TEST_BUSY"],
    );
    assert.equal(
      Object.keys(JSON.parse(readFileSync(statePath, "utf8")).effects).length,
      1,
    );
    assert.equal(existsSync(lockPath), false);
    assert.equal(lstatSync(`${lockPath}.guard`).mode & 0o777, 0o600);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
