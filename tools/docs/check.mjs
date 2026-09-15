#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { readFileSync, readdirSync, realpathSync, statSync } from "node:fs";
import { dirname, extname, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

import {
  PLANNING_CONTRACT_SHA256,
  planningMutationInputSchema,
  planningQueryInputSchema,
} from "../../generated/planning-contract.shared.ts";

export const PUBLIC_GUIDES = [
  "README.md",
  "CONTRIBUTING.md",
  "docs/README.md",
  "docs/getting-started.md",
  "docs/user-guide.md",
  "docs/operator-guide.md",
  "docs/security-boundaries.md",
  "docs/developer-guide.md",
  "docs/installation-update.md",
  "docs/configuration.md",
  "examples/organizer/README.md",
];

const REQUIRED_PUBLIC_GUIDES = [
  "docs/getting-started.md",
  "docs/user-guide.md",
  "docs/operator-guide.md",
  "docs/security-boundaries.md",
  "docs/developer-guide.md",
];

function markdownFiles(root, directory = root) {
  const result = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.name === ".git" || entry.name === "node_modules") continue;
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) result.push(...markdownFiles(root, path));
    else if (entry.isFile() && extname(entry.name) === ".md") {
      result.push(path.slice(root.length + 1).split(sep).join("/"));
    }
  }
  return result.sort();
}

