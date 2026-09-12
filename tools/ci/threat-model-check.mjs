#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { createHash } from "node:crypto";
import { existsSync, lstatSync, readFileSync, realpathSync } from "node:fs";
import { dirname, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const manifestPath = "docs/threat-model-hardening.json";

const requiredThreats = new Set([
  "TCB-1", "CTL-1", "CTL-2", "AGT-1", "AGT-2", "AGT-3", "AGT-4", "AGT-5",
  "REP-1", "FS-1", "FS-2", "FS-3", "CMD-1", "CMD-2", "SEC-1", "SEC-2",
  "SUP-1", "MCP-1", "MCP-2", "MCP-3", "DEL-1", "DAT-1", "NET-1", "DOS-1",
]);
const requiredCategories = new Set([
  "credentials", "secrets", "private_paths", "process_arguments_environment",
  "mcp_capability_scope", "policy_envelope", "actor_authority_separation",
  "response_loss_restart", "exact_repository_resource_ownership", "owned_deletion",
]);
const requiredInventorySources = new Set([
  "plan", "adrs", "beads_task_and_comments", "prior_review_findings", "public_contract_schemas",
  "connectors", "coordinator", "engine_ports_and_adapters", "operations_and_support",
  "maintenance_backup_and_retention", "tests",
]);

function safeRelativePath(path) {
  return typeof path === "string" && path.length > 0 && !path.startsWith("/") &&
    !path.includes("\\") && !path.split("/").includes("..");
}

function anchorParts(anchor) {
  if (typeof anchor !== "string") return null;
  const separator = anchor.indexOf("#");
  if (separator < 1 || separator === anchor.length - 1) return null;
  const path = anchor.slice(0, separator);
  const symbol = anchor.slice(separator + 1);
  return safeRelativePath(path) && symbol.trim() === symbol ? { path, symbol } : null;
}

export function validateThreatModel(manifest, readSource = (path) => readFileSync(resolve(repositoryRoot, path), "utf8")) {
  const errors = [];
  if (manifest?.schemaVersion !== 1 || manifest?.task !== "dir-m5.9" || manifest?.status !== "closed") {
    errors.push("manifest identity or status is not the closed dir-m5.9 schema");
  }
  if (!Array.isArray(manifest?.normativeSources) || manifest.normativeSources.length === 0) {
    errors.push("normative sources are absent");
  }
  const inventorySources = new Set(manifest?.inventorySources ?? []);
  for (const source of requiredInventorySources) {
    if (!inventorySources.has(source)) errors.push(`${source}: inventory source is not recorded`);
  }
  for (const source of inventorySources) {
    if (!requiredInventorySources.has(source)) errors.push(`${source}: unknown inventory source`);
  }
  for (const source of manifest?.normativeSources ?? []) {
    if (!safeRelativePath(source)) errors.push(`unsafe normative source: ${String(source)}`);
    else {
      try { readSource(source); } catch { errors.push(`missing normative source: ${source}`); }
    }
  }
  const ids = new Set();
  const threats = new Map();
  const categories = new Set();
  for (const control of manifest?.controls ?? []) {
    if (typeof control.id !== "string" || ids.has(control.id)) errors.push(`duplicate or invalid control: ${String(control.id)}`);
    ids.add(control.id);
    if (!new Set(["P0", "P1"]).has(control.severity) || control.status !== "closed") {
      errors.push(`${control.id}: mitigation is not a closed P0/P1 item`);
    }
    if (typeof control.mitigation !== "string" || control.mitigation.length < 32) errors.push(`${control.id}: mitigation is not substantive`);
    if (!Array.isArray(control.categories) || control.categories.length === 0) errors.push(`${control.id}: categories are absent`);
    for (const category of control.categories ?? []) {
      if (!requiredCategories.has(category)) errors.push(`${control.id}: unknown category ${category}`);
      categories.add(category);
    }
    for (const threat of control.threats ?? []) {
      if (!requiredThreats.has(threat)) errors.push(`${control.id}: unknown threat ${threat}`);
      threats.set(threat, (threats.get(threat) ?? 0) + 1);
    }
    for (const field of ["implementation", "tests"]) {
      const anchors = control[field];
      if (!Array.isArray(anchors) || anchors.length === 0) {
        errors.push(`${control.id}: ${field} anchors are absent`);
        continue;
      }
      for (const anchor of anchors) {
        const parts = anchorParts(anchor);
        if (!parts) {
          errors.push(`${control.id}: invalid ${field} anchor ${String(anchor)}`);
          continue;
        }
        try {
          const source = readSource(parts.path);
          if (!source.includes(parts.symbol)) errors.push(`${control.id}: stale ${field} anchor ${anchor}`);
          if (field === "tests" && !/(?:_test\.go|\.test\.(?:mjs|ts))$/u.test(parts.path)) errors.push(`${control.id}: non-test evidence anchor ${anchor}`);
        } catch {
          errors.push(`${control.id}: missing ${field} anchor ${anchor}`);
        }
      }
    }
  }
  for (const threat of requiredThreats) {
    if (threats.get(threat) !== 1) errors.push(`${threat}: expected exactly one closed mapping, found ${threats.get(threat) ?? 0}`);
  }
  for (const category of requiredCategories) {
    if (!categories.has(category)) errors.push(`${category}: required category is not mapped`);
  }
  return errors;
}

export function verifyRepositoryBoundary(root = repositoryRoot) {
  const errors = [];
  const manifestAbsolute = resolve(root, manifestPath);
  if (!existsSync(manifestAbsolute) || !lstatSync(manifestAbsolute).isFile()) return ["threat-model manifest is absent"];
  const rootReal = realpathSync(root);
  const readSource = (path) => {
    const absolute = resolve(root, path);
    const relativePath = relative(rootReal, realpathSync(absolute));
    if (relativePath === ".." || relativePath.startsWith(`..${sep}`) || lstatSync(absolute).isSymbolicLink()) {
      throw new Error("anchor escapes repository");
    }
    return readFileSync(absolute, "utf8");
  };
  let manifest;
  try { manifest = JSON.parse(readSource(manifestPath)); } catch { return ["threat-model manifest is not strict JSON"] ; }
  errors.push(...validateThreatModel(manifest, readSource));
  return errors;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const errors = verifyRepositoryBoundary();
  if (errors.length > 0) {
    for (const error of errors) process.stderr.write(`${error}\n`);
    process.exitCode = 1;
  } else {
    const bytes = readFileSync(resolve(repositoryRoot, manifestPath));
    process.stdout.write(`threat-model controls verified: sha256=${createHash("sha256").update(bytes).digest("hex")}\n`);
  }
}
