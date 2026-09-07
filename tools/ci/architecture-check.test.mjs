// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  architectureErrors,
  goDependencyErrors,
  policyOwnershipErrors,
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
