#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  lstatSync,
  readFileSync,
  realpathSync,
} from "node:fs";
import { basename, resolve } from "node:path";
import { isDeepStrictEqual } from "node:util";
import { fileURLToPath } from "node:url";

const WORKFLOW_PATH = ".github/workflows/ci.yml";
const CHECKOUT_REF =
  "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1";
const SETUP_NODE_REF =
  "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020";
const SETUP_GO_REF =
  "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e";
const INSTALL_COMMAND =
  "npm ci --ignore-scripts --no-audit --no-fund && bash tools/ci/install-dolt-2.3.2.sh";
const CHECK_COMMAND = "npm run ci";
const LICENSE_SHA256 =
  "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30";
const EMPTY_SHA256 =
  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";
const RELEASE_ORIGIN = "https://github.com";
const RELEASE_PATH_PREFIX =
  "/mcuadros/paseo-director/releases/download/";
const REQUIRED_SCRIPTS = [
  "architecture:check",
  "build",
  "build:engine",
  "ci",
  "contract:check",
  "contract:generate",
  "engine:check",
  "format:check",
  "lint",
  "smoke",
  "test",
  "test:baseline",
  "test:engine",
  "test:host",
  "typecheck",
];
const REQUIRED_UI_REGISTRATIONS = [
  'addSurface("home", DirectorHome)',
  'id: "project-board"',
  'id: "task-inspector"',
  'id: "open-project-board"',
  'id: "open-task-inspector"',
];
const ENGINE_ALLOWED_PREFIXES = [
  "engine/adapters/",
  "engine/agent-runtime/",
  "engine/application/",
  "engine/cmd/director-engine/",
  "engine/cmd/generate-host-client/",
  "engine/cmd/generate-planning-client/",
  "engine/cmd/generate-agent-mcp-client/",
  "engine/domain/",
  "engine/internal/planningtestkit/",
  "engine/internal/testkit/projectionoracle/",
  "engine/internal/testkit/configoracle/",
  "engine/internal/testkit/scheduleroracle/",
  "engine/ports/",
  "engine/projection/",
  "engine/reducer/",
];
const FUTURE_PRODUCT_PATHS = [
  /^(?:application|domain|orchestration|projection|reducers|scheduler|taskstore)\//,
  /^connector\/(?:application|domain|orchestration|projection|reducers|scheduler|taskstore)\//,
];
const FORBIDDEN_DISTRIBUTION_PATHS = [
  /(^|\/)(?:bin|node_modules|vendor)(?:\/|$)/,
  /\.(?:dll|dylib|exe|node|so|wasm)$/i,
  /^\.gitmodules$/,
];
const RELATION_INVERSE = {
  amends: "amended_by",
  amended_by: "amends",
  supersedes: "superseded_by",
  superseded_by: "supersedes",
};

function gitPaths(repositoryRoot, args) {
  const result = spawnSync("git", args, {
    cwd: repositoryRoot,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`git ${args.join(" ")} failed: ${result.stderr.trim()}`);
  }
  return result.stdout.split("\0").filter(Boolean);
}

export function repositoryFiles(repositoryRoot) {
  return [
    ...new Set([
      ...gitPaths(repositoryRoot, ["ls-files", "-z"]),
      ...gitPaths(repositoryRoot, [
        "ls-files",
        "--others",
        "--exclude-standard",
        "-z",
      ]),
    ]),
  ]
    .filter((path) => {
      try {
        lstatSync(resolve(repositoryRoot, path));
        return true;
      } catch (error) {
        if (error?.code === "ENOENT") return false;
        throw error;
      }
    })
    .sort();
}

export function repositorySymlinkErrors(repositoryRoot, paths) {
  return paths
    .filter((path) => lstatSync(resolve(repositoryRoot, path)).isSymbolicLink())
    .map(
      (path) =>
        `${path}: tracked repository symlinks require an explicit policy`,
    );
}

