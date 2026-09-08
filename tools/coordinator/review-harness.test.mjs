// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { CoordinatorError, digest } from "./coordinator.mjs";
import { runReviewHarness } from "./review-harness.mjs";

function harnessFixture() {
  const root = mkdtempSync(join(tmpdir(), "director-review-harness-test-"));
  const checkout = join(root, "checkout");
  const manifestPath = join(root, "manifest.json");
  const stateFile = join(root, "state.json");
  const manifest = {
    schemaVersion: 1,
    authoritative: false,
    task: {
      id: "dir-m1.20",
      acceptanceCriteria: "criteria",
      acceptanceCriteriaHash: digest("criteria"),
    },
    humanDecisions: [],
    candidate: { sha: "1".repeat(40) },
    base: { sha: "2".repeat(40) },
    diff: { changedPathCount: 0, changedPaths: [] },
    priorFindings: [],
    authorValidation: {},
    ownership: {},
    pendingGates: [],
  };
  manifest.manifestHash = digest(manifest);
  writeFileSync(manifestPath, `${JSON.stringify(manifest)}\n`);
  return {
    root,
    checkout,
    manifestPath,
    stateFile,
    options: {
      manifest: manifestPath,
      checkout,
      "state-file": stateFile,
      "review-id": "review-0001",
      actor: "paseo:11111111-2222-4333-8444-555555555555",
      reason: "initial",
    },
  };
}

function harnessDependencies(overrides) {
  return {
    preflightReviewEnvironment: () => ({
      id: "review-environment",
      status: "passed",
    }),
    ...overrides,
  };
}

test("review checks start concurrently and results are collected deterministically", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  let ciStarted = false;
  let identityStarted = false;
  let releaseCi;
  const ciRelease = new Promise((resolvePromise) => {
    releaseCi = resolvePromise;
  });
  try {
    const output = await runReviewHarness(fixture.options, harnessDependencies({
      async runCompleteCi() {
        ciStarted = true;
        await ciRelease;
        return { id: "maintained-linux-ci", status: "passed" };
      },
      verifyReviewManifest() {
        identityStarted = true;
        assert.equal(ciStarted, true);
        releaseCi();
        return { id: "manifest-identity", status: "passed" };
      },
    }));
    assert.equal(identityStarted, true);
    assert.deepEqual(
      output.result.results.map((result) => result.id),
      ["maintained-linux-ci", "manifest-identity"],
    );
    assert.equal(output.result.attempt, 1);
    assert.equal(output.result.limits.maxParallel, 2);
    assert.equal(lstatMode(fixture.stateFile), 0o600);

    await assert.rejects(
      runReviewHarness({ ...fixture.options, "review-id": "review-0002" }, harnessDependencies({
        runCompleteCi: async () => ({ id: "maintained-linux-ci", status: "passed" }),
        verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
      })),
      (error) => error instanceof CoordinatorError && error.code === "REVIEW_FULL_CI_ALREADY_RUN",
    );
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

function lstatMode(path) {
  return lstatSync(path).mode & 0o777;
}

test("one recorded failure confirmation is allowed and a third full run is refused", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  try {
    await assert.rejects(
      runReviewHarness(fixture.options, harnessDependencies({
        runCompleteCi: async () => ({ id: "maintained-linux-ci", status: "failed", exitCode: 1 }),
        verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
      })),
      (error) => error instanceof CoordinatorError && error.code === "REVIEW_HARNESS_FAILED",
    );
    const second = {
      ...fixture.options,
      reason: "failure_confirmation",
      "reason-record": "Confirm the concrete maintained CI failure from attempt 1.",
    };
    const output = await runReviewHarness(second, harnessDependencies({
      runCompleteCi: async () => ({ id: "maintained-linux-ci", status: "passed" }),
      verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
    }));
    assert.equal(output.result.attempt, 2);
    await assert.rejects(
      runReviewHarness(second, harnessDependencies({
        runCompleteCi: async () => ({ id: "maintained-linux-ci", status: "passed" }),
        verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
      })),
      (error) => error instanceof CoordinatorError && error.code === "REVIEW_FULL_CI_LIMIT",
    );
    const state = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    const attempts = Object.values(state.reviews)[0].attempts;
    assert.deepEqual(
      attempts.map((attempt) => [attempt.number, attempt.reason, attempt.reasonRecord]),
      [
        [1, "initial", null],
        [2, "failure_confirmation", "Confirm the concrete maintained CI failure from attempt 1."],
      ],
    );
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("the exact review-state lock prevents concurrent full CI attempts", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  let startCi;
  let releaseCi;
  const started = new Promise((resolvePromise) => {
    startCi = resolvePromise;
  });
  const released = new Promise((resolvePromise) => {
    releaseCi = resolvePromise;
  });
  try {
    const first = runReviewHarness(fixture.options, harnessDependencies({
      async runCompleteCi() {
        startCi();
        await released;
        return { id: "maintained-linux-ci", status: "passed" };
      },
      verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
    }));
    await started;
    await assert.rejects(
      runReviewHarness(fixture.options, harnessDependencies({
        runCompleteCi: async () => ({ id: "maintained-linux-ci", status: "passed" }),
        verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
      })),
      (error) => error instanceof CoordinatorError && error.code === "REVIEW_HARNESS_BUSY",
    );
    releaseCi();
    await first;
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("review state inside the detached checkout is refused before CI", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  try {
    await assert.rejects(
      runReviewHarness({
        ...fixture.options,
        "state-file": join(fixture.checkout, "review-state.json"),
      }),
      (error) => error instanceof CoordinatorError && error.code === "REVIEW_STATE_INSIDE_CHECKOUT",
    );
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("missing npm preparation is invalid before the single CI attempt is consumed", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  let ciRuns = 0;
  try {
    await assert.rejects(
      runReviewHarness(fixture.options, {
        runCompleteCi: async () => {
          ciRuns += 1;
          return { id: "maintained-linux-ci", status: "passed" };
        },
        verifyReviewManifest: () => ({
          id: "manifest-identity",
          status: "passed",
        }),
      }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "REVIEW_DEPENDENCIES_NOT_PREPARED",
    );
    assert.equal(ciRuns, 0);
    assert.equal(lstatExists(fixture.stateFile), false);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

function lstatExists(path) {
  try {
    lstatSync(path);
    return true;
  } catch {
    return false;
  }
}

test("missing toolchain PATH is invalid before the single CI attempt is consumed", async () => {
  const fixture = harnessFixture();
  mkdirSync(join(fixture.checkout, "node_modules"), { recursive: true });
  writeFileSync(join(fixture.checkout, "package-lock.json"), "{}\n");
  writeFileSync(
    join(fixture.checkout, "node_modules", ".package-lock.json"),
    "{}\n",
  );
  let ciRuns = 0;
  try {
    await assert.rejects(
      runReviewHarness(fixture.options, {
        run(executable) {
          return {
            error: undefined,
            status: executable === "go" ? 127 : 0,
            stdout: "version\n",
            stderr: "",
          };
        },
        runCompleteCi: async () => {
          ciRuns += 1;
          return { id: "maintained-linux-ci", status: "passed" };
        },
        verifyReviewManifest: () => ({
          id: "manifest-identity",
          status: "passed",
        }),
      }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "REVIEW_TOOLCHAIN_NOT_PREPARED",
    );
    assert.equal(ciRuns, 0);
    assert.equal(lstatExists(fixture.stateFile), false);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});
