// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  chmodSync,
  copyFileSync,
  lstatSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readlinkSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import {
  checkWorkflowContract,
  checkWorkflowSet,
  enginePackageInventoryErrors,
  formatErrors,
  hostSourceErrors,
  lintRepository,
  repositoryFiles,
  releaseMetadataErrors,
  run,
  workflowErrors,
} from "./scaffold-check.mjs";

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const workflowPath = ".github/workflows/ci.yml";
const workflow = JSON.parse(
  readFileSync(resolve(repositoryRoot, workflowPath), "utf8"),
);

function trackedRepositoryCopy(prefix) {
  const temporaryRoot = mkdtempSync(join(tmpdir(), prefix));
  const listed = spawnSync("git", ["ls-files", "-z"], {
    cwd: repositoryRoot,
    encoding: "utf8",
  });
  assert.equal(listed.status, 0, listed.stderr);
  for (const path of listed.stdout.split("\0").filter(Boolean)) {
    const source = resolve(repositoryRoot, path);
    const destination = resolve(temporaryRoot, path);
    const status = lstatSync(source);
    mkdirSync(dirname(destination), { recursive: true });
    if (status.isSymbolicLink()) {
      symlinkSync(readlinkSync(source), destination);
    } else {
      copyFileSync(source, destination);
      chmodSync(destination, status.mode);
    }
  }
  for (const args of [
    ["init", "--quiet"],
    ["add", "--all"],
  ]) {
    const result = spawnSync("git", args, {
      cwd: temporaryRoot,
      encoding: "utf8",
    });
    assert.equal(result.status, 0, result.stderr);
  }
  return temporaryRoot;
}

async function loadMutatedScaffoldCheck(mutate) {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-scaffold-mutant-"));
  const source = readFileSync(
    resolve(repositoryRoot, "tools/ci/scaffold-check.mjs"),
    "utf8",
  );
  const mutated = mutate(source);
  assert.notEqual(mutated, source, "mutation must change the scaffold checker");
  const destination = resolve(temporaryRoot, "scaffold-check.mjs");
  writeFileSync(destination, mutated, { mode: 0o600 });
  return {
    checker: await import(pathToFileURL(destination).href),
    temporaryRoot,
  };
}

test("the maintained scaffold and workflow satisfy their contracts", () => {
  assert.deepEqual(lintRepository(repositoryRoot).errors, []);
});

test("scaffold inventory admits only the exact credential fixture package", () => {
  const exact = "engine/internal/testkit/secretfixture/canary.go";
  assert.deepEqual(enginePackageInventoryErrors([exact]), []);

  const rejected = [
    "engine/internal/testkit/secretfixtures/canary.go",
    "engine/internal/testkit/secretfixture/runtime/canary.go",
    "engine/internal/testkit/secretfixture-product/canary.go",
    "engine/internal/runtime/secretfixture.go",
  ];
  assert.deepEqual(enginePackageInventoryErrors(rejected), [
    `engine scaffold contains an unclassified product package: ${rejected.join(", ")}`,
  ]);
});

test("copied repository retains exact fixture admission and rejects neighboring runtime packages", () => {
  const temporaryRoot = trackedRepositoryCopy("director-scaffold-fixture-boundary-");
  const rejected = [
    "engine/internal/testkit/secretfixtures/canary.go",
    "engine/internal/testkit/secretfixture/runtime/canary.go",
  ];
  try {
    assert.deepEqual(lintRepository(temporaryRoot).errors, []);
    for (const path of rejected) {
      const absolute = resolve(temporaryRoot, path);
      mkdirSync(dirname(absolute), { recursive: true });
      writeFileSync(absolute, "package runtime\n", { mode: 0o600 });
    }
    assert.ok(
      lintRepository(temporaryRoot).errors.includes(
        `engine scaffold contains an unclassified product package: ${rejected.toSorted().join(", ")}`,
      ),
    );
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});

test("scaffold inventory mutations cannot omit or broaden the exact fixture boundary", async () => {
  const exact = "engine/internal/testkit/secretfixture/canary.go";
  const neighboring = "engine/internal/testkit/secretfixtures/canary.go";
  const expected = [
    `engine scaffold contains an unclassified product package: ${neighboring}`,
  ];
  const mutations = [
    (source) => source.replace(
      '  "engine/internal/testkit/secretfixture",\n',
      "",
    ),
    (source) => source.replace(
      "!ENGINE_ALLOWED_PACKAGE_DIRECTORIES.has(\n        path.slice(0, path.lastIndexOf(\"/\")),\n      )",
      "![...ENGINE_ALLOWED_PACKAGE_DIRECTORIES].some((directory) => path.startsWith(directory.slice(0, directory.lastIndexOf(\"/\"))))",
    ),
  ];
  for (const mutate of mutations) {
    const { checker, temporaryRoot } = await loadMutatedScaffoldCheck(mutate);
    try {
      assert.notDeepEqual(
        checker.enginePackageInventoryErrors([exact, neighboring]),
        expected,
      );
    } finally {
      rmSync(temporaryRoot, { recursive: true, force: true });
    }
  }
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
  for (const url of [
    "https://github.com/mcuadros/paseo-director/releases/download/../../../../attacker/evil/releases/download/v1/x",
    "https://github.com/mcuadros/paseo-director/releases/download/%2e%2e/%2e%2e/%2e%2e/%2e%2e/attacker/x",
    `${asset.url}?mirror=attacker`,
    `${asset.url}#attacker`,
  ]) {
    assert.ok(
      releaseMetadataErrors({
        ...published,
        binary: { ...asset, url },
      }).some((error) => error.includes("binary identity")),
      `release validator accepted ${url}`,
    );
  }
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
      "connector/scheduler.server.ts",
      'import "@getpaseo/client"; const retryPolicy = true;\n',
    ),
    [
      "connector/scheduler.server.ts: Paseo SDK imports belong only in the host connector",
      "connector/scheduler.server.ts: host source contains prohibited workflow policy",
    ],
  );
});

