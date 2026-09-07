#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { readFileSync, realpathSync } from "node:fs";
import { posix, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  repositoryFiles,
  repositorySymlinkErrors,
} from "./scaffold-check.mjs";

const ENGINE_MODULE = "github.com/mcuadros/director-engine";
const GO_ROOTS = [
  "domain",
  "reducer",
  "projection",
  "ports",
  "application",
  "adapters",
  "agent-runtime",
];
const REDUCERS = [
  "eligibility",
  "launch",
  "retry",
  "escalation",
  "routing",
  "closure",
];
const OUTCOMES = [
  "completed",
  "needs_validation",
  "needs_review",
  "needs_human_decision",
  "blocked_by_dependency",
  "blocked_by_access",
  "budget_exhausted",
];
const GO_ALLOWED_DEPENDENCIES = {
  domain: new Set(["domain"]),
  reducer: new Set(["domain", "reducer"]),
  projection: new Set(["domain"]),
  ports: new Set(["domain", "ports"]),
  application: new Set(["domain", "reducer", "projection", "ports"]),
  adapters: new Set(["domain", "ports", "adapters"]),
  "agent-runtime": new Set(["domain", "application", "agent-runtime"]),
  cmd: new Set([...GO_ROOTS, "cmd"]),
};
const PURE_GO_ROLES = new Set(["domain", "reducer", "projection"]);
const IMPURE_STANDARD_IMPORTS = [
  "crypto/rand",
  "database",
  "log",
  "math/rand",
  "net",
  "os",
  "runtime",
  "syscall",
  "time",
  "unsafe",
];
const TS_ALLOWED_DEPENDENCIES = {
  composition: new Set(["connector", "rpc", "ui"]),
  ui: new Set(["rpc", "ui"]),
  rpc: new Set(["generated", "rpc"]),
  generated: new Set(["generated"]),
  connector: new Set(["connector", "generated", "rpc"]),
};
const TS_ALLOWED_EXTERNAL_IMPORTS = {
  composition: new Set(["@getpaseo/plugin"]),
  ui: new Set([
    "@getpaseo/plugin",
    "@getpaseo/plugin/react-native",
    "@tanstack/react-query",
    "react",
    "react-native",
  ]),
  rpc: new Set(["@getpaseo/plugin/server", "zod"]),
  generated: new Set(),
  connector: new Set(["@getpaseo/client"]),
};
const POLICY_DECLARATION = /(?:Reducer|Policy|Scheduler|Orchestrator|TaskStore|Reconciler|StateTransition|DomainModel|ApplicationService|LifecycleDecision|LifecycleTransition|(?:Eligibility|Launch|Retry|Escalation|Routing|Closure)Decision)$/i;
const ADAPTER_RUNTIME_POLICY_DECLARATION = /(?:Domain|Application|Orchestrat|Eligib|Schedul|Retry|Escalat|Rout|Reconcil|StateTransition|TaskStore|Projection|Closure|Reducer|Policy|OrganizerRevisionState|ConfigurationRevisionState|RunConfigurationSnapshot)/i;
const POLICY_PATH = /(?:^|\/)(?:domain|application|orchestration|eligibility|scheduler|scheduling|retry|escalation|routing|reconciliation|state-transition|taskstore|projection|closure|reducers?|organizer-revision|configuration-revision|revision-state|run-configuration-snapshot)(?:[./_-]|$)/i;
const CANONICAL_HOST_CONTRACT = "engine/ports/host/host-interface.v1.json";
const POLICY_GUARDED_PATH_PREFIXES = [
  ["ui", "ui/"],
  ["rpc", "rpc/"],
  ["generated", "generated/"],
  ["connector", "connector/"],
  ["adapters", "engine/adapters/"],
  ["agent-runtime", "engine/agent-runtime/"],
];

export function classifyGoPackage(importPath) {
  if (importPath === ENGINE_MODULE) return null;
  if (!importPath.startsWith(`${ENGINE_MODULE}/`)) return null;
  const relativePath = importPath.slice(ENGINE_MODULE.length + 1);
  if (relativePath === "cmd" || relativePath.startsWith("cmd/")) return "cmd";
  return GO_ROOTS.find(
    (root) => relativePath === root || relativePath.startsWith(`${root}/`),
  ) ?? null;
}

function standardImportMatches(importPath, prefix) {
  return importPath === prefix || importPath.startsWith(`${prefix}/`);
}

