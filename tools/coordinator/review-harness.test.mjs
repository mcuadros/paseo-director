// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
  existsSync,
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
import { fileURLToPath } from "node:url";

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
  const fixture = {
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
  const observation = remoteObservation(fixture);
  fixture.options["remote-ci-file"] = observation.path;
  return fixture;
}

function harnessDependencies(overrides) {
  return {
    preflightRemoteReviewEnvironment: () => ({
      id: "review-environment",
      status: "passed",
    }),
    ...overrides,
  };
}

function lstatMode(path) {
  return lstatSync(path).mode & 0o777;
}

test("the exact review-state lock serializes one remote observation consumer", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  let startIdentity;
  let releaseIdentity;
  const started = new Promise((resolvePromise) => {
    startIdentity = resolvePromise;
  });
  const released = new Promise((resolvePromise) => {
    releaseIdentity = resolvePromise;
  });
  try {
    const first = runReviewHarness(fixture.options, harnessDependencies({
      async verifyReviewManifest() {
        startIdentity();
        await released;
        return { id: "manifest-identity", status: "passed" };
      },
    }));
    await started;
    await assert.rejects(
      runReviewHarness(fixture.options, harnessDependencies({
        verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
      })),
      (error) => error instanceof CoordinatorError && error.code === "REVIEW_HARNESS_BUSY",
    );
    releaseIdentity();
    await first;
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("review state inside the detached checkout is refused before remote observation consumption", async () => {
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

test("review handoff ownership rejects an adversarial raw-material field", async () => {
  const fixture = harnessFixture();
  const ownershipCanary = `ownership-${randomBytes(32).toString("hex")}`;
  mkdirSync(fixture.checkout);
  try {
    const manifest = JSON.parse(readFileSync(fixture.manifestPath, "utf8"));
    delete manifest.manifestHash;
    manifest.ownership = { ownership: ownershipCanary };
    manifest.manifestHash = digest(manifest);
    writeFileSync(fixture.manifestPath, `${JSON.stringify(manifest)}\n`);
    await assert.rejects(
      runReviewHarness(fixture.options, harnessDependencies()),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "HARNESS_SCHEMA_INVALID" &&
        !String(error.message).includes(ownershipCanary),
    );
    assert.equal(existsSync(fixture.stateFile), false);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("unknown schema keys never echo a high-entropy canary", async () => {
  const fixture = harnessFixture();
  const unknownKey = `ownership-${randomBytes(32).toString("hex")}`;
  mkdirSync(fixture.checkout);
  try {
    const manifest = JSON.parse(readFileSync(fixture.manifestPath, "utf8"));
    delete manifest.manifestHash;
    manifest[unknownKey] = "attacker-controlled";
    manifest.manifestHash = digest(manifest);
    writeFileSync(fixture.manifestPath, `${JSON.stringify(manifest)}\n`);

    await assert.rejects(
      runReviewHarness(fixture.options, harnessDependencies()),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "HARNESS_SCHEMA_INVALID" &&
        error.details?.unknownFieldCount === 1 &&
        !JSON.stringify(error).includes(unknownKey),
    );

    const cli = spawnSync(
      process.execPath,
      [
        fileURLToPath(new URL("./review-harness.mjs", import.meta.url)),
        "run",
        ...Object.entries(fixture.options).flatMap(([key, value]) => [
          `--${key}`,
          String(value),
        ]),
      ],
      { encoding: "utf8" },
    );
    assert.equal(cli.status, 2, cli.stderr);
    assert.equal(cli.stdout.includes(unknownKey), false);
    assert.equal(cli.stderr.includes(unknownKey), false);
    assert.equal(existsSync(fixture.stateFile), false);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("the harness requires one remote observation and never starts local CI", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  try {
    await assert.rejects(
      runReviewHarness(
        { ...fixture.options, "remote-ci-file": undefined },
        harnessDependencies({
          runCompleteCi() {
            assert.fail("the v2 review harness started a local complete CI");
          },
        }),
      ),
      (error) => error instanceof CoordinatorError && error.code === "HARNESS_OPTION_REQUIRED",
    );
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

test("missing Git PATH is invalid before the remote observation is consumed", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  try {
    await assert.rejects(
      runReviewHarness(fixture.options, {
        run(executable) {
          return {
            error: undefined,
            status: executable === "git" ? 127 : 0,
            stdout: "version\n",
            stderr: "",
          };
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
    assert.equal(lstatExists(fixture.stateFile), false);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

function remoteObservation(fixture, changes = {}) {
  const path = join(fixture.root, "remote-ci.json");
  const result = {
    authoritative: true,
    base: "2".repeat(40),
    candidate: "1".repeat(40),
    checks: [
      {
        command: ["github-actions", "CI"],
        id: "maintained-linux-ci",
        status: "passed",
      },
    ],
    observationId: digest("remote-observation-1"),
    remote: { workflow: { id: 900001, name: "CI" } },
    task: "dir-m1.20",
    ...changes,
  };
  writeFileSync(
    path,
    `${JSON.stringify({
      schemaVersion: 1,
      command: "remote-ci",
      outcome: "ready",
      result,
    })}\n`,
  );
  return { path, result };
}

test("review consumes the authoritative remote CI and starts no complete CI", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  const observation = remoteObservation(fixture);
  try {
    const output = await runReviewHarness(
      { ...fixture.options, "remote-ci-file": observation.path },
      harnessDependencies({
        runCompleteCi() {
          assert.fail("review started a complete CI despite an authoritative remote run");
        },
        preflightRemoteReviewEnvironment: () => ({
          id: "review-environment",
          status: "passed",
        }),
        verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
      }),
    );
    assert.equal(output.result.completeCi.source, "authoritative-remote");
    assert.equal(output.result.completeCi.startedByReview, false);
    assert.equal(
      output.result.completeCi.observationId,
      observation.result.observationId,
    );
    const ci = output.result.results.find(
      (item) => item.id === "maintained-linux-ci",
    );
    assert.equal(ci.status, "passed");
    assert.equal(ci.source, "authoritative-remote");
    assert.equal(ci.workflowRunId, 900001);

    // Consuming the same observation again is idempotent and returns the exact
    // stored result without appending a second review attempt.
    const again = await runReviewHarness(
      { ...fixture.options, "remote-ci-file": observation.path },
      harnessDependencies({
        runCompleteCi() {
          assert.fail("second consumption started a complete CI");
        },
        preflightRemoteReviewEnvironment: () => ({
          id: "review-environment",
          status: "passed",
        }),
        verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
      }),
    );
    assert.equal(again.result.attempt, 1);
    assert.equal(again.result.completeCi.source, "authoritative-remote");
    assert.deepEqual(again, output);

    await assert.rejects(
      runReviewHarness(
        {
          ...fixture.options,
          "review-id": "reviewer-other",
          "remote-ci-file": observation.path,
        },
        harnessDependencies({
          preflightRemoteReviewEnvironment: () => ({
            id: "review-environment",
            status: "passed",
          }),
        }),
      ),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "REVIEWER_IDENTITY_CHANGED");
        return true;
      },
    );
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("remote CI consumption fails closed on a foreign, failed, or rerun observation", async () => {
  const fixture = harnessFixture();
  mkdirSync(fixture.checkout);
  const dependencies = harnessDependencies({
    runCompleteCi() {
      assert.fail("a refused remote observation must not start a complete CI");
    },
    preflightRemoteReviewEnvironment: () => ({
      id: "review-environment",
      status: "passed",
    }),
    verifyReviewManifest: () => ({ id: "manifest-identity", status: "passed" }),
  });
  try {
    for (const [name, changes, code] of [
      ["other candidate", { candidate: "3".repeat(40) }, "REVIEW_REMOTE_CI_INVALID"],
      ["other base", { base: "4".repeat(40) }, "REVIEW_REMOTE_CI_INVALID"],
      ["not authoritative", { authoritative: false }, "REVIEW_REMOTE_CI_INVALID"],
      [
        "failed run",
        {
          checks: [
            {
              command: ["github-actions", "CI"],
              id: "maintained-linux-ci",
              status: "failed",
            },
          ],
        },
        "REVIEW_REMOTE_CI_NOT_PASSED",
      ],
    ]) {
      const observation = remoteObservation(fixture, changes);
      await assert.rejects(
        runReviewHarness(
          { ...fixture.options, "remote-ci-file": observation.path },
          dependencies,
        ),
        (error) => {
          assert.ok(error instanceof CoordinatorError, name);
          assert.equal(error.code, code, name);
          return true;
        },
      );
    }

    // A remote observation is consumed, not retried: a rerun reason belongs to
    // a locally started complete CI.
    const observation = remoteObservation(fixture);
    await assert.rejects(
      runReviewHarness(
        {
          ...fixture.options,
          "remote-ci-file": observation.path,
          reason: "failure_confirmation",
          "reason-record": "rerun",
        },
        dependencies,
      ),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "HARNESS_INPUT_INVALID");
        return true;
      },
    );

    await runReviewHarness(
      { ...fixture.options, "remote-ci-file": observation.path },
      dependencies,
    );
    const replaced = remoteObservation(fixture, {
      observationId: digest("remote-observation-2"),
    });
    await assert.rejects(
      runReviewHarness(
        { ...fixture.options, "remote-ci-file": replaced.path },
        dependencies,
      ),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "REVIEW_REMOTE_CI_CHANGED");
        return true;
      },
    );
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});