export function formatErrors(repositoryRoot, paths) {
  const errors = [];
  for (const path of paths) {
    if (lstatSync(resolve(repositoryRoot, path)).isSymbolicLink()) {
      errors.push(`${path}: tracked repository symlinks require an explicit policy`);
      continue;
    }
    const bytes = readFileSync(resolve(repositoryRoot, path));
    if (bytes.includes(0)) {
      errors.push(`${path}: binary files are not permitted`);
      continue;
    }
    let text;
    try {
      text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    } catch {
      errors.push(`${path}: file is not valid UTF-8`);
      continue;
    }
    if (text.length > 0 && !text.endsWith("\n")) {
      errors.push(`${path}: missing final newline`);
    }
    if (text.includes("\r")) {
      errors.push(`${path}: carriage returns are not allowed`);
    }
    for (const [index, line] of text.split("\n").entries()) {
      if (/[ \t]+$/.test(line)) {
        errors.push(`${path}:${index + 1}: trailing whitespace`);
      }
    }
  }
  const goFiles = paths.filter((path) => path.endsWith(".go"));
  if (goFiles.length > 0) {
    const result = spawnSync("gofmt", ["-l", ...goFiles], {
      cwd: repositoryRoot,
      encoding: "utf8",
    });
    if (result.error?.code === "ENOENT") {
      errors.push("gofmt is required but was not found on PATH");
    } else if (result.error) {
      errors.push(
        `gofmt failed to start: ${result.error.code ?? "unknown error"}`,
      );
    } else if (result.status !== 0) {
      errors.push(`gofmt failed: ${result.stderr.trim()}`);
    } else {
      for (const path of result.stdout.trim().split("\n").filter(Boolean)) {
        errors.push(`${path}: Go source is not gofmt-formatted`);
      }
    }
  }
  return errors;
}

function relationKind(label) {
  const normalized = label.toLowerCase();
  for (const [prefix, kind] of [
    ["amended by", "amended_by"],
    ["amends", "amends"],
    ["superseded by", "superseded_by"],
    ["supersedes", "supersedes"],
  ]) {
    if (normalized.startsWith(prefix)) return kind;
  }
  return null;
}

export function decisionRelations(identifier, text) {
  const relations = Object.fromEntries(
    Object.keys(RELATION_INVERSE).map((kind) => [kind, new Set()]),
  );
  const fields = [];
  let active = null;
  for (const line of text.split(/\n##\s/)[0].split("\n")) {
    const field = line.match(/^- \*\*([^*]+):\*\*\s*(.*)$/);
    if (field) {
      active = { label: field[1], value: field[2] };
      fields.push(active);
    } else if (active && /^\s{2,}\S/.test(line)) {
      active.value += ` ${line.trim()}`;
    } else {
      active = null;
    }
  }
  for (const field of fields) {
    const kind = relationKind(field.label);
    if (!kind) continue;
    if (kind === "amends" && /\bPLAN\b/.test(field.value)) {
      relations[kind].add("PLAN");
    }
    for (const link of field.value.matchAll(/\[(ADR-\d{4})\]\([^)]+\)/g)) {
      relations[kind].add(link[1]);
    }
  }
  return { identifier, relations };
}

export function checkRelationshipReciprocity(decisions) {
  const errors = [];
  for (const [source, decision] of decisions) {
    for (const [kind, targets] of Object.entries(decision.relations)) {
      for (const target of targets) {
        const inverse = RELATION_INVERSE[kind];
        const reciprocal = decisions.get(target)?.relations[inverse];
        if (!reciprocal) {
          errors.push(`${source}: ${kind} names unknown decision ${target}`);
        } else if (!reciprocal.has(source)) {
          errors.push(`${source}: ${kind} ${target} lacks reciprocal ${inverse}`);
        }
      }
    }
  }
  return errors;
}

function governanceErrors(repositoryRoot, paths) {
  const errors = [];
  const decisions = new Map();
  decisions.set(
    "PLAN",
    decisionRelations(
      "PLAN",
      readFileSync(resolve(repositoryRoot, "docs/PLAN.md"), "utf8"),
    ),
  );
  const adrs = paths.filter(
    (path) =>
      /^docs\/adr\/\d{4}-.+\.md$/.test(path) &&
      !path.endsWith("0000-template.md"),
  );
  for (const path of adrs) {
    const identifier = `ADR-${basename(path).slice(0, 4)}`;
    const text = readFileSync(resolve(repositoryRoot, path), "utf8");
    const status = text.match(/^- \*\*Status:\*\* (\S+)$/m)?.[1];
    if (!["Proposed", "Accepted", "Superseded"].includes(status)) {
      errors.push(`${path}: invalid or missing ADR lifecycle status`);
    }
    decisions.set(identifier, decisionRelations(identifier, text));
  }
  errors.push(...checkRelationshipReciprocity(decisions));
  return { count: adrs.length, errors };
}

