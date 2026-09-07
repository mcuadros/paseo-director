// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  architectureErrors,
  goDependencyErrors,
  hostContractErrors,
  policyOwnershipErrors,
  policyPathErrors,
  structureErrors,
  typescriptBoundaryErrors,
} from "./architecture-check.mjs";

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const modulePath = "github.com/mcuadros/director-engine";

test("the repository satisfies the complete modular architecture contract", () => {
  assert.deepEqual(architectureErrors(repositoryRoot), []);
});

test("Go boundaries reject outward, cross-reducer, external, and effectful imports", () => {
  const errors = goDependencyErrors([
    {
      importPath: `${modulePath}/domain`,
      imports: [`${modulePath}/application`],
    },
    {
      importPath: `${modulePath}/reducer/eligibility`,
      imports: [`${modulePath}/reducer/routing`, "time"],
    },
    {
      importPath: `${modulePath}/application`,
      imports: ["example.com/external"],
    },
    {
      importPath: `${modulePath}/adapters/host`,
      imports: ["github.com/getpaseo/client"],
    },
  ]);
  assert.ok(errors.some((error) => error.includes("domain boundary cannot import application")));
  assert.ok(errors.some((error) => error.includes("cannot depend on another reducer")));
  assert.ok(errors.some((error) => error.includes("effectful standard package time")));
  assert.ok(errors.some((error) => error.includes("imports external package")));
  assert.ok(
    errors.some(
      (error) =>
        error.includes(`${modulePath}/adapters/host`) &&
        error.includes("github.com/getpaseo/client"),
    ),
  );
  assert.equal(
    goDependencyErrors([
      {
        importPath: `${modulePath}/adapters/dolt`,
        imports: ["github.com/go-sql-driver/mysql"],
      },
    ]).length,
    0,
  );

  const logErrors = goDependencyErrors([
    {
      importPath: `${modulePath}/domain`,
      imports: ["log"],
    },
    {
      importPath: `${modulePath}/reducer/routing`,
      imports: ["log/slog"],
    },
    {
      importPath: `${modulePath}/projection`,
      imports: ["log"],
    },
  ]);
  for (const role of ["domain", "reducer", "projection"]) {
    assert.ok(
      logErrors.some(
        (error) =>
          error.includes(`pure ${role} package`) &&
          error.includes("effectful standard package log"),
      ),
    );
  }
  assert.deepEqual(
    goDependencyErrors([
      {
        importPath: `${modulePath}/domain`,
        imports: ["fmt"],
      },
      {
        importPath: `${modulePath}/reducer/closure`,
        imports: ["fmt"],
      },
      {
        importPath: `${modulePath}/projection`,
        imports: ["fmt"],
      },
    ]),
    [],
  );
});

test("adapters and agent runtime cannot declare lifecycle policy", () => {
  assert.deepEqual(
    policyOwnershipErrors(
      "engine/adapters/host/adapter.go",
      "package host\ntype RetryPolicy struct{}\n",
      "go",
    ),
    [
      "engine/adapters/host/adapter.go: boundary adapter/runtime declares policy-shaped symbol RetryPolicy",
    ],
  );
  assert.deepEqual(
    policyOwnershipErrors(
      "engine/agent-runtime/runtime.go",
      "package agentruntime\nfunc SubmitClaim() {}\n",
      "go",
    ),
    [],
  );
  assert.equal(
    policyOwnershipErrors(
      "engine/agent-runtime/runtime.go",
      "package agentruntime\nfunc RoutingDecision() {}\n",
      "go",
    ).length,
    1,
  );
});