function declarations(source, language) {
  const identifiers = [];
  const patterns = language === "go"
    ? [
        /\bfunc\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)/g,
        /\b(?:type|var|const)\s+([A-Za-z_]\w*)/g,
      ]
    : [
        /\b(?:class|function|interface|type|enum|const|let|var)\s+([A-Za-z_$][\w$]*)/g,
      ];
  for (const pattern of patterns) {
    for (const match of source.matchAll(pattern)) identifiers.push(match[1]);
  }
  return identifiers;
}

export function policyOwnershipErrors(path, source, language) {
  const declarationPattern = /^(?:engine\/(?:adapters|agent-runtime)|ui|rpc|generated|connector)\//.test(path)
    ? ADAPTER_RUNTIME_POLICY_DECLARATION
    : POLICY_DECLARATION;
  return declarations(source, language)
    .filter((identifier) => declarationPattern.test(identifier))
    .map(
      (identifier) =>
        `${path}: boundary adapter/runtime declares policy-shaped symbol ${identifier}`,
    );
}

export function policyPathErrors(path) {
  const boundary = POLICY_GUARDED_PATH_PREFIXES.find(([, prefix]) =>
    path.startsWith(prefix),
  );
  if (!boundary) return [];
  const [role, prefix] = boundary;
  return POLICY_PATH.test(path.slice(prefix.length))
    ? [`${path}: ${role} path cannot own a Director policy boundary`]
    : [];
}

export function goDependencyErrors(packages) {
  const errors = [];
  for (const current of packages) {
    const role = classifyGoPackage(current.importPath);
    if (!role) {
      errors.push(`${current.importPath}: Go package is outside an approved engine boundary`);
      continue;
    }
    for (const imported of current.imports) {
      if (!imported.includes(".")) {
        if (
          PURE_GO_ROLES.has(role) &&
          IMPURE_STANDARD_IMPORTS.some((prefix) =>
            standardImportMatches(imported, prefix),
          )
        ) {
          errors.push(
            `${current.importPath}: pure ${role} package imports effectful standard package ${imported}`,
          );
        }
        continue;
      }
      if (!imported.startsWith(`${ENGINE_MODULE}/`)) {
        if (
          role === "adapters" &&
          !/(?:paseo|director-for-paseo)/i.test(imported)
        ) {
          continue;
        }
        errors.push(
          `${current.importPath}: standalone engine imports external package ${imported}`,
        );
        continue;
      }
      const importedRole = classifyGoPackage(imported);
      if (!importedRole) {
        errors.push(
          `${current.importPath}: imports unclassified engine package ${imported}`,
        );
        continue;
      }
      if (!GO_ALLOWED_DEPENDENCIES[role].has(importedRole)) {
        errors.push(
          `${current.importPath}: ${role} boundary cannot import ${importedRole} package ${imported}`,
        );
      }
      if (
        role === "reducer" &&
        importedRole === "reducer" &&
        imported !== `${ENGINE_MODULE}/reducer`
      ) {
        errors.push(
          `${current.importPath}: one decision reducer cannot depend on another reducer package ${imported}`,
        );
      }
    }
  }
  return errors;
}

function classifyTypescriptPath(path) {
  if (path === "index.ts") return "composition";
  for (const role of ["ui", "rpc", "generated", "connector"]) {
    if (path.startsWith(`${role}/`)) return role;
  }
  return null;
}