function syntaxErrors(repositoryRoot, paths) {
  const scripts = paths.filter((path) => path.endsWith(".mjs"));
  const errors = [];
  for (const path of scripts) {
    const result = spawnSync(process.execPath, ["--check", path], {
      cwd: repositoryRoot,
      encoding: "utf8",
    });
    if (result.status !== 0) {
      errors.push(`${path}: node --check failed: ${result.stderr.trim()}`);
    }
  }
  return { count: scripts.length, errors };
}

function eventObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function checkWorkflowContract(workflow, path = WORKFLOW_PATH) {
  const errors = [];
  const reject = (condition, message) => {
    if (condition) errors.push(`${path}: ${message}`);
  };
  const events = workflow?.on;
  const pullRequest = events?.pull_request;
  const push = events?.push;
  const jobs = workflow?.jobs;
  const job = jobs?.quality;

  reject(workflow?.name !== "CI", "workflow name must be CI");
  reject(
    !isDeepStrictEqual(Object.keys(workflow ?? {}).sort(), [
      "jobs",
      "name",
      "on",
      "permissions",
    ]),
    "workflow may contain only name, events, permissions, and jobs",
  );
  reject(
    workflow?.concurrency !== undefined,
    "workflow concurrency cancellation is not permitted",
  );
  reject(!eventObject(events), "workflow events must be an object");
  reject(
    !eventObject(events) ||
      !isDeepStrictEqual(Object.keys(events).sort(), ["pull_request", "push"]),
    "workflow must contain only pull_request and push events",
  );
  reject(
    !eventObject(pullRequest) || Object.keys(pullRequest).length !== 0,
    "pull_request must be unfiltered",
  );
  reject(
    !eventObject(push) ||
      !isDeepStrictEqual(Object.keys(push).sort(), ["branches"]) ||
      !isDeepStrictEqual(push.branches, ["main"]),
    "push must target main without path, event, or ignore filters",
  );
  reject(
    !isDeepStrictEqual(workflow?.permissions, { contents: "read" }),
    "workflow permissions must be read-only",
  );
  reject(
    !jobs || Object.keys(jobs).length !== 1 || !job,
    "workflow must contain exactly the quality job",
  );
  if (!job) return errors;
  reject(
    !isDeepStrictEqual(Object.keys(job).sort(), [
      "name",
      "runs-on",
      "steps",
      "timeout-minutes",
    ]),
    "quality job may not define env, strategy, concurrency, or unknown configuration",
  );
  reject(job.name !== "Scaffold checks (Linux)", "quality job name changed");
  reject(job["runs-on"] !== "ubuntu-24.04", "quality must use the pinned Linux runner");
  reject(
    !Number.isInteger(job["timeout-minutes"]) ||
      job["timeout-minutes"] <= 0 ||
      job["timeout-minutes"] > 15,
    "quality must have a finite timeout of at most 15 minutes",
  );
  reject(
    job["continue-on-error"] !== undefined || job.if !== undefined,
    "quality cannot be skipped or allowed to fail",
  );
  reject(!Array.isArray(job.steps) || job.steps.length !== 5, "quality must have five exact steps");
  if (!Array.isArray(job.steps)) return errors;
  for (const step of job.steps) {
    reject(
      step["continue-on-error"] !== undefined || step.if !== undefined,
      "workflow steps cannot be skipped or allowed to fail",
    );
    reject(
      step.uses && !/^[^@]+@[0-9a-f]{40}$/.test(step.uses),
      `action is not pinned to an exact commit: ${step.uses}`,
    );
  }
  const checkout = job.steps[0];
  reject(
    checkout?.uses !== CHECKOUT_REF ||
      checkout?.with?.["persist-credentials"] !== "false",
    "checkout must use the maintained pin without persisted credentials",
  );
  const setupNode = job.steps[1];
  reject(
    setupNode?.uses !== SETUP_NODE_REF ||
      setupNode?.with?.["node-version"] !== "26.7.0" ||
      setupNode?.with?.["check-latest"] !== "false" ||
      setupNode?.with?.["package-manager-cache"] !== "false",
    "Node and cache inputs must match the maintained baseline",
  );
  const setupGo = job.steps[2];
  reject(
    setupGo?.uses !== SETUP_GO_REF ||
      setupGo?.with?.["go-version"] !== "1.26.5" ||
      setupGo?.with?.["check-latest"] !== "false" ||
      setupGo?.with?.cache !== "false",
    "Go and cache inputs must match the maintained baseline",
  );
  const install = job.steps[3];
  reject(
    install?.shell !== "bash" || install?.run !== INSTALL_COMMAND,
    "quality must perform the deterministic clean install",
  );
  const check = job.steps[4];
  reject(
    check?.shell !== "bash" || check?.run !== CHECK_COMMAND,
    "quality must run the complete scaffold suite",
  );
  return errors;
}