test("policy-shaped paths are rejected symmetrically across every boundary layer", () => {
  for (const path of [
    "ui/eligibility/board.client.tsx",
    "rpc/scheduler/status.shared.ts",
    "generated/taskstore/schema.shared.ts",
    "connector/retry/attempt.server.ts",
    "engine/adapters/projection/reader.go",
    "engine/agent-runtime/closure/claim.go",
  ]) {
    assert.equal(policyPathErrors(path).length, 1, `admitted policy path ${path}`);
  }

  for (const path of [
    "ui/components/board.client.tsx",
    "rpc/status.shared.ts",
    "generated/host-contract.shared.ts",
    "connector/paseo.server.ts",
    "engine/adapters/dolt/store.go",
    "engine/agent-runtime/claim.go",
  ]) {
    assert.deepEqual(policyPathErrors(path), [], `rejected legitimate path ${path}`);
  }
});

test("TypeScript boundaries reject host SDK escape, reverse imports, policy, and mixed suffixes", () => {
  const errors = typescriptBoundaryErrors([
    {
      path: "ui/board.client.tsx",
      source: 'import "@getpaseo/client";\nimport "../connector/paseo.server";\n',
    },
    {
      path: "connector/policy.server.ts",
      source: "export class ClosurePolicy {}\n",
    },
    {
      path: "connector/retry-policy.server.ts",
      source: "export const mode = 'fixed';\n",
    },
    {
      path: "connector/configuration-revision.server.ts",
      source: "export interface OrganizerRevisionState {}\n",
    },
    {
      path: "ui/eligibility/board.client.tsx",
      source: "export const BoardView = {};\n",
    },
    {
      path: "rpc/scheduler/status.shared.ts",
      source: "export function loadStatus() {}\n",
    },
    {
      path: "generated/taskstore/schema.shared.ts",
      source: "export interface ClaimEnvelope {}\n",
    },
    {
      path: "ui/gates.client.ts",
      source: "export function decideEligibility() {}\n",
    },
    {
      path: "rpc/gates.shared.ts",
      source: "export class EligibilityChecker {}\n",
    },
    {
      path: "generated/gates.shared.ts",
      source: "export const computeRetryBackoff = () => 0;\n",
    },
    {
      path: "rpc/status.server.ts",
      source: 'import "node:fs";\n',
    },
    {
      path: "index.ts",
      source: 'const RetryPolicy = {};\nvoid import("./connector/paseo.server");\n',
    },
  ]);
  assert.ok(errors.some((error) => error.includes("only the minimum Paseo connector")));
  assert.ok(errors.some((error) => error.includes("ui boundary cannot import connector")));
  assert.ok(errors.some((error) => error.includes("policy-shaped symbol ClosurePolicy")));
  assert.ok(errors.some((error) => error.includes("connector path cannot own")));
  assert.ok(errors.some((error) => error.includes("OrganizerRevisionState")));
  assert.ok(errors.some((error) => error.includes("rpc runtime filename")));
  assert.ok(errors.some((error) => error.includes("rpc boundary cannot import Node")));
  assert.ok(errors.some((error) => error.includes("policy-shaped symbol RetryPolicy")));
  for (const path of [
    "ui/eligibility/board.client.tsx",
    "rpc/scheduler/status.shared.ts",
    "generated/taskstore/schema.shared.ts",
  ]) {
    assert.ok(
      errors.some(
        (error) => error.startsWith(path) && error.includes("path cannot own"),
      ),
    );
  }
  for (const identifier of [
    "decideEligibility",
    "EligibilityChecker",
    "computeRetryBackoff",
  ]) {
    assert.ok(
      errors.some((error) => error.includes(`policy-shaped symbol ${identifier}`)),
    );
  }

  assert.deepEqual(
    typescriptBoundaryErrors([
      {
        path: "ui/card.client.tsx",
        source: "export function renderCard() {}\n",
      },
      {
        path: "rpc/status.shared.ts",
        source: "export interface StatusResponse {}\n",
      },
      {
        path: "generated/host.shared.ts",
        source: "export type HostObservation = unknown;\n",
      },
      {
        path: "connector/paseo.server.ts",
        source: "export class PaseoAdapter {}\n",
      },
    ]),
    [],
  );
});