test("prohibited host policy vocabulary covers the entrypoint and every host directory", () => {
  for (const [path, identifier] of [
    ["index.ts", "ELIGIBILITY"],
    ["ui/policy.client.tsx", "scheduler"],
    ["connector/policy.server.ts", "ReTrYpOlIcY"],
    ["rpc/policy.shared.ts", "taskstore"],
  ]) {
    assert.deepEqual(hostSourceErrors(path, `const ${identifier} = true;\n`), [
      `${path}: host source contains prohibited workflow policy`,
    ]);
  }

  for (const [path, identifier] of [
    ["generated/policy.shared.ts", "STATEtransition"],
    ["index.ts", "Escalation"],
    ["connector/policy.server.ts", "RECONCILIATION"],
    ["generated/policy.shared.ts", "closurepolicy"],
  ]) {
    assert.deepEqual(hostSourceErrors(path, `class ${identifier} {}\n`), [
      `${path}: host source contains prohibited workflow policy`,
    ]);
  }
});

test("legitimate UI, connector, RPC, and generated host sources remain policy-free", () => {
  for (const path of [
    "ui/shells.client.tsx",
    "connector/paseo.server.ts",
    "rpc/startup.shared.ts",
    "generated/host-contract.shared.ts",
  ]) {
    assert.deepEqual(
      hostSourceErrors(path, readFileSync(resolve(repositoryRoot, path), "utf8")),
      [],
      path,
    );
  }
});

test("standalone lint reports tracked dangling non-workflow symlinks without stacks", (context) => {
  const temporaryRoot = trackedRepositoryCopy("director-ci-lint-dangling-");
  const deferredSyntaxPath = "tools/ci/deferred-broken.mjs";
  const danglingPaths = [
    "connector/gone.server.ts",
    "docs/adr/0099-gone.md",
    "tools/ci/gone.mjs",
  ];
  try {
    writeFileSync(
      resolve(temporaryRoot, deferredSyntaxPath),
      "export const = broken;\n",
    );
    for (const path of danglingPaths) {
      mkdirSync(dirname(resolve(temporaryRoot, path)), { recursive: true });
      symlinkSync("missing-target", resolve(temporaryRoot, path));
    }
    const tracked = spawnSync(
      "git",
      ["add", "--", deferredSyntaxPath, ...danglingPaths],
      {
        cwd: temporaryRoot,
        encoding: "utf8",
      },
    );
    assert.equal(tracked.status, 0, tracked.stderr);

    const expectedErrors = danglingPaths
      .toSorted()
      .map(
        (path) =>
          `${path}: tracked repository symlinks require an explicit policy`,
      );
    const lintResult = lintRepository(temporaryRoot);
    assert.deepEqual(lintResult.errors, expectedErrors);
    assert.deepEqual(lintResult.governance, { count: 0, errors: [] });
    assert.deepEqual(lintResult.syntax, { count: 0, errors: [] });
    assert.deepEqual(lintResult.workflows, { count: 0, errors: [] });

    const output = [];
    context.mock.method(console, "error", (message) => {
      output.push(String(message));
    });
    assert.equal(run(temporaryRoot, "lint"), 1);
    assert.deepEqual(output, [
      "Scaffold lint failed:",
      ...expectedErrors.map((error) => `- ${error}`),
    ]);
    assert.doesNotMatch(output.join("\n"), /ENOENT|node:fs|\n\s+at\s/);

    output.length = 0;
    assert.equal(run(temporaryRoot, "format"), 1);
    assert.deepEqual(output, [
      "Formatting checks failed:",
      ...expectedErrors.map((error) => `- ${error}`),
    ]);
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});

test("missing gofmt returns a deterministic format diagnostic", () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-ci-no-gofmt-"));
  const goPath = "engine/doc.go";
  const originalPath = process.env.PATH;
  try {
    mkdirSync(dirname(resolve(temporaryRoot, goPath)), { recursive: true });
    writeFileSync(resolve(temporaryRoot, goPath), "package engine\n");
    process.env.PATH = "";
    assert.deepEqual(formatErrors(temporaryRoot, [goPath]), [
      "gofmt is required but was not found on PATH",
    ]);
  } finally {
    if (originalPath === undefined) {
      delete process.env.PATH;
    } else {
      process.env.PATH = originalPath;
    }
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});

test("tracked dangling workflow symlinks remain visible to repository policies", () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-ci-dangling-"));
  try {
    mkdirSync(join(temporaryRoot, ".github", "workflows"), { recursive: true });
    const danglingPath = ".github/workflows/dangling.yml";
    symlinkSync("missing-workflow.yml", join(temporaryRoot, danglingPath));
    for (const args of [
      ["init", "--quiet"],
      ["add", "--", danglingPath],
    ]) {
      const result = spawnSync("git", args, {
        cwd: temporaryRoot,
        encoding: "utf8",
      });
      assert.equal(result.status, 0, result.stderr);
    }

    const paths = repositoryFiles(temporaryRoot);
    assert.deepEqual(paths, [danglingPath]);
    assert.ok(
      formatErrors(temporaryRoot, paths).includes(
        `${danglingPath}: tracked repository symlinks require an explicit policy`,
      ),
    );
    const workflows = workflowErrors(temporaryRoot, paths);
    assert.equal(workflows.count, 1);
    assert.ok(
      workflows.errors.some(
        (error) =>
          error.startsWith(`${danglingPath}: workflow must be JSON-compatible YAML:`),
      ),
    );
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
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
