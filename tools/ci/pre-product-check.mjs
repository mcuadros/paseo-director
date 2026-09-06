#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { basename, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const WORKFLOW_PATH = ".github/workflows/ci.yml";
const CHECKOUT_REF =
  "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1";
const SETUP_NODE_REF =
  "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020";
const CHECK_COMMAND = [
  "node tools/ci/pre-product-check.test.mjs",
  "node tools/ci/pre-product-check.mjs",
  "node tools/spikes/dir-m0.10/effect-contract.mjs",
  "node tools/spikes/dir-m0.10/guard-mutations.mjs",
  "node tools/spikes/dir-m0.6/worktree-ownership.mjs --state-fsync-fixture",
  "node tools/spikes/dir-m0.7/github-delivery.mjs --self-test",
  "node docs/evidence/m0.14/practical-boundary-contract.mjs",
  "node tools/spikes/dir-m0.17/ignored-tree-policy.mjs --timeout-probe",
].join("\n");
const PRODUCT_MARKERS = [
  /^package\.json$/,
  /^tsconfig(?:\.[^/]+)?\.json$/,
  /^(?:src|test|tests|packages)\//,
];
const RELATION_INVERSE = {
  amends: "amended_by",
  amended_by: "amends",
  supersedes: "superseded_by",
  superseded_by: "supersedes",
};

function trackedFiles(repositoryRoot) {
  const result = spawnSync("git", ["ls-files", "-z"], {
    cwd: repositoryRoot,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`git ls-files failed: ${result.stderr.trim()}`);
  }
  return result.stdout.split("\0").filter(Boolean).sort();
}

export function findPreProductMarkers(paths) {
  return paths.filter((path) => PRODUCT_MARKERS.some((pattern) => pattern.test(path)));
}

function relationKind(label) {
  const normalized = label.toLowerCase();
  for (const [prefix, kind] of [
    ["amended by", "amended_by"],
    ["amends", "amends"],
    ["superseded by", "superseded_by"],
    ["supersedes", "supersedes"],
  ]) {
    if (normalized.startsWith(prefix)) {
      return kind;
    }
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
    if (!kind) {
      continue;
    }
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

export function checkWorkflowContract(workflow) {
  const errors = [];
  const reject = (condition, message) => {
    if (condition) {
      errors.push(message);
    }
  };
  const events = workflow?.on;
  const jobs = workflow?.jobs;
  const job = jobs?.pre_product;
  reject(workflow?.name !== "CI", "workflow name must be CI");
  reject(!events || !("pull_request" in events), "workflow must run for pull requests");
  reject(!events?.push?.branches?.includes("main"), "workflow must run for updates to main");
  reject(workflow?.permissions?.contents !== "read", "workflow permissions must be read-only");
  reject(!jobs || Object.keys(jobs).length !== 1 || !job, "workflow must contain exactly the pre_product job");
  if (!job) {
    return errors;
  }
  reject(job["runs-on"] !== "ubuntu-24.04", "pre_product must use the pinned Linux runner");
  reject(
    !Number.isInteger(job["timeout-minutes"]) ||
      job["timeout-minutes"] <= 0 ||
      job["timeout-minutes"] > 10,
    "pre_product must have a finite timeout of at most 10 minutes",
  );
  reject(
    job["continue-on-error"] !== undefined || job.if !== undefined,
    "pre_product cannot be skipped or allowed to fail",
  );
  if (!Array.isArray(job.steps)) {
    errors.push("pre_product steps are missing");
    return errors;
  }
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
  const uses = new Set(job.steps.map((step) => step.uses).filter(Boolean));
  reject(
    !uses.has(CHECKOUT_REF) || !uses.has(SETUP_NODE_REF) || uses.size !== 2,
    "workflow action pins do not match the maintained baseline",
  );
  const checkout = job.steps.find((step) => step.uses === CHECKOUT_REF);
  reject(
    checkout?.with?.["persist-credentials"] !== "false",
    "checkout credentials must not persist",
  );
  const setupNode = job.steps.find((step) => step.uses === SETUP_NODE_REF);
  reject(
    setupNode?.with?.["node-version"] !== "26.7.0" ||
      setupNode?.with?.["check-latest"] !== "false" ||
      setupNode?.with?.["package-manager-cache"] !== "false",
    "Node and cache inputs must match the maintained baseline",
  );
  const command = job.steps.find((step) => step.run !== undefined);
  reject(
    command?.shell !== "bash" || command?.run !== CHECK_COMMAND,
    "workflow must run the complete maintained pre-product command",
  );
  return errors;
}

function textErrors(repositoryRoot, paths) {
  const errors = [];
  for (const path of paths) {
    const bytes = readFileSync(resolve(repositoryRoot, path));
    if (bytes.includes(0)) {
      errors.push(`${path}: binary files require an explicit CI policy`);
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
  return errors;
}

function adrErrors(repositoryRoot, paths) {
  const errors = [];
  const decisions = new Map();
  const plan = readFileSync(resolve(repositoryRoot, "docs/PLAN.md"), "utf8");
  decisions.set("PLAN", decisionRelations("PLAN", plan));
  const adrs = paths.filter(
    (path) => /^docs\/adr\/\d{4}-.+\.md$/.test(path) &&
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

function run(repositoryRoot) {
  const paths = trackedFiles(repositoryRoot);
  const errors = textErrors(repositoryRoot, paths);
  const governance = adrErrors(repositoryRoot, paths);
  const syntax = syntaxErrors(repositoryRoot, paths);
  errors.push(...governance.errors, ...syntax.errors);

  try {
    const workflow = JSON.parse(
      readFileSync(resolve(repositoryRoot, WORKFLOW_PATH), "utf8"),
    );
    errors.push(...checkWorkflowContract(workflow));
  } catch (error) {
    errors.push(`${WORKFLOW_PATH}: invalid JSON-compatible YAML: ${error.message}`);
  }

  const markers = findPreProductMarkers(paths);
  if (markers.length > 0) {
    errors.push(`product markers require replacing the pre-product CI guard: ${markers.join(", ")}`);
  }
  if (process.platform !== "linux") {
    errors.push(`pre-product CI supports Linux only, not ${process.platform}`);
  }

  if (errors.length > 0) {
    console.error("Pre-product CI checks failed:");
    errors.forEach((error) => console.error(`- ${error}`));
    return 1;
  }
  console.log("Pre-product CI checks passed:");
  console.log(`- ${paths.length} tracked UTF-8 text files are formatted`);
  console.log(`- ${governance.count} ADRs have valid reciprocal lifecycle relationships`);
  console.log(`- ${syntax.count} dependency-free Node scripts parse`);
  console.log("- workflow syntax, immutable pins, Linux runner, and fail-closed policy match");
  console.log("- no product marker exists, so product-only PLAN section 21.1 checks are deferred");
  return 0;
}

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const invokedPath = process.argv[1] ? pathToFileURL(resolve(process.argv[1])).href : null;
if (invokedPath === import.meta.url) {
  process.exitCode = run(repositoryRoot);
}