test("configuration revision policy guards cover every TypeScript host boundary", () => {
  const boundaries = [
    ["ui", "client.ts"],
    ["rpc", "shared.ts"],
    ["generated", "shared.ts"],
    ["connector", "server.ts"],
  ];
  const identifiers = [
    "OrganizerRevisionState",
    "ConfigurationRevisionState",
    "RunConfigurationSnapshot",
  ];

  for (const [role, suffix] of boundaries) {
    for (const identifier of identifiers) {
      const path = `${role}/status.${suffix}`;
      const errors = typescriptBoundaryErrors([
        { path, source: `export interface ${identifier} {}\n` },
      ]);
      assert.ok(
        errors.some((error) =>
          error.includes(`policy-shaped symbol ${identifier}`)
        ),
        `admitted ${identifier} in ${role}`,
      );
    }

    const path = `${role}/configuration-revision/status.${suffix}`;
    const errors = typescriptBoundaryErrors([
      { path, source: "export interface HostStatus {}\n" },
    ]);
    assert.ok(
      errors.some((error) => error.startsWith(path) && error.includes("path cannot own")),
      `admitted configuration revision path in ${role}`,
    );
  }
});

test("only the exact canonical engine host contract is admitted", () => {
  const canonicalPath = "engine/ports/host/host-interface.v1.json";
  const canonicalSource = readFileSync(resolve(repositoryRoot, canonicalPath), "utf8");
  assert.deepEqual(
    hostContractErrors([{ path: canonicalPath, source: canonicalSource }]),
    [],
  );

  for (const alternative of [
    { path: "engine/host-interface.v2.json", source: "{}" },
    { path: "engine/adapters/nested/host-interface.v2.json", source: "{}" },
    { path: "engine/ports/host2/host-contract.v1.json", source: "{}" },
    {
      path: "engine/ports/alternate/schema.v2.json",
      source: canonicalSource,
    },
  ]) {
    assert.deepEqual(
      hostContractErrors([
        { path: canonicalPath, source: canonicalSource },
        alternative,
      ]),
      [
        "engine/ports/host: exactly one engine-owned versioned host interface is required",
      ],
      `admitted alternative host contract ${alternative.path}`,
    );
  }
});

test("standalone architecture checks diagnose tracked dangling symlinks", () => {
  const temporaryRoot = mkdtempSync(
    join(tmpdir(), "director-architecture-dangling-"),
  );
  const danglingPath = "connector/gone.server.ts";
  try {
    for (const path of [
      "tools/ci/architecture-check.mjs",
      "tools/ci/scaffold-check.mjs",
    ]) {
      const destination = resolve(temporaryRoot, path);
      mkdirSync(dirname(destination), { recursive: true });
      copyFileSync(resolve(repositoryRoot, path), destination);
    }
    mkdirSync(resolve(temporaryRoot, "connector"), { recursive: true });
    symlinkSync("missing-target", resolve(temporaryRoot, danglingPath));
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

    const result = spawnSync(
      process.execPath,
      [resolve(temporaryRoot, "tools/ci/architecture-check.mjs")],
      { cwd: temporaryRoot, encoding: "utf8" },
    );
    assert.equal(result.status, 1);
    assert.equal(
      result.stderr.trim(),
      [
        "Architecture boundary checks failed:",
        `- ${danglingPath}: tracked repository symlinks require an explicit policy`,
      ].join("\n"),
    );
    assert.doesNotMatch(result.stderr, /ENOENT|node:fs|\n\s+at\s/);
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});

test("required reducer, outcome-schema, and split-host homes fail closed when absent", () => {
  const errors = structureErrors([], []);
  assert.ok(errors.some((error) => error.includes("required Go architecture boundary")));
  assert.ok(errors.some((error) => error.includes("required pure reducer home")));
  assert.ok(errors.some((error) => error.includes("exactly seven closed schemas")));
  assert.ok(errors.some((error) => error.includes("one engine-owned versioned host interface")));
  assert.ok(errors.some((error) => error.includes("engine-owned paseo-director.json schema")));
  assert.ok(errors.some((error) => error.includes("required configuration boundary")));
  assert.ok(errors.some((error) => error.includes("generated engine client")));
});
