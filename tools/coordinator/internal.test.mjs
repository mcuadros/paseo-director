// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  lstatSync,
  mkdtempSync,
  readFileSync,
  rmSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

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