function importSpecifiers(source) {
  const imports = new Set();
  const pattern = /\b(?:import|export)\s+(?:type\s+)?(?:[^"'`;]*?\s+from\s+)?["']([^"']+)["']/g;
  for (const match of source.matchAll(pattern)) imports.add(match[1]);
  for (const match of source.matchAll(/\bimport\s*\(\s*["']([^"']+)["']\s*\)/g)) {
    imports.add(match[1]);
  }
  return [...imports];
}

function packageName(specifier) {
  if (specifier.startsWith("@getpaseo/plugin/")) return specifier;
  if (specifier.startsWith("@")) return specifier.split("/").slice(0, 2).join("/");
  return specifier.split("/")[0];
}

function relativeTypescriptRole(path, specifier) {
  const target = posix.normalize(posix.join(posix.dirname(path), specifier));
  return classifyTypescriptPath(target);
}

function validRuntimeSuffix(path, role) {
  if (role === "composition") return path === "index.ts";
  if (role === "ui") return /\.client\.tsx?$/.test(path);
  if (role === "rpc" || role === "generated") return /\.shared\.ts$/.test(path);
  return /\.server\.ts$/.test(path);
}

export function typescriptBoundaryErrors(files) {
  const errors = [];
  for (const { path, source } of files) {
    const role = classifyTypescriptPath(path);
    if (!role) {
      errors.push(`${path}: TypeScript runtime is outside ui/rpc/generated/connector boundaries`);
      continue;
    }
    if (!validRuntimeSuffix(path, role)) {
      errors.push(`${path}: ${role} runtime filename has the wrong Paseo 0.7 suffix`);
    }
    errors.push(...policyPathErrors(path));
    errors.push(...policyOwnershipErrors(path, source, "typescript"));
    for (const specifier of importSpecifiers(source)) {
      if (specifier.startsWith(".")) {
        const importedRole = relativeTypescriptRole(path, specifier);
        if (!importedRole || !TS_ALLOWED_DEPENDENCIES[role].has(importedRole)) {
          errors.push(
            `${path}: ${role} boundary cannot import ${importedRole ?? "unclassified"} module ${specifier}`,
          );
        }
        continue;
      }
      if (specifier.startsWith("node:")) {
        if (role !== "connector") {
          errors.push(`${path}: ${role} boundary cannot import Node module ${specifier}`);
        }
        continue;
      }
      const dependency = packageName(specifier);
      if (!TS_ALLOWED_EXTERNAL_IMPORTS[role].has(dependency)) {
        errors.push(
          `${path}: ${role} boundary cannot import external package ${specifier}`,
        );
      }
      if (
        dependency === "@getpaseo/client" &&
        path !== "connector/paseo.server.ts"
      ) {
        errors.push(
          `${path}: only the minimum Paseo connector may import @getpaseo/client`,
        );
      }
    }
  }
  return errors;
}

function hasHostContractPathShape(path) {
  if (!path.startsWith("engine/") || !path.endsWith(".json")) return false;
  const relativePath = path.slice("engine/".length).toLowerCase();
  const tokens = relativePath.split(/[\/._-]+/);
  return (
    relativePath.startsWith("ports/host/") ||
    (tokens.includes("host") &&
      (tokens.includes("interface") || tokens.includes("contract")))
  );
}

function hasHostContractContentShape(source) {
  if (typeof source !== "string") return false;
  try {
    const value = JSON.parse(source);
    return (
      value !== null &&
      typeof value === "object" &&
      Array.isArray(value.capabilities) &&
      (value.command !== undefined || value.observation !== undefined)
    );
  } catch {
    return false;
  }
}

export function hostContractErrors(files) {
  const hostContracts = files.filter(
    ({ path, source }) =>
      path === CANONICAL_HOST_CONTRACT ||
      hasHostContractPathShape(path) ||
      (path.startsWith("engine/") &&
        path.endsWith(".json") &&
        hasHostContractContentShape(source)),
  );
  if (
    hostContracts.length === 1 &&
    hostContracts[0].path === CANONICAL_HOST_CONTRACT
  ) {
    return [];
  }
  return [
    "engine/ports/host: exactly one engine-owned versioned host interface is required",
  ];
}

export function structureErrors(paths, packagePaths, fileSources = new Map()) {
  const errors = [];
  const pathSet = new Set(paths);
  const packageSet = new Set(packagePaths);
  for (const root of GO_ROOTS) {
    if (!packageSet.has(`${ENGINE_MODULE}/${root}`)) {
      errors.push(`engine/${root}: required Go architecture boundary is missing`);
    }
  }
  for (const reducer of REDUCERS) {
    if (!packageSet.has(`${ENGINE_MODULE}/reducer/${reducer}`)) {
      errors.push(`engine/reducer/${reducer}: required pure reducer home is missing`);
    }
  }
  const schemaPrefix = "engine/domain/agentoutcome/schemas/";
  const actualSchemas = paths
    .filter((path) => path.startsWith(schemaPrefix) && path.endsWith(".schema.json"))
    .map((path) => path.slice(schemaPrefix.length, -".schema.json".length))
    .sort();
  const expectedSchemas = [...OUTCOMES].sort();
  if (
    actualSchemas.length !== expectedSchemas.length ||
    actualSchemas.some((name, index) => name !== expectedSchemas[index])
  ) {
    errors.push(
      `engine/domain/agentoutcome: expected exactly seven closed schemas (${expectedSchemas.join(", ")})`,
    );
  }
  errors.push(
    ...hostContractErrors(
      paths.map((path) => ({ path, source: fileSources.get(path) })),
    ),
  );
  const configurationSchemas = paths.filter((path) =>
    path.endsWith("/paseo-director.schema.json"),
  );
  if (
    configurationSchemas.length !== 1 ||
    configurationSchemas[0] !==
      "engine/domain/configuration/paseo-director.schema.json"
  ) {
    errors.push(
      "engine/domain/configuration: exactly one engine-owned paseo-director.json schema is required",
    );
  }
  for (const requiredPackage of [
    `${ENGINE_MODULE}/domain/configuration`,
    `${ENGINE_MODULE}/application/configuration`,
  ]) {
    if (!packageSet.has(requiredPackage)) {
      errors.push(`${requiredPackage}: required configuration boundary is missing`);
    }
  }
  if (!pathSet.has("generated/host-contract.shared.ts")) {
    errors.push("generated: the generated engine client is missing");
  }
  for (const requiredHostPath of [
    "connector/paseo.server.ts",
    "rpc/startup.shared.ts",
    "ui/shells.client.tsx",
  ]) {
    if (!pathSet.has(requiredHostPath)) {
      errors.push(`${requiredHostPath}: required split Paseo host boundary is missing`);
    }
  }
  for (const legacy of ["client/", "server/", "shared/"]) {
    if (paths.some((path) => path.startsWith(legacy) && /\.[cm]?[jt]sx?$/.test(path))) {
      errors.push(`${legacy}: legacy mixed TypeScript runtime boundary is prohibited`);
    }
  }
  return errors;
}

function listGoPackages(repositoryRoot) {
  const result = spawnSync(
    "go",
    [
      "-C",
      "engine",
      "list",
      "-buildvcs=false",
      "-f",
      '{{.ImportPath}}\t{{join .Imports ","}}\t{{join .TestImports ","}}\t{{join .XTestImports ","}}',
      "./...",
    ],
    {
      cwd: repositoryRoot,
      encoding: "utf8",
      env: { ...process.env, GOTOOLCHAIN: "local" },
    },
  );
  if (result.status !== 0) {
    throw new Error(result.stderr.trim() || result.error?.message || "go list failed");
  }
  return result.stdout
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => {
      const [importPath, imports = "", testImports = "", externalTestImports = ""] =
        line.split("\t");
      return {
        importPath,
        imports: [...new Set(
          [imports, testImports, externalTestImports]
            .flatMap((group) => group.split(","))
            .filter(Boolean),
        )],
      };
    });
}

