#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { createHash } from "node:crypto";
import { lstatSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";

export const INSTALL_COMMAND = [
  "npm",
  "ci",
  "--omit=dev",
  "--ignore-scripts",
  "--no-audit",
  "--no-fund",
];
export const VERIFY_COMMAND = [
  "node",
  "tools/packaging/verify-install.mjs",
];

const REGISTRY_ORIGIN = "https://registry.npmjs.org";
const INTEGRITY_PATTERN = /^sha512-[A-Za-z0-9+/]+={0,2}$/u;
const VERSION_PATTERN = /^[0-9A-Za-z][0-9A-Za-z.+_-]{0,127}$/u;
const ALLOWED_INSTALL_SCRIPTS = new Map([
  ["node_modules/esbuild", "0.25.12"],
  ["node_modules/fsevents", "2.3.3"],
]);

function readJSON(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    throw new Error(`${label} is not valid JSON`);
  }
}

function registryURL(value) {
  if (typeof value !== "string") return false;
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    return false;
  }
  return (
    parsed.origin === REGISTRY_ORIGIN &&
    parsed.username === "" &&
    parsed.password === "" &&
    parsed.search === "" &&
    parsed.hash === "" &&
    parsed.pathname.endsWith(".tgz")
  );
}

function dependencyEntry(packages, packagePath, name) {
  let search = packagePath;
  for (;;) {
    const nested = search ? `${search}/node_modules/${name}` : `node_modules/${name}`;
    if (packages[nested]) return nested;
    const marker = search.lastIndexOf("/node_modules/");
    if (marker < 0) break;
    search = search.slice(0, marker);
  }
  return packages[`node_modules/${name}`] ? `node_modules/${name}` : null;
}

function dependencyNames(value) {
  return [...new Set([
    ...Object.keys(value.dependencies ?? {}),
    ...Object.keys(value.devDependencies ?? {}),
    ...Object.keys(value.optionalDependencies ?? {}),
    ...Object.keys(value.peerDependencies ?? {}),
  ])];
}

function packageDirectory(repositoryRoot, packagePath) {
  const path = resolve(repositoryRoot, packagePath);
  let status;
  try {
    status = lstatSync(path);
  } catch {
    throw new Error(`${packagePath} is not installed`);
  }
  if (!status.isDirectory() || status.isSymbolicLink()) {
    throw new Error(`${packagePath} is not a real installed package directory`);
  }
  return path;
}