export function checkWorkflowSet(entries) {
  const errors = [];
  for (const { path, workflow } of entries) {
    errors.push(...checkWorkflowContract(workflow, path));
  }
  if (entries.length !== 1 || entries[0]?.path !== WORKFLOW_PATH) {
    errors.push(
      `workflow set must contain exactly ${WORKFLOW_PATH}; found ${entries.map(({ path }) => path).join(", ") || "none"}`,
    );
  }
  return errors;
}

export function workflowErrors(repositoryRoot, paths) {
  const errors = [];
  const workflowPaths = paths.filter((path) => path.startsWith(".github/workflows/"));
  const entries = [];
  for (const path of workflowPaths) {
    if (!/\.ya?ml$/.test(path)) {
      errors.push(`${path}: unsupported workflow filename`);
      continue;
    }
    try {
      entries.push({
        path,
        workflow: JSON.parse(readFileSync(resolve(repositoryRoot, path), "utf8")),
      });
    } catch (error) {
      errors.push(`${path}: workflow must be JSON-compatible YAML: ${error.message}`);
    }
  }
  errors.push(...checkWorkflowSet(entries));
  return { count: workflowPaths.length, errors };
}

function readJson(repositoryRoot, path, errors) {
  try {
    return JSON.parse(readFileSync(resolve(repositoryRoot, path), "utf8"));
  } catch (error) {
    errors.push(`${path}: invalid JSON: ${error.message}`);
    return null;
  }
}

function validReleaseSegment(value) {
  return (
    typeof value === "string" &&
    value !== "." &&
    value !== ".." &&
    /^[A-Za-z0-9._-]+$/.test(value)
  );
}

function releaseAssetValid(asset) {
  let parsed;
  try {
    parsed = new URL(asset?.url);
  } catch {
    return false;
  }
  return (
    asset !== null &&
    typeof asset === "object" &&
    !Array.isArray(asset) &&
    isDeepStrictEqual(Object.keys(asset).sort(), ["sha256", "url"]) &&
    typeof asset.url === "string" &&
    parsed.origin === RELEASE_ORIGIN &&
    parsed.username === "" &&
    parsed.password === "" &&
    parsed.pathname.startsWith(RELEASE_PATH_PREFIX) &&
    parsed.search === "" &&
    parsed.hash === "" &&
    typeof asset.sha256 === "string" &&
    /^[0-9a-f]{64}$/.test(asset.sha256) &&
    asset.sha256 !== EMPTY_SHA256
  );
}

export function releaseMetadataErrors(release) {
  const errors = [];
  if (
    release === null ||
    typeof release !== "object" ||
    Array.isArray(release) ||
    release.schemaVersion !== 1 ||
    release.target !== "linux-amd64" ||
    !validReleaseSegment(release.version)
  ) {
    return ["release metadata identity is invalid"];
  }
  if (release.state === "unpublished") {
    if (
      !isDeepStrictEqual(Object.keys(release).sort(), [
        "schemaVersion",
        "state",
        "target",
        "version",
      ])
    ) {
      errors.push("unpublished release metadata must not declare assets");
    }
    return errors;
  }
  if (release.state !== "published") {
    return ["release state must be explicitly published or unpublished"];
  }
  if (
    !isDeepStrictEqual(Object.keys(release).sort(), [
      "binary",
      "notices",
      "schemaVersion",
      "state",
      "target",
      "version",
    ])
  ) {
    errors.push("published release metadata fields do not match");
  }
  if (!releaseAssetValid(release.binary)) {
    errors.push("published release binary identity or digest is invalid");
  }
  if (!releaseAssetValid(release.notices)) {
    errors.push("published release notices identity or digest is invalid");
  }
  return errors;
}