export function architectureErrors(repositoryRoot) {
  const paths = repositoryFiles(repositoryRoot);
  const symlinkErrors = repositorySymlinkErrors(repositoryRoot, paths);
  if (symlinkErrors.length > 0) return symlinkErrors;
  const packages = listGoPackages(repositoryRoot);
  const fileSources = new Map(
    paths
      .filter((path) => path.startsWith("engine/") && path.endsWith(".json"))
      .map((path) => [
        path,
        readFileSync(resolve(repositoryRoot, path), "utf8"),
      ]),
  );
  const errors = [
    ...structureErrors(
      paths,
      packages.map(({ importPath }) => importPath),
      fileSources,
    ),
    ...goDependencyErrors(packages),
  ];
  for (const path of paths.filter((candidate) => candidate.endsWith(".go"))) {
    const importPath = `${ENGINE_MODULE}/${posix.dirname(path).replace(/^engine\/?/, "")}`;
    const role = classifyGoPackage(importPath);
    if (role === "adapters" || role === "agent-runtime") {
      errors.push(...policyPathErrors(path));
      errors.push(
        ...policyOwnershipErrors(
          path,
          readFileSync(resolve(repositoryRoot, path), "utf8"),
          "go",
        ),
      );
    }
  }
  const typescriptFiles = paths
    .filter(
      (path) =>
        /\.[cm]?[jt]sx?$/.test(path) &&
        !["docs/", "tests/", "tools/"].some((prefix) => path.startsWith(prefix)),
    )
    .map((path) => ({
      path,
      source: readFileSync(resolve(repositoryRoot, path), "utf8"),
    }));
  errors.push(...typescriptBoundaryErrors(typescriptFiles));
  return errors;
}

function run(repositoryRoot) {
  const errors = architectureErrors(repositoryRoot);
  if (errors.length > 0) {
    console.error("Architecture boundary checks failed:");
    errors.forEach((error) => console.error(`- ${error}`));
    return 1;
  }
  console.log(
    "Architecture boundaries passed: inward Go dependencies, engine-owned configuration/revisions, six pure reducer homes, seven closed claim schemas, and split Paseo host runtimes.",
  );
  return 0;
}

const modulePath = realpathSync(fileURLToPath(import.meta.url));
const invokedPath = process.argv[1]
  ? realpathSync(resolve(process.argv[1]))
  : null;
if (invokedPath === modulePath) {
  process.exitCode = run(fileURLToPath(new URL("../../", import.meta.url)));
}
