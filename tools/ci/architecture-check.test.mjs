// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
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
  architectureErrors,
  goDependencyErrors,
  goListFailureMessage,
  hostContractErrors,
  policyOwnershipErrors,
  policyPathErrors,
  structureErrors,
  typescriptBoundaryErrors,
} from "./architecture-check.mjs";

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const modulePath = "github.com/mcuadros/director-engine";

async function loadMutatedArchitectureCheck(mutate) {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-architecture-mutant-"));
  const sourcePath = resolve(repositoryRoot, "tools/ci/architecture-check.mjs");
  const destinationPath = resolve(temporaryRoot, "tools/ci/architecture-check.mjs");
  mkdirSync(dirname(destinationPath), { recursive: true });
  copyFileSync(
    resolve(repositoryRoot, "tools/ci/scaffold-check.mjs"),
    resolve(temporaryRoot, "tools/ci/scaffold-check.mjs"),
  );
  const source = readFileSync(sourcePath, "utf8");
  const mutated = mutate(source);
  assert.notEqual(mutated, source, "mutation must change the architecture checker");
  writeFileSync(destinationPath, mutated, { mode: 0o600 });
  return {
    checker: await import(pathToFileURL(destinationPath).href),
    temporaryRoot,
  };
}

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
    {
      importPath: `${modulePath}/adapters/taskstore`,
      imports: ["github.com/gastownhall/beads"],
    },
    {
      importPath: `${modulePath}/ports/taskstore`,
      imports: ["context", "database/sql", `${modulePath}/domain`],
    },
  ]);
  assert.ok(errors.some((error) => error.includes("domain boundary cannot import application")));
  assert.ok(errors.some((error) => error.includes("cannot depend on another reducer")));
  assert.ok(errors.some((error) => error.includes("effectful standard package time")));
  assert.ok(errors.some((error) => error.includes("imports external package")));
  assert.ok(
    errors.some((error) =>
      error.includes("cannot leak database backend package database/sql"),
    ),
  );
  assert.ok(
    errors.some(
      (error) =>
        error.includes(`${modulePath}/adapters/host`) &&
        error.includes("github.com/getpaseo/client"),
    ),
  );
  assert.ok(
    errors.some(
      (error) =>
        error.includes(`${modulePath}/adapters/taskstore`) &&
        error.includes("github.com/gastownhall/beads"),
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

test("planning testkit is isolated from every product boundary", () => {
  const testkit = `${modulePath}/internal/planningtestkit`;
  assert.deepEqual(
    goDependencyErrors([{ importPath: testkit, imports: ["cmp", "slices"] }]),
    [],
  );
  assert.ok(
    goDependencyErrors([
      { importPath: testkit, imports: [`${modulePath}/domain`] },
    ]).some((error) => error.includes("testkit boundary cannot import domain")),
  );
  assert.ok(
    goDependencyErrors([
      { importPath: `${modulePath}/domain`, imports: [testkit] },
    ]).some((error) => error.includes("domain boundary cannot import testkit")),
  );
  assert.ok(
    goDependencyErrors([
      { importPath: `${modulePath}/internal/runtime-policy`, imports: [] },
    ]).some((error) => error.includes("outside an approved engine boundary")),
  );
});

test("product tests alone may import the exact planning testkit", () => {
  const testkit = `${modulePath}/internal/planningtestkit`;
  assert.deepEqual(
    goDependencyErrors([
      {
        importPath: `${modulePath}/domain/planning`,
        imports: ["fmt"],
        testImports: ["testing", testkit],
      },
      {
        importPath: `${modulePath}/application/planning`,
        imports: [`${modulePath}/domain`],
        xTestImports: ["testing", testkit],
      },
    ]),
    [],
  );

  const runtimeErrors = goDependencyErrors([
    {
      importPath: `${modulePath}/domain/planning`,
      imports: [testkit],
      testImports: [],
      xTestImports: [],
    },
  ]);
  assert.ok(
    runtimeErrors.some((error) =>
      error.includes("domain boundary cannot import testkit"),
    ),
  );

  const reciprocalErrors = goDependencyErrors([
    {
      importPath: testkit,
      imports: [`${modulePath}/domain/planning`],
      testImports: [],
      xTestImports: [],
    },
  ]);
  assert.ok(
    reciprocalErrors.some((error) =>
      error.includes("testkit boundary cannot import domain"),
    ),
  );

  assert.ok(
    goDependencyErrors([
      {
        importPath: `${modulePath}/domain/planning`,
        imports: [],
        testImports: [`${testkit}/other`],
      },
    ]).some((error) => error.includes("imports unclassified engine package")),
  );
});

test("projection oracle is an exact independent testkit boundary", () => {
  const planningTestkit = `${modulePath}/internal/planningtestkit`;
  const projectionOracle =
    `${modulePath}/internal/testkit/projectionoracle`;

  assert.deepEqual(
    goDependencyErrors([
      {
        importPath: projectionOracle,
        imports: [
          "encoding/base64",
          "encoding/json",
          "errors",
          "slices",
          "strconv",
        ],
        testImports: ["testing"],
      },
      {
        importPath: `${modulePath}/projection`,
        imports: [`${modulePath}/domain`],
        testImports: ["testing", projectionOracle],
      },
      {
        importPath: `${modulePath}/application/board`,
        imports: [`${modulePath}/projection`],
        xTestImports: ["testing", projectionOracle],
      },
    ]),
    [],
  );

  for (const current of [
    {
      importPath: `${modulePath}/projection`,
      imports: [projectionOracle],
    },
    {
      importPath: projectionOracle,
      imports: [`${modulePath}/projection`],
    },
    {
      importPath: projectionOracle,
      imports: [planningTestkit],
    },
    {
      importPath: planningTestkit,
      imports: [projectionOracle],
    },
  ]) {
    assert.ok(goDependencyErrors([current]).length > 0);
  }

  assert.ok(
    goDependencyErrors([
      {
        importPath: `${modulePath}/projection`,
        imports: [],
        testImports: [`${projectionOracle}/other`],
      },
    ]).some((error) => error.includes("imports unclassified engine package")),
  );
});

test("configuration oracle is an exact test-only architecture role", () => {
  const oracle = `${modulePath}/internal/testkit/configoracle`;
  assert.deepEqual(
    goDependencyErrors([
      {
        importPath: oracle,
        imports: ["cmp", "slices"],
      },
      {
        importPath: `${modulePath}/domain/configuration`,
        imports: ["fmt"],
        testImports: ["testing", oracle],
      },
      {
        importPath: `${modulePath}/application/configuration`,
        imports: [`${modulePath}/domain/configuration`],
        xTestImports: ["testing", oracle],
      },
    ]),
    [],
  );

  assert.ok(
    goDependencyErrors([
      {
        importPath: `${modulePath}/domain/configuration`,
        imports: [oracle],
      },
    ]).some((error) => error.includes("domain boundary cannot import testkit")),
  );
  assert.ok(
    goDependencyErrors([
      {
        importPath: oracle,
        imports: [`${modulePath}/domain/configuration`],
      },
    ]).some((error) => error.includes("testkit boundary cannot import domain")),
  );
  assert.ok(
    goDependencyErrors([
      {
        importPath: oracle,
        imports: [],
        testImports: [`${modulePath}/application/configuration`],
      },
    ]).some((error) => error.includes("testkit boundary cannot import application")),
  );

  for (const nonExact of [
    `${modulePath}/internal/testkit`,
    `${oracle}/runtime`,
    `${modulePath}/internal/testkit/config-oracle`,
  ]) {
    assert.ok(
      goDependencyErrors([
        {
          importPath: `${modulePath}/domain/configuration`,
          imports: [],
          testImports: [nonExact],
        },
      ]).some((error) => error.includes("imports unclassified engine package")),
      `${nonExact} was admitted as an exact configuration-oracle test import`,
    );
  }
});

test("product tests alone may import the exact scheduler oracle", () => {
  const oracle = `${modulePath}/internal/testkit/scheduleroracle`;
  assert.deepEqual(
    goDependencyErrors([
      {
        importPath: `${modulePath}/reducer/scheduler`,
        imports: ["slices"],
        testImports: ["testing", oracle],
      },
      {
        importPath: oracle,
        imports: ["slices"],
        xTestImports: ["testing", oracle],
      },
    ]),
    [],
  );
  assert.ok(
    goDependencyErrors([
      {
        importPath: `${modulePath}/reducer/scheduler`,
        imports: [oracle],
      },
    ]).some((error) => error.includes("reducer boundary cannot import testkit")),
  );
  assert.ok(
    goDependencyErrors([
      {
        importPath: oracle,
        imports: [`${modulePath}/reducer/scheduler`],
      },
    ]).some((error) => error.includes("testkit boundary cannot import reducer")),
  );
});

test("execution oracle is an exact independent test-source-only boundary", () => {
  const oracle = `${modulePath}/internal/testkit/executionoracle`;
  const otherTestkit = `${modulePath}/internal/testkit/scheduleroracle`;
  assert.deepEqual(
    goDependencyErrors([
      {
        importPath: oracle,
        imports: ["errors", "math/bits", "slices", "sync"],
        testImports: ["testing"],
      },
      {
        importPath: `${modulePath}/application/execution`,
        imports: [`${modulePath}/domain`],
        testImports: ["testing", oracle],
      },
    ]),
    [],
  );

  for (const current of [
    {
      importPath: `${modulePath}/application/execution`,
      imports: [oracle],
    },
    {
      importPath: oracle,
      imports: [`${modulePath}/domain/execution`],
    },
    {
      importPath: oracle,
      testImports: [otherTestkit],
    },
    {
      importPath: otherTestkit,
      xTestImports: [oracle],
    },
  ]) {
    assert.ok(goDependencyErrors([current]).length > 0);
  }

  assert.ok(
    goDependencyErrors([
      {
        importPath: `${modulePath}/application/execution`,
        testImports: [`${oracle}/runtime`],
      },
    ]).some((error) => error.includes("imports unclassified engine package")),
  );
});

test("credential canaries are confined to the exact test-only fixture package", () => {
  const fixture = `${modulePath}/internal/testkit/secretfixture`;
  assert.deepEqual(
    goDependencyErrors([
      { importPath: fixture, imports: ["strings"] },
      {
        importPath: `${modulePath}/domain/safedata`,
        imports: ["regexp", "strings"],
        testImports: ["testing", fixture],
      },
    ]),
    [],
  );
  assert.ok(
    goDependencyErrors([
      { importPath: `${modulePath}/domain/safedata`, imports: [fixture] },
    ]).some((error) => error.includes("domain boundary cannot import testkit")),
  );
  assert.ok(
    goDependencyErrors([
      { importPath: fixture, imports: [`${modulePath}/domain/safedata`] },
    ]).some((error) => error.includes("testkit boundary cannot import domain")),
  );
  assert.ok(
    goDependencyErrors([
      {
        importPath: `${modulePath}/domain/safedata`,
        testImports: [`${fixture}/runtime`],
      },
    ]).some((error) => error.includes("imports unclassified engine package")),
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

  for (const identifier of [
    "TaskStoreAdapter",
    "DoltTaskStore",
    "ProjectionReader",
    "DomainEventRow",
  ]) {
    assert.deepEqual(
      policyOwnershipErrors(
        "engine/adapters/dolt/taskstore.go",
        `package dolt\ntype ${identifier} struct{}\n`,
        "go",
      ),
      [],
      `${identifier} is a readable adapter/data-access name, not policy ownership`,
    );
  }

  for (const identifier of [
    "TaskStorePolicy",
    "ProjectionPolicy",
    "DomainModel",
    "ApplicationService",
    "LifecycleTransition",
    "RetryDecision",
    "PolicyEngine",
    "SchedulerLoop",
    "OrchestratorState",
    "ReducerRegistry",
    "RetryDecisionMaker",
    "LifecycleTransitionTable",
    "EscalationPolicyTable",
    "ReconcilerLoop",
    "ClosureDecisionLog",
  ]) {
    assert.equal(
      policyOwnershipErrors(
        "engine/adapters/dolt/taskstore.go",
        `package dolt\ntype ${identifier} struct{}\n`,
        "go",
      ).length,
      1,
      `${identifier} must remain prohibited policy ownership`,
    );
  }

  assert.equal(
    policyOwnershipErrors(
      "engine/agent-runtime/runtime.go",
      "package agentruntime\ntype DoltTaskStore struct{}\n",
      "go",
    ).length,
    1,
    "adapter data-access allowlist must not weaken agent-runtime",
  );
});

test("connector retains domain, application, TaskStore, and projection path bans", () => {
  const errors = typescriptBoundaryErrors(
    ["domain", "application", "taskstore", "projection"].map((boundary) => ({
      path: `connector/${boundary}.server.ts`,
      source: "export const transport = true;\n",
    })),
  );
  for (const boundary of ["domain", "application", "taskstore", "projection"]) {
    assert.ok(
      errors.some(
        (error) =>
          error.includes(`connector/${boundary}.server.ts`) &&
          error.includes("connector path cannot own"),
      ),
      `${boundary} connector path must remain prohibited`,
    );
  }
});

test("adapter path exemption is limited to the exact Dolt TaskStore implementation", () => {
  assert.deepEqual(policyPathErrors("engine/adapters/dolt/taskstore.go"), []);
  for (const path of [
    "engine/adapters/domain.go",
    "engine/adapters/domain/store.go",
    "engine/adapters/application.go",
    "engine/adapters/application/x.go",
    "engine/adapters/projection.go",
    "engine/adapters/projection/reader.go",
    "engine/adapters/taskstore.go",
    "engine/adapters/dolt/taskstore_adapter.go",
  ]) {
    assert.equal(
      policyPathErrors(path).length,
      1,
      `${path} must remain prohibited outside the exact exemption`,
    );
  }
  assert.equal(
    policyPathErrors("engine/agent-runtime/dolt/taskstore.go").length,
    1,
    "the adapter path exemption must not apply to agent-runtime",
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
    "ui/projections/board.client.tsx",
    "rpc/projections/status.shared.ts",
    "connector/retries/attempt.server.ts",
    "ui/closures/card.client.tsx",
    "ui/schedulers/card.client.tsx",
    "connector/policies/transport.server.ts",
    "engine/adapters/projections/reader.go",
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
    "ui/projectors/board.client.tsx",
    "rpc/schedules/status.shared.ts",
    "connector/retrievals/transport.server.ts",
  ]) {
    assert.deepEqual(policyPathErrors(path), [], `rejected legitimate path ${path}`);
  }
});

test("runtime suffix diagnostics name the Director repository convention", () => {
  const errors = typescriptBoundaryErrors([
    { path: "ui/board.tsx", source: "export function BoardView() {}\n" },
  ]);
  assert.deepEqual(errors, [
    "ui/board.tsx: ui runtime filename does not follow the Director repository convention",
  ]);
  assert.doesNotMatch(errors.join("\n"), /Paseo 0\.7 suffix/u);
});

test("the integrated Board/List and current M5 host sources remain admitted", () => {
  const paths = [
    "index.ts",
    "ui/board-view.client.ts",
    "ui/planning-surface.client.tsx",
    "ui/task-detail-view.client.tsx",
    "ui/task-inspector.client.tsx",
  ];
  assert.deepEqual(
    typescriptBoundaryErrors(paths.map((path) => ({
      path,
      source: readFileSync(resolve(repositoryRoot, path), "utf8"),
    }))),
    [],
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
      source: "export class ClosurePolicy {}\nexport class DoltTaskStore {}\n",
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
  assert.ok(errors.some((error) => error.includes("policy-shaped symbol DoltTaskStore")));
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

test("the composition root uses the broad configuration and projection declaration guard", () => {
  for (const identifier of [
    "OrganizerRevisionState",
    "ConfigurationRevisionState",
    "RunConfigurationSnapshot",
    "TaskProjection",
  ]) {
    const errors = typescriptBoundaryErrors([
      { path: "index.ts", source: `export interface ${identifier} {}\n` },
    ]);
    assert.ok(
      errors.some((error) => error.includes(`policy-shaped symbol ${identifier}`)),
      `admitted ${identifier} in the composition root`,
    );
  }

  assert.deepEqual(
    typescriptBoundaryErrors([
      {
        path: "index.ts",
        source: "export default function contribute() {}\n",
      },
    ]),
    [],
  );
});

test("Organizer Create and Adopt orchestration paths stay out of host boundaries", () => {
  for (const role of ["ui", "rpc", "generated", "connector"]) {
    for (const boundary of ["organizer-bootstrap", "create-project", "adopt-organizer"]) {
      const path = `${role}/${boundary}/status.server.ts`;
      const errors = typescriptBoundaryErrors([
        { path, source: "export interface HostStatus {}\n" },
      ]);
      assert.ok(
        errors.some((error) => error.startsWith(path) && error.includes("path cannot own")),
        `admitted ${boundary} path in ${role}`,
      );
    }
  }
});

test("only the exact canonical engine host contract is admitted", () => {
  const canonicalPath = "engine/ports/host/host-interface.v1.json";
  const canonicalSource = readFileSync(resolve(repositoryRoot, canonicalPath), "utf8");
  assert.deepEqual(
    hostContractErrors([{ path: canonicalPath, source: canonicalSource }]),
    [],
  );
  assert.deepEqual(hostContractErrors([]), [
    `${canonicalPath}: exactly one engine-owned versioned host interface is required (found 0)`,
  ]);
  assert.deepEqual(
    hostContractErrors([
      { path: canonicalPath, source: canonicalSource },
      { path: canonicalPath, source: canonicalSource },
    ]),
    [
      `${canonicalPath}: exactly one engine-owned versioned host interface is required (found 2)`,
    ],
  );

  for (const alternative of [
    { path: "engine/host-interface.v2.json", source: "{}" },
    { path: "engine/adapters/nested/host-interface.v2.json", source: "{}" },
    { path: "engine/ports/host2/host-contract.v1.json", source: "{}" },
    { path: "rpc/host-interface.v2.json", source: "{}" },
    {
      path: "engine/ports/alternate/schema.v2.json",
      source: canonicalSource,
    },
    {
      path: "ui/assets/transport.v2.json",
      source: canonicalSource,
    },
  ]) {
    assert.deepEqual(
      hostContractErrors([
        { path: canonicalPath, source: canonicalSource },
        alternative,
      ]),
      [
        `${alternative.path}: duplicate or misplaced host interface contract; only ${canonicalPath} is permitted`,
      ],
      `admitted alternative host contract ${alternative.path}`,
    );
  }

  assert.deepEqual(
    hostContractErrors([
      { path: canonicalPath, source: canonicalSource },
      {
        path: "engine/ports/planning/planning-surface.v1.json",
        source: readFileSync(
          resolve(repositoryRoot, "engine/ports/planning/planning-surface.v1.json"),
          "utf8",
        ),
      },
      {
        path: "package.json",
        source: readFileSync(resolve(repositoryRoot, "package.json"), "utf8"),
      },
    ]),
    [],
    "legitimate non-host JSON must remain admitted",
  );

  assert.deepEqual(
    hostContractErrors([
      { path: canonicalPath, source: canonicalSource },
      { path: "rpc/z-host-interface.v2.json", source: "{}" },
      { path: "connector/a-host-contract.v2.json", source: "{}" },
    ]),
    [
      `connector/a-host-contract.v2.json: duplicate or misplaced host interface contract; only ${canonicalPath} is permitted`,
      `rpc/z-host-interface.v2.json: duplicate or misplaced host interface contract; only ${canonicalPath} is permitted`,
    ],
    "offending paths must be named in deterministic order",
  );
});

test("standalone checks diagnose a content-shaped host contract outside engine", () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-host-contract-copy-"));
  const checkout = resolve(temporaryRoot, "checkout");
  const duplicatePath = "rpc/transport.v2.json";
  try {
    const clone = spawnSync(
      "git",
      ["clone", "--quiet", "--no-hardlinks", repositoryRoot, checkout],
      { encoding: "utf8" },
    );
    assert.equal(clone.status, 0, clone.stderr);
    copyFileSync(
      resolve(repositoryRoot, "tools/ci/architecture-check.mjs"),
      resolve(checkout, "tools/ci/architecture-check.mjs"),
    );
    writeFileSync(
      resolve(checkout, duplicatePath),
      readFileSync(
        resolve(repositoryRoot, "engine/ports/host/host-interface.v1.json"),
        "utf8",
      ),
      { mode: 0o600 },
    );

    const result = spawnSync(
      process.execPath,
      [resolve(checkout, "tools/ci/architecture-check.mjs")],
      {
        cwd: checkout,
        encoding: "utf8",
        env: {
          ...process.env,
          GOCACHE: resolve(temporaryRoot, "go-cache"),
          GOWORK: "off",
        },
      },
    );
    assert.equal(result.status, 1);
    assert.equal(
      result.stderr.trim(),
      [
        "Architecture boundary checks failed:",
        `- ${duplicatePath}: duplicate or misplaced host interface contract; only engine/ports/host/host-interface.v1.json is permitted`,
      ].join("\n"),
    );
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
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
  assert.ok(errors.some((error) => error.includes("application/organizer")));
  assert.ok(errors.some((error) => error.includes("generated engine client")));
});

test("a missing or failing go toolchain fails closed with a bounded reason", () => {
  assert.equal(
    goListFailureMessage({
      status: null,
      signal: null,
      stdout: null,
      stderr: null,
      error: Object.assign(new Error("spawnSync go ENOENT"), { code: "ENOENT" }),
    }),
    "go list failed: go is required but was not found on PATH",
  );

  assert.equal(
    goListFailureMessage({ status: 1, stderr: "  engine/x: broken import  " }),
    "go list failed: engine/x: broken import",
  );

  assert.equal(
    goListFailureMessage({
      status: 1,
      stderr: "z/package: later\na/package: first\nz/package: later\n",
    }),
    "go list failed: a/package: first | z/package: later",
  );

  const flood = goListFailureMessage({ status: 1, stderr: "e".repeat(9_000) });
  assert.ok(flood.length <= 2_000, "the diagnostic must stay bounded");
  assert.match(flood, /\(truncated\)$/u);

  assert.equal(
    goListFailureMessage({
      status: null,
      stderr: null,
      error: Object.assign(new Error("host-specific detail"), { code: "EPERM" }),
    }),
    "go list failed to start: EPERM",
  );

  assert.equal(
    goListFailureMessage({ status: 2, stderr: "", error: undefined }),
    "go list failed with exit status 2",
  );
  assert.equal(
    goListFailureMessage({ status: null, signal: "SIGKILL", stderr: null }),
    "go list failed: terminated by signal SIGKILL",
  );
  assert.equal(goListFailureMessage({}), "go list failed");
  assert.equal(goListFailureMessage(undefined), "go list failed");
});

test("standalone architecture checks report go list failure without a Node stack", () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-architecture-golist-"));
  try {
    for (const path of [
      "tools/ci/architecture-check.mjs",
      "tools/ci/scaffold-check.mjs",
    ]) {
      const destination = resolve(temporaryRoot, path);
      mkdirSync(dirname(destination), { recursive: true });
      copyFileSync(resolve(repositoryRoot, path), destination);
    }
    mkdirSync(resolve(temporaryRoot, "engine"), { recursive: true });
    writeFileSync(
      resolve(temporaryRoot, "engine/go.mod"),
      "module example.com/director-architecture-fixture\n\ngo 1.22\n",
      { mode: 0o600 },
    );
    mkdirSync(resolve(temporaryRoot, "engine/ports/host"), { recursive: true });
    writeFileSync(
      resolve(temporaryRoot, "engine/ports/host/contract.go"),
      [
        "package host",
        "",
        'import _ "embed"',
        "",
        "//go:embed host-interface.v1.json",
        "var schema []byte",
        "",
      ].join("\n"),
      { mode: 0o600 },
    );
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
      {
        cwd: temporaryRoot,
        encoding: "utf8",
        env: {
          ...process.env,
          GOCACHE: resolve(temporaryRoot, ".go-cache"),
          GOWORK: "off",
        },
      },
    );
    assert.equal(result.status, 1);
    assert.match(
      result.stderr,
      /^Architecture boundary checks failed:\n- engine: go list failed: /u,
    );
    assert.match(result.stderr, /host-interface\.v1\.json/u);
    assert.doesNotMatch(result.stderr, /node:|file:\/\/|\n\s+at\s/u);
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});

test("focused architecture mutations are killed by their adversarial probes", async () => {
  const canonicalPath = "engine/ports/host/host-interface.v1.json";
  const canonicalSource = readFileSync(resolve(repositoryRoot, canonicalPath), "utf8");
  const mutations = [
    {
      name: "composition root declaration guard",
      mutate: (source) => source.replace(
        'path === "index.ts" ||',
        "false ||",
      ),
      killed: (checker) => checker.policyOwnershipErrors(
        "index.ts",
        "export interface TaskProjection {}\n",
        "typescript",
      ).length !== 1,
    },
    {
      name: "plural projection path guard",
      mutate: (source) => source.replace("projections?", "projection"),
      killed: (checker) =>
        checker.policyPathErrors("ui/projections/board.client.tsx").length !== 1,
    },
    {
      name: "whole-tree host contract content sweep",
      mutate: (source) => source.replace(
        '(path.endsWith(".json") && hasHostContractContentShape(source))',
        '(path.startsWith("engine/") && path.endsWith(".json") && hasHostContractContentShape(source))',
      ),
      killed: (checker) => checker.hostContractErrors([
        { path: canonicalPath, source: canonicalSource },
        { path: "rpc/transport.v2.json", source: canonicalSource },
      ]).length !== 1,
    },
  ];

  for (const mutation of mutations) {
    const { checker, temporaryRoot } = await loadMutatedArchitectureCheck(
      mutation.mutate,
    );
    try {
      assert.equal(mutation.killed(checker), true, `${mutation.name} survived`);
    } finally {
      rmSync(temporaryRoot, { recursive: true, force: true });
    }
  }
});