export function hostSourceErrors(path, source) {
  const errors = [];
  const isHostRuntime =
    path === "index.ts" ||
    path.startsWith("connector/") ||
    path.startsWith("generated/") ||
    path.startsWith("rpc/") ||
    path.startsWith("ui/");
  if (
    isHostRuntime &&
    source.includes("@getpaseo/client") &&
    path !== "connector/paseo.server.ts"
  ) {
    errors.push(`${path}: Paseo SDK imports belong only in the host connector`);
  }
  if (
    isHostRuntime &&
    /\b(?:eligibility|scheduler|retryPolicy|escalation|reconciliation|TaskStore|stateTransition|closurePolicy)\b/i.test(
      source,
    )
  ) {
    errors.push(`${path}: host source contains prohibited workflow policy`);
  }
  return errors;
}

function architectureErrors(repositoryRoot, paths) {
  const errors = [];
  const futureProduct = paths.filter((path) =>
    FUTURE_PRODUCT_PATHS.some((pattern) => pattern.test(path)),
  );
  if (futureProduct.length > 0) {
    errors.push(
      `product behavior is outside this scaffold and needs its PLAN section 21.1 suites: ${futureProduct.join(", ")}`,
    );
  }
  const unexpectedEngine = paths.filter(
    (path) =>
      path.startsWith("engine/") &&
      path.endsWith(".go") &&
      !ENGINE_ALLOWED_PREFIXES.some((prefix) => path.startsWith(prefix)),
  );
  if (unexpectedEngine.length > 0) {
    errors.push(`engine scaffold contains an unclassified product package: ${unexpectedEngine.join(", ")}`);
  }
  for (const path of paths.filter((candidate) => /\.[cm]?[jt]sx?$/.test(candidate))) {
    errors.push(
      ...hostSourceErrors(
        path,
        readFileSync(resolve(repositoryRoot, path), "utf8"),
      ),
    );
  }
  const entry = readFileSync(resolve(repositoryRoot, "index.ts"), "utf8");
  for (const registration of REQUIRED_UI_REGISTRATIONS) {
    if (!entry.includes(registration)) {
      errors.push(`index.ts: missing planned UI shell registration ${registration}`);
    }
  }
  if (!entry.includes("plugin.handle(connectorStartupStatus")) {
    errors.push("index.ts: missing the Zod-validated startup RPC");
  }
  return errors;
}

function scaffoldErrors(repositoryRoot, paths) {
  const errors = [];
  const packageJson = readJson(repositoryRoot, "package.json", errors);
  const manifest = readJson(repositoryRoot, "paseo-plugin.json", errors);
  const lockfile = readJson(repositoryRoot, "package-lock.json", errors);
  const release = readJson(repositoryRoot, "release/engine.json", errors);
  const readme = readFileSync(resolve(repositoryRoot, "README.md"), "utf8");
  const licenseHash = createHash("sha256")
    .update(readFileSync(resolve(repositoryRoot, "LICENSE")))
    .digest("hex");
  if (licenseHash !== LICENSE_SHA256) {
    errors.push("LICENSE must be the unmodified English Apache-2.0 text");
  }
  if (packageJson) {
    if (
      packageJson.name !== "director-for-paseo" ||
      packageJson.version !== "0.0.0" ||
      packageJson.private !== true ||
      packageJson.license !== "Apache-2.0" ||
      packageJson.dependencies?.["@getpaseo/client"] !== "0.7.2" ||
      packageJson.devDependencies?.["@getpaseo/plugin"] !== "0.7.2"
    ) {
      errors.push("package.json does not match the Director for Paseo Apache-2.0 scaffold");
    }
    for (const script of REQUIRED_SCRIPTS) {
      if (typeof packageJson.scripts?.[script] !== "string") {
        errors.push(`package.json: required deterministic script ${script} is missing`);
      }
    }
  }
  const expectedManifest = {
    id: "director",
    build: [["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"]],
  };
  if (manifest && !isDeepStrictEqual(manifest, expectedManifest)) {
    errors.push("paseo-plugin.json must use the exact stable ID and declared install argv");
  }
  if (
    lockfile &&
    (lockfile.lockfileVersion !== 3 ||
      lockfile.packages?.[""]?.license !== "Apache-2.0" ||
      lockfile.packages?.[""]?.dependencies?.["@getpaseo/client"] !== "0.7.2" ||
      lockfile.packages?.[""]?.devDependencies?.["@getpaseo/plugin"] !== "0.7.2" ||
      lockfile.packages?.["node_modules/@getpaseo/client"]?.version !== "0.7.2" ||
      lockfile.packages?.["node_modules/@getpaseo/plugin"]?.version !== "0.7.2")
  ) {
    errors.push("package-lock.json must lock the exact 0.7.2 host boundary");
  }
  if (release) {
    errors.push(
      ...releaseMetadataErrors(release).map(
        (error) => `release/engine.json: ${error}`,
      ),
    );
    if (release.state !== "unpublished") {
      errors.push(
        "release/engine.json: scaffold release must fail closed as unpublished",
      );
    }
  }
  if (
    !readme.includes("independent community plugin") ||
    !/not affiliated\s+with, endorsed by, maintained by, or sponsored by Paseo/.test(
      readme,
    )
  ) {
    errors.push("README.md must carry the ADR-0009 non-affiliation disclosure");
  }
  if (
    !readme.includes("trusted, unsandboxed daemon-host code") ||
    !readme.includes("daemon user's access during installation and update")
  ) {
    errors.push("README.md must disclose trusted daemon-host manifest execution");
  }
  const redistributed = paths.filter((path) =>
    FORBIDDEN_DISTRIBUTION_PATHS.some((pattern) => pattern.test(path)),
  );
  if (redistributed.length > 0) {
    errors.push(`external modules or executables are redistributed: ${redistributed.join(", ")}`);
  }
  errors.push(...architectureErrors(repositoryRoot, paths));
  return errors;
}

