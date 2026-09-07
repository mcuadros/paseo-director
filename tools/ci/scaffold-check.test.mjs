// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  checkWorkflowContract,
  checkWorkflowSet,
  hostSourceErrors,
  lintRepository,
  releaseMetadataErrors,
} from "./scaffold-check.mjs";

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const workflowPath = ".github/workflows/ci.yml";
const workflow = JSON.parse(
  readFileSync(resolve(repositoryRoot, workflowPath), "utf8"),
);

test("the maintained scaffold and workflow satisfy their contracts", () => {
  assert.deepEqual(lintRepository(repositoryRoot).errors, []);
});

test("workflow coverage rejects pull-request, main, path, and event filters", () => {
  const pullRequestFiltered = structuredClone(workflow);
  pullRequestFiltered.on.pull_request["paths-ignore"] = ["**"];
  assert.ok(
    checkWorkflowContract(pullRequestFiltered).includes(
      `${workflowPath}: pull_request must be unfiltered`,
    ),
  );

  const mainFiltered = structuredClone(workflow);
  mainFiltered.on.push = { branches: ["release"], paths: ["src/**"] };
  assert.ok(
    checkWorkflowContract(mainFiltered).includes(
      `${workflowPath}: push must target main without path, event, or ignore filters`,
    ),
  );
});

test("every workflow file is inspected and additional workflows fail closed", () => {
  const unchecked = structuredClone(workflow);
  delete unchecked.on.pull_request;
  const errors = checkWorkflowSet([
    { path: workflowPath, workflow },
    { path: ".github/workflows/unchecked.yml", workflow: unchecked },
  ]);
  assert.ok(
    errors.includes(
      ".github/workflows/unchecked.yml: pull_request must be unfiltered",
    ),
  );
  assert.ok(
    errors.some((error) => error.startsWith("workflow set must contain exactly")),
  );
});

test("workflow mutations cannot skip or replace the real scaffold suite", () => {
  const changed = structuredClone(workflow);
  changed.jobs.quality.steps[4].run = "echo skipped";
  changed.jobs.quality.steps[4]["continue-on-error"] = true;
  const errors = checkWorkflowContract(changed);
  assert.ok(
    errors.includes(
      `${workflowPath}: workflow steps cannot be skipped or allowed to fail`,
    ),
  );
  assert.ok(
    errors.includes(
      `${workflowPath}: quality must run the complete scaffold suite`,
    ),
  );
});

test("workflow validation rejects cancellation, unknown events, and job env or strategy", () => {
  const cancellation = structuredClone(workflow);
  cancellation.concurrency = {
    group: "ci",
    "cancel-in-progress": true,
  };
  const cancellationErrors = checkWorkflowContract(cancellation);
  assert.ok(
    cancellationErrors.includes(
      `${workflowPath}: workflow concurrency cancellation is not permitted`,
    ),
  );

  const unknownEvent = structuredClone(workflow);
  unknownEvent.on.schedule = [{ cron: "0 0 * * *" }];
  assert.ok(
    checkWorkflowContract(unknownEvent).includes(
      `${workflowPath}: workflow must contain only pull_request and push events`,
    ),
  );

  for (const field of ["env", "strategy"]) {
    const jobConfiguration = structuredClone(workflow);
    jobConfiguration.jobs.quality[field] = {};
    assert.ok(
      checkWorkflowContract(jobConfiguration).includes(
        `${workflowPath}: quality job may not define env, strategy, concurrency, or unknown configuration`,
      ),
    );
  }
});

test("release validation rejects empty digests, dot segments, and assets while unpublished", () => {
  const asset = {
    url: "https://github.com/mcuadros/paseo-director/releases/download/v1/director-engine",
    sha256: "1".repeat(64),
  };
  const published = {
    schemaVersion: 1,
    state: "published",
    version: "v1",
    target: "linux-amd64",
    binary: asset,
    notices: { ...asset, sha256: "2".repeat(64) },
  };
  assert.deepEqual(releaseMetadataErrors(published), []);
  assert.ok(
    releaseMetadataErrors({
      ...published,
      binary: {
        ...asset,
        sha256:
          "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      },
    }).some((error) => error.includes("digest")),
  );
  assert.ok(releaseMetadataErrors({ ...published, version: ".." }).length > 0);
  assert.ok(
    releaseMetadataErrors({
      schemaVersion: 1,
      state: "unpublished",
      version: "0.0.0-scaffold",
      target: "linux-amd64",
      binary: asset,
    }).includes("unpublished release metadata must not declare assets"),
  );
});

test("engine independence disables VCS stamping", () => {
  const source = readFileSync(
    resolve(repositoryRoot, "tools/ci/engine-independence.mjs"),
    "utf8",
  );
  assert.ok(source.includes('"-buildvcs=false"'));
});

test("connector policy and Paseo SDK imports outside the adapter fail lint", () => {
  assert.deepEqual(
    hostSourceErrors(
      "server/scheduler.server.ts",
      'import "@getpaseo/client"; const retryPolicy = true;\n',
    ),
    [
      "server/scheduler.server.ts: Paseo SDK imports belong only in the host connector",
      "server/scheduler.server.ts: host server source contains prohibited workflow policy",
    ],
  );
});

test("the checker executes when its entry path is a symlink", () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-ci-symlink-"));
  try {
    const link = join(temporaryRoot, "scaffold-check.mjs");
    symlinkSync(resolve(repositoryRoot, "tools/ci/scaffold-check.mjs"), link);
    const result = spawnSync(process.execPath, [link, "invalid-mode"], {
      cwd: repositoryRoot,
      encoding: "utf8",
    });
    assert.equal(result.status, 2);
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});