export function auditDependencyClosure(repositoryRoot, options = {}) {
  const packageJSON = readJSON(join(repositoryRoot, "package.json"), "package.json");
  const lock = readJSON(join(repositoryRoot, "package-lock.json"), "package-lock.json");
  const manifest = readJSON(join(repositoryRoot, "paseo-plugin.json"), "paseo-plugin.json");
  const errors = [];
  if (lock.lockfileVersion !== 3 || lock.requires !== true || !lock.packages?.[""]) {
    errors.push("package-lock.json must be a complete lockfileVersion 3 closure");
  }
  const root = lock.packages?.[""] ?? {};
  if (lock.name !== packageJSON.name || lock.version !== packageJSON.version || root.name !== packageJSON.name || root.version !== packageJSON.version) {
    errors.push("package and lockfile root identity disagree");
  }
  for (const field of ["dependencies", "devDependencies", "engines", "bin"]) {
    if (!isDeepStrictEqual(packageJSON[field] ?? {}, root[field] ?? {})) {
      errors.push(`package.json and package-lock.json disagree on ${field}`);
    }
  }
  for (const lifecycle of ["preinstall", "install", "postinstall", "prepare"]) {
    if (packageJSON.scripts?.[lifecycle] !== undefined) {
      errors.push(`package.json lifecycle script ${lifecycle} is not permitted`);
    }
  }
  if (
    manifest.id !== "director" ||
    !isDeepStrictEqual(manifest.build, [INSTALL_COMMAND, VERIFY_COMMAND]) ||
    !isDeepStrictEqual(Object.keys(manifest).sort(), ["build", "id"])
  ) {
    errors.push("paseo-plugin.json must declare the exact locked install and verification argv");
  }

  const packages = lock.packages ?? {};
  const inventory = [];
  const observedInstallScripts = new Map();
  let productionCount = 0;
  for (const [path, value] of Object.entries(packages).sort(([left], [right]) => left.localeCompare(right))) {
    if (path === "") continue;
    if (!path.startsWith("node_modules/") || value.link === true || value.inBundle === true) {
      errors.push(`${path || "<root>"} is not a registry package`);
      continue;
    }
    if (!VERSION_PATTERN.test(value.version ?? "")) {
      errors.push(`${path} has an invalid locked version`);
    }
    const integrityBytes = typeof value.integrity === "string"
      ? Buffer.from(value.integrity.slice("sha512-".length), "base64")
      : Buffer.alloc(0);
    if (!registryURL(value.resolved) || !INTEGRITY_PATTERN.test(value.integrity ?? "") || integrityBytes.length !== 64) {
      errors.push(`${path} lacks an exact npm registry origin or SHA-512 integrity`);
    }
    if (value.hasInstallScript === true) {
      observedInstallScripts.set(path, value.version);
    }
    if (value.dev !== true) productionCount += 1;
    for (const field of ["dependencies", "optionalDependencies"]) {
      for (const name of Object.keys(value[field] ?? {})) {
        if (!dependencyEntry(packages, path, name)) {
          errors.push(`${path} declares missing locked ${field} entry ${name}`);
        }
      }
    }
    inventory.push([
      path,
      value.version ?? "",
      value.resolved ?? "",
      value.integrity ?? "",
      value.dev === true,
      value.optional === true,
      value.hasInstallScript === true,
    ]);
  }
  if (!isDeepStrictEqual([...observedInstallScripts], [...ALLOWED_INSTALL_SCRIPTS])) {
    errors.push("the audited package lifecycle-script set changed");
  }

  const reachable = new Set();
  const queue = dependencyNames(root)
    .map((name) => dependencyEntry(packages, "", name))
    .filter(Boolean);
  while (queue.length > 0) {
    const path = queue.shift();
    if (reachable.has(path)) continue;
    reachable.add(path);
    for (const name of dependencyNames(packages[path] ?? {})) {
      const dependency = dependencyEntry(packages, path, name);
      if (dependency && !reachable.has(dependency)) queue.push(dependency);
    }
  }
  const unreachable = inventory.map(([path]) => path).filter((path) => !reachable.has(path));
  if (unreachable.length > 0) {
    errors.push(`package-lock.json contains ${unreachable.length} unreachable package entries`);
  }

  if (options.installed === true) {
    for (const [path, value] of Object.entries(packages)) {
      if (!path || value.dev === true || value.optional === true) continue;
      try {
        const directory = packageDirectory(repositoryRoot, path);
        const installed = readJSON(join(directory, "package.json"), `${path}/package.json`);
        if (installed.version !== value.version) {
          errors.push(`${path} installed version does not match the lockfile`);
        }
      } catch (error) {
        errors.push(error instanceof Error ? error.message : `${path} is unavailable`);
      }
    }
  }

  const sha256 = createHash("sha256")
    .update(JSON.stringify(inventory))
    .digest("hex");
  return {
    schemaVersion: 1,
    packageCount: inventory.length,
    productionPackageCount: productionCount,
    lifecycleScriptsExecuted: 0,
    auditedInstallScriptPackages: [...observedInstallScripts.keys()],
    sha256,
    errors,
  };
}

export function run(repositoryRoot, options = {}) {
  const result = auditDependencyClosure(repositoryRoot, options);
  if (result.errors.length > 0) {
    for (const error of result.errors) process.stderr.write(`dependency audit: ${error}\n`);
    return 1;
  }
  process.stdout.write(`${JSON.stringify(result)}\n`);
  return 0;
}

const modulePath = fileURLToPath(import.meta.url);
if (process.argv[1] && resolve(process.argv[1]) === resolve(modulePath)) {
  const repositoryRoot = resolve(dirname(modulePath), "../..");
  process.exitCode = run(repositoryRoot, {
    installed: process.argv.includes("--installed"),
  });
}