export function lintRepository(repositoryRoot) {
  const paths = repositoryFiles(repositoryRoot);
  const symlinkErrors = repositorySymlinkErrors(repositoryRoot, paths);
  if (symlinkErrors.length > 0) {
    // Symlink rejection is categorical: content readers and their other lint
    // diagnostics do not run against an ambiguous tree, every section count
    // is deliberately zero, and a clean rerun after removing the symlinks is
    // required for further diagnostics.
    return {
      errors: symlinkErrors,
      governance: { count: 0, errors: [] },
      paths,
      syntax: { count: 0, errors: [] },
      workflows: { count: 0, errors: [] },
    };
  }
  const governance = governanceErrors(repositoryRoot, paths);
  const syntax = syntaxErrors(repositoryRoot, paths);
  const workflows = workflowErrors(repositoryRoot, paths);
  const errors = [
    ...governance.errors,
    ...syntax.errors,
    ...workflows.errors,
    ...scaffoldErrors(repositoryRoot, paths),
  ];
  if (process.platform !== "linux") {
    errors.push(`scaffold CI supports Linux only, not ${process.platform}`);
  }
  return { errors, governance, paths, syntax, workflows };
}

function printErrors(label, errors) {
  console.error(`${label} failed:`);
  errors.forEach((error) => console.error(`- ${error}`));
}

export function run(repositoryRoot, mode) {
  const paths = repositoryFiles(repositoryRoot);
  if (mode === "format") {
    const errors = formatErrors(repositoryRoot, paths);
    if (errors.length > 0) {
      printErrors("Formatting checks", errors);
      return 1;
    }
    console.log(`Formatting checks passed for ${paths.length} repository files.`);
    return 0;
  }
  if (mode === "lint") {
    const result = lintRepository(repositoryRoot);
    if (result.errors.length > 0) {
      printErrors("Scaffold lint", result.errors);
      return 1;
    }
    console.log("Scaffold lint passed:");
    console.log(`- ${result.syntax.count} dependency-free Node scripts parse`);
    console.log(`- ${result.governance.count} ADRs have reciprocal lifecycle relationships`);
    console.log(`- ${result.workflows.count} Linux workflow has unfiltered pull-request/main coverage`);
    console.log("- standalone engine, full host UI shell, connector boundary, and explicit release state exist");
    console.log("- architecture package homes exist without unrelated product behavior");
    return 0;
  }
  console.error("Usage: node tools/ci/scaffold-check.mjs <format|lint>");
  return 2;
}

const modulePath = realpathSync(fileURLToPath(import.meta.url));
const invokedPath = process.argv[1]
  ? realpathSync(resolve(process.argv[1]))
  : null;
if (invokedPath === modulePath) {
  process.exitCode = run(
    fileURLToPath(new URL("../../", import.meta.url)),
    process.argv[2],
  );
}