function slug(value) {
  return value
    .toLowerCase()
    .replace(/<[^>]*>/gu, "")
    .replace(/\[([^\]]+)\]\([^)]+\)/gu, "$1")
    .replace(/[`*_~]/gu, "")
    .replace(/[^\p{L}\p{N}\s-]/gu, "")
    .trim()
    .replace(/\s+/gu, "-");
}

function anchors(text) {
  const values = new Set();
  const duplicates = new Map();
  for (const line of text.split("\n")) {
    const match = /^(?:#{1,6})\s+(.+?)\s*#*$/u.exec(line);
    if (!match) continue;
    const base = slug(match[1]);
    const count = duplicates.get(base) ?? 0;
    duplicates.set(base, count + 1);
    values.add(count === 0 ? base : `${base}-${count}`);
  }
  return values;
}

export function localLinkErrors(root, path, text) {
  const errors = [];
  const pattern = /!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/gu;
  for (const match of text.matchAll(pattern)) {
    const target = match[1];
    if (/^(?:https?:|mailto:)/u.test(target)) continue;
    const [filePart, fragment] = target.split("#", 2);
    let decoded;
    try {
      decoded = decodeURIComponent(filePart || path);
    } catch {
      errors.push(`${path}: malformed local link ${target}`);
      continue;
    }
    const absolute = resolve(root, dirname(path), decoded);
    if (!absolute.startsWith(`${root}${sep}`) && absolute !== root) {
      errors.push(`${path}: local link escapes the repository: ${target}`);
      continue;
    }
    let status;
    try {
      status = statSync(absolute);
    } catch {
      errors.push(`${path}: missing local link target ${target}`);
      continue;
    }
    if (!status.isFile() && !status.isDirectory()) {
      errors.push(`${path}: unsupported local link target ${target}`);
      continue;
    }
    if (fragment && status.isFile() && extname(absolute) === ".md") {
      const available = anchors(readFileSync(absolute, "utf8"));
      if (!available.has(fragment)) errors.push(`${path}: missing heading fragment ${target}`);
    }
  }
  return errors;
}

function commandTokens(line) {
  return line.match(/"[^"]*"|'[^']*'|\S+/gu) ?? [];
}

export function consoleCommandErrors(path, block, packageJSON = { scripts: {} }) {
  const errors = [];
  const logicalLines = [];
  let pending = "";
  let pendingLine = 0;
  for (const [index, source] of block.split("\n").entries()) {
    const trimmed = source.trim();
    if (!pending) pendingLine = index + 1;
    pending += `${pending ? " " : ""}${trimmed.replace(/\\$/u, "").trim()}`;
    if (trimmed.endsWith("\\")) continue;
    logicalLines.push({ line: pending, number: pendingLine });
    pending = "";
  }
  if (pending) logicalLines.push({ line: pending, number: pendingLine });
  for (const logical of logicalLines) {
    const line = logical.line;
    if (!line || line.startsWith("#")) continue;
    if (/\b(?:restart|reboot|shutdown)\b/iu.test(line)) {
      errors.push(`${path}: console line ${logical.number} prescribes a forbidden restart or reboot`);
      continue;
    }
    if (/^[A-Z][A-Z0-9_]*=[A-Za-z0-9._-]+$/u.test(line)) continue;
    const tokens = commandTokens(line);
    const command = tokens[0];
    if (command === "paseo") {
      const action = tokens[1];
      if (action === "--version") continue;
      if (action !== "plugin" || !["add", "status", "ls", "reload", "update", "logs", "remove"].includes(tokens[2])) {
        errors.push(`${path}: console line ${logical.number} uses an unmaintained Paseo command`);
      }
      continue;
    }
    if (["node", "git"].includes(command) && tokens[1] === "--version") continue;
    if (command === "node" && /^tools\/(?:coordinator|packaging|release)\/[A-Za-z0-9._/-]+$/u.test(tokens[1] ?? "")) continue;
    if (command === "npm" && tokens[1] === "--version") continue;
    if (command === "npm" && tokens[1] === "ci" && [
      "--ignore-scripts --no-audit --no-fund",
      "--omit=dev --ignore-scripts --no-audit --no-fund",
    ].includes(tokens.slice(2).join(" "))) continue;
    if (command === "npm" && tokens[1] === "run" && typeof packageJSON.scripts?.[tokens[2]] === "string") continue;
    if (command === "go" && tokens[1] === "-C" && tokens[2] === "engine" && ["test", "run", "build", "vet"].includes(tokens[3])) continue;
    if (command === "director-engine" && ["version", "smoke"].includes(tokens[1])) continue;
    if (command === "director-engine" && tokens[1] === "serve-board" &&
      ["--listen", "--taskstore-config", "--host-id", "--host-label"].every((flag) => tokens.includes(flag))) continue;
    if (command === "gh" && tokens[1] === "pr" && tokens[2] === "merge" && tokens.includes("--match-head-commit")) continue;
    errors.push(`${path}: console line ${logical.number} uses an unmaintained command`);
  }
  return errors;
}

export function fencedBlockErrors(path, text, packageJSON) {
  const errors = [];
  const fences = text.split("\n").filter((line) => line.startsWith("```")).length;
  if (fences % 2 !== 0) errors.push(`${path}: fenced code block is not closed`);
  const pattern = /^```([^\n]*)\n([\s\S]*?)^```\s*$/gmu;
  for (const match of text.matchAll(pattern)) {
    const language = match[1].trim();
    if (!new Set(["console", "json", "text"]).has(language)) {
      errors.push(`${path}: fenced code block has unsupported language ${language || "untagged"}`);
    }
    if (language === "console") errors.push(...consoleCommandErrors(path, match[2], packageJSON));
    if (language === "text" && /^(?:paseo|npm|node|git|go|gh|director-engine)\s/mu.test(match[2].trim())) {
      errors.push(...consoleCommandErrors(path, match[2], packageJSON));
    }
    if (language === "json") {
      try {
        JSON.parse(match[2]);
      } catch {
        errors.push(`${path}: JSON code block is not valid JSON`);
      }
    }
  }
  return errors;
}

function invariantErrors(root, guides, packageJSON) {
  const errors = [];
  const combined = guides.map((path) => readFileSync(resolve(root, path), "utf8")).join("\n");
  for (const path of REQUIRED_PUBLIC_GUIDES) {
    const text = readFileSync(resolve(root, path), "utf8");
    if (!text.includes("Paseo `0.7.2`") && !text.includes("Paseo 0.7.2")) {
      errors.push(`${path}: exact supported Paseo 0.7.2 boundary is missing`);
    }
  }
  for (const phrase of [
    "handler-scoped object",
    "complete public `PaseoApi`",
    "rootless OCI",
    "no telemetry",
    "never uploaded",
  ]) {
    if (!combined.toLowerCase().includes(phrase.toLowerCase())) errors.push(`public documentation is missing ${phrase}`);
  }
  if (!combined.includes("npm ci --omit=dev --ignore-scripts --no-audit --no-fund") ||
      !combined.includes("@getpaseo/client") || !combined.includes("not vendored")) {
    errors.push("public documentation does not preserve the locked npm and no-vendoring decision");
  }
  if (!combined.includes("private XDG") || !combined.includes("never compiles or falls back to source")) {
    errors.push("public documentation does not explain private XDG state and the no-source-fallback release boundary");
  }
  if (!combined.includes("Never stop or restart the Paseo daemon or machine")) {
    errors.push("public documentation does not state the restart-free activation requirement");
  }
  if (packageJSON.dependencies?.["@getpaseo/client"] !== "0.7.2") {
    errors.push("package.json does not retain exact @getpaseo/client 0.7.2");
  }
  const requiredTopics = new Map([
    ["docs/getting-started.md", ["installation", "update", "Create Project", "Advanced import", "Preview", "Apply"]],
    ["docs/user-guide.md", ["inheritance", "Board", "List", "Task detail", "Inspector", "Validation", "Review", "delivery", "feedback", "recovery", "cleanup"]],
    ["docs/operator-guide.md", ["Doctor", "Repair", "Sync now", "health", "audit", "logs", "support bundle", "backup", "restore", "retention"]],
    ["docs/security-boundaries.md", ["handler-scoped", "credential", "rootless-OCI", "no telemetry", "P2", "human", "destructive"]],
    ["docs/developer-guide.md", ["generated", "host-interface.v1.json", "planning-surface.v1.json", "project-admin-mcp", "API", "contribution"]],
    ["examples/organizer/README.md", ["prerequisite", "priority", "labels", "acceptance criteria", "policy", "workflow"]],
  ]);
  for (const [path, topics] of requiredTopics) {
    const text = readFileSync(resolve(root, path), "utf8").toLowerCase();
    for (const topic of topics) {
      if (!text.includes(topic.toLowerCase())) errors.push(`${path}: required public topic is missing: ${topic}`);
    }
  }
  return errors;
}

function diagnosticCodes(text) {
  return new Set(text.match(/\b(?:DIRECTOR|ENGINE|DOLT)_[A-Z0-9_]*[A-Z0-9]\b/gu) ?? []);
}

function filesUnder(root, relative) {
  const absolute = resolve(root, relative);
  const result = [];
  for (const entry of readdirSync(absolute, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name === ".git") continue;
    const child = `${relative}/${entry.name}`;
    if (entry.isDirectory()) result.push(...filesUnder(root, child));
    else if (entry.isFile()) result.push(child);
  }
  return result;
}

function diagnosticErrors(root) {
  const sourceCodes = new Set();
  for (const directory of ["connector", "engine", "tools", "ui", "rpc", "generated"]) {
    for (const path of filesUnder(root, directory)) {
      if (/(?:_test\.go|\.test\.[cm]?[jt]s|\.test\.mjs)$/u.test(path)) continue;
      if (!/\.(?:go|mjs|mts|ts|tsx)$/u.test(path)) continue;
      for (const code of diagnosticCodes(readFileSync(resolve(root, path), "utf8"))) sourceCodes.add(code);
    }
  }
  const publicPaths = [
    "README.md", "SECURITY.md", "SUPPORT.md",
    ...readdirSync(resolve(root, "docs"), { withFileTypes: true })
      .filter((entry) => entry.isFile() && entry.name.endsWith(".md") && entry.name !== "PLAN.md")
      .map((entry) => `docs/${entry.name}`),
  ];
  const errors = [];
  const retired = new Set(["DIRECTOR_RUNTIME_EXTERNAL_OWNER", "ENGINE_INSTALL_NOT_PREPARED"]);
  for (const path of publicPaths) {
    for (const code of diagnosticCodes(readFileSync(resolve(root, path), "utf8"))) {
      if (retired.has(code)) errors.push(`${path}: retired diagnostic ${code}`);
      else if (!sourceCodes.has(code)) errors.push(`${path}: diagnostic ${code} is not emitted by current source`);
    }
  }
  return errors;
}

function executableSurfaceErrors() {
  const errors = [];
  for (const [command, args, expected] of [
    ["paseo", ["--version"], "0.7.2"],
    ["node", ["--version"], "v"],
    ["npm", ["--version"], ""],
    ["git", ["--version"], "git version"],
  ]) {
    const result = spawnSync(command, args, { encoding: "utf8", shell: false, timeout: 10_000, maxBuffer: 16 * 1024 });
    const output = result.stdout.trim();
    if (result.status !== 0 || (expected && !output.startsWith(expected))) errors.push(`documented executable surface is unavailable: ${command} ${args.join(" ")}`);
  }
  for (const action of ["add", "status", "ls", "reload", "update", "logs", "remove"]) {
    const result = spawnSync("paseo", ["plugin", action, "--help"], { encoding: "utf8", shell: false, timeout: 10_000, maxBuffer: 64 * 1024 });
    if (result.status !== 0) errors.push(`documented Paseo plugin action is unavailable: ${action}`);
  }
  return errors;
}

function fixtureErrors(root) {
  const errors = [];
  const query = JSON.parse(readFileSync(resolve(root, "examples/api/planning-query.json"), "utf8"));
  const mutation = JSON.parse(readFileSync(resolve(root, "examples/api/task-create.json"), "utf8"));
  if (!planningQueryInputSchema.safeParse(query).success) errors.push("planning query example does not match the generated schema");
  if (!planningMutationInputSchema.safeParse(mutation).success) errors.push("Task-create example does not match the generated schema");
  if (mutation.contractHash !== PLANNING_CONTRACT_SHA256) errors.push("Task-create example has a stale planning contract hash");
  const organizer = readFileSync(resolve(root, "examples/organizer/paseo-director.json"), "utf8");
  for (const forbidden of [realpathSync(root), "/home/agents/", "ghp_", "BEGIN PRIVATE KEY", "Bearer "]) {
    if (organizer.includes(forbidden)) errors.push(`public Organizer contains private or credential-shaped content: ${forbidden}`);
  }
  const manifest = JSON.parse(readFileSync(resolve(root, "paseo-plugin.json"), "utf8"));
  const preparation = JSON.stringify(manifest.build);
  if (preparation !== JSON.stringify([
    ["npm", "ci", "--omit=dev", "--ignore-scripts", "--no-audit", "--no-fund"],
    ["node", "tools/packaging/verify-install.mjs"],
  ])) errors.push("Paseo manifest preparation drifted from the documented locked commands");
  return errors;
}

export function checkRepository(root) {
  const packageJSON = JSON.parse(readFileSync(resolve(root, "package.json"), "utf8"));
  const errors = [];
  for (const path of markdownFiles(root)) {
    errors.push(...localLinkErrors(root, path, readFileSync(resolve(root, path), "utf8")));
  }
  const topLevelContracts = readdirSync(resolve(root, "docs"), { withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.endsWith(".md") && entry.name !== "PLAN.md")
    .map((entry) => `docs/${entry.name}`);
  for (const path of [...new Set([...PUBLIC_GUIDES, ...topLevelContracts])].sort()) {
    const text = readFileSync(resolve(root, path), "utf8");
    errors.push(...fencedBlockErrors(path, text, packageJSON));
  }
  for (const path of PUBLIC_GUIDES) {
    const text = readFileSync(resolve(root, path), "utf8");
    for (const forbidden of [realpathSync(root), "/home/agents/"]) {
      if (text.includes(forbidden)) errors.push(`${path}: contains a private execution path`);
    }
  }
  errors.push(...invariantErrors(root, PUBLIC_GUIDES, packageJSON));
  errors.push(...fixtureErrors(root));
  errors.push(...diagnosticErrors(root));
  errors.push(...executableSurfaceErrors());
  return errors;
}

export function run(root) {
  const errors = checkRepository(root);
  if (errors.length > 0) {
    console.error("Public documentation checks failed:");
    for (const error of errors) console.error(`- ${error}`);
    return 1;
  }
  console.log("Public documentation links, commands, examples, contracts, security disclosures, and redaction boundaries passed.");
  return 0;
}

const modulePath = realpathSync(fileURLToPath(import.meta.url));
const invokedPath = process.argv[1] ? realpathSync(resolve(process.argv[1])) : null;
if (modulePath === invokedPath) {
  process.exitCode = run(realpathSync(fileURLToPath(new URL("../../", import.meta.url))));
}
