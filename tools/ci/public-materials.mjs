#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const LICENSE_SHA256 = "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30";
const PUBLIC_PATHS = [
  "CHANGELOG.md",
  "CONTRIBUTING.md",
  "LICENSE",
  "README.md",
  "SECURITY.md",
  "SUPPORT.md",
  "docs/compatibility.md",
  "docs/installation-update.md",
  "docs/licensing.md",
  "docs/release-process.md",
];

function includesAll(text, values) {
  const compact = text.replace(/\s+/gu, " ");
  return values.filter((value) => !compact.includes(value.replace(/\s+/gu, " ")));
}

function positiveRestartGuidance(text) {
  const pattern = /\b(?:restart|reboot)\s+(?:the\s+)?(?:Paseo\s+daemon|daemon|machine|host)\b/giu;
  for (const match of text.matchAll(pattern)) {
    const prefix = text.slice(Math.max(0, match.index - 180), match.index);
    const sentence = prefix.slice(Math.max(prefix.lastIndexOf("."), prefix.lastIndexOf("\n\n")) + 1);
    if (!/\b(?:do not|does not|must not|never|neither|no|not a supported|without)\b/iu.test(sentence)) return true;
  }
  return false;
}

function unsafePublicContent(text) {
  return [
    /\/(?:home|Users)\/[A-Za-z0-9._-]+\//u,
    /\/(?:tmp|var\/tmp|run\/user\/[0-9]+)\/[A-Za-z0-9._-]+/u,
    /[A-Za-z]:\\Users\\[A-Za-z0-9._-]+\\/u,
    /\bpaseo:[0-9a-f]{8}-[0-9a-f-]{27,}\b/iu,
    /\bwks_[A-Za-z0-9]+\b/u,
    /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/u,
    /\b(?:ghp_|github_pat_|AKIA)[A-Za-z0-9_]{12,}\b/u,
    /\bsk-[A-Za-z0-9_-]{20,}\b/u,
    /\bAuthorization:\s*Bearer\s+\S+/iu,
    /\b(?:password|secret|token)\s*[:=]\s*[A-Za-z0-9_./+-]{8,}/iu,
  ].some((pattern) => pattern.test(text));
}

function localLinkErrors(repositoryRoot, path, text) {
  const errors = [];
  for (const match of text.matchAll(/\[[^\]]+\]\(([^)]+)\)/gu)) {
    const target = match[1].split("#", 1)[0];
    if (!target || /^[a-z][a-z0-9+.-]*:/iu.test(target)) continue;
    const resolved = resolve(repositoryRoot, dirname(path), decodeURIComponent(target));
    if (!existsSync(resolved)) errors.push(`${path}: local link target is missing: ${target}`);
  }
  return errors;
}

export function publicMaterialErrors(repositoryRoot, overrides = {}) {
  const content = new Map();
  const errors = [];
  for (const path of PUBLIC_PATHS) {
    try {
      content.set(path, overrides[path] ?? readFileSync(resolve(repositoryRoot, path), "utf8"));
    } catch {
      errors.push(`${path}: required public material is missing`);
    }
  }
  if (errors.length > 0) return errors;

  const license = content.get("LICENSE");
  if (createHash("sha256").update(license).digest("hex") !== LICENSE_SHA256) {
    errors.push("LICENSE: must remain the unmodified English Apache-2.0 text");
  }
  let packageJSON;
  let lock;
  let releaseMetadata;
  let releaseSchema;
  let bootstrapMetadata;
  let bootstrapSchema;
  let doltMetadata;
  try {
    const json = (path) => JSON.parse(overrides[path] ?? readFileSync(resolve(repositoryRoot, path), "utf8"));
    packageJSON = json("package.json");
    lock = json("package-lock.json");
    releaseMetadata = json("release/engine.json");
    releaseSchema = json("release/engine.schema.json");
    bootstrapMetadata = json("release/bootstrap-linux-amd64.json");
    bootstrapSchema = json("release/bootstrap-linux-amd64.schema.json");
    doltMetadata = json("release/dolt-linux-amd64.json");
  } catch {
    errors.push("package or release metadata is unreadable");
  }
  if (packageJSON?.license !== "Apache-2.0" || lock?.packages?.[""]?.license !== "Apache-2.0") {
    errors.push("package and lockfile SPDX identity must be Apache-2.0");
  }
  const lockedEntries = Object.keys(lock?.packages ?? {}).filter(Boolean);
  if (lockedEntries.length !== 580 ||
      lockedEntries.filter((path) => lock.packages[path].dev !== true).length !== 7 ||
      lock?.packages?.["node_modules/@getpaseo/client"]?.version !== "0.7.2" ||
      lock?.packages?.["node_modules/@getpaseo/client"]?.dev === true ||
      lock?.packages?.["node_modules/@getpaseo/cli"]?.version !== "0.7.2" ||
      lock?.packages?.["node_modules/@getpaseo/cli"]?.dev !== true) {
    errors.push("package-lock.json must retain the exact 580-entry, seven-production, CI-only-Paseo-CLI closure");
  }
  const publishedSchema = releaseSchema?.oneOf?.find((entry) => entry?.properties?.state?.const === "published");
  const publishedBootstrapSchema = bootstrapSchema?.oneOf?.find((entry) => entry?.properties?.state?.const === "published");
  if (releaseMetadata?.schemaVersion !== 2 || releaseMetadata?.state !== "unpublished" ||
      releaseMetadata?.version !== "0.0.0-scaffold" || releaseMetadata?.target !== "linux-amd64" ||
      !publishedSchema?.required?.includes("dolt") ||
      !publishedSchema?.properties?.binary || !publishedSchema?.properties?.notices ||
      !releaseSchema?.$defs?.binary?.required?.includes("size") ||
      !releaseSchema?.$defs?.notices?.required?.includes("size") ||
      !releaseSchema?.$defs?.dolt?.required?.includes("executableSize") ||
      !releaseSchema?.$defs?.dolt?.properties?.archive?.required?.includes("size")) {
    errors.push("release metadata must remain unpublished schema 2 with Engine/notices/Dolt size and digest fields");
  }
  if (bootstrapMetadata?.schemaVersion !== 1 || bootstrapMetadata?.state !== "unpublished" ||
      bootstrapMetadata?.version !== "0.0.0-scaffold" || bootstrapMetadata?.target !== "linux-amd64" ||
      !publishedBootstrapSchema?.required?.includes("binary") ||
      !publishedBootstrapSchema?.properties?.binary) {
    errors.push("bootstrap metadata must remain unpublished schema 1 with a published binary closure");
  }
  if (JSON.stringify(doltMetadata) !== JSON.stringify({
    schemaVersion: 1,
    version: "2.3.2",
    target: "linux-amd64",
    archive: {
      name: "dolt-linux-amd64.tar.gz",
      url: "https://github.com/dolthub/dolt/releases/download/v2.3.2/dolt-linux-amd64.tar.gz",
      sha256: "7a2949fa2b2b3799ee1e57e6d64519a8d65d675fd832f6469d4e07e5a1c72b14",
      size: 43842207,
    },
    executableSha256: "eb50b0e7c4ce2303486deaf850f9dc5630ffcfd8d9aa4a40a2b3e0b23e2ac2f0",
    executableSize: 126224392,
  })) {
    errors.push("canonical Dolt 2.3.2 metadata origin, sizes, or digests changed");
  }
  if (packageJSON?.scripts?.["test:activation:real"] !== undefined ||
      packageJSON?.scripts?.ci?.includes("test:activation:real")) {
    errors.push("package scripts must not restore the retired activation-real gate");
  }

  const readme = content.get("README.md");
  for (const missing of includesAll(readme, [
    "docs/licensing.md", "SECURITY.md", "SUPPORT.md", "docs/compatibility.md",
    "CHANGELOG.md", "docs/release-process.md", "independent community plugin",
  ])) errors.push(`README.md: missing public policy link or disclosure ${missing}`);

  const security = content.get("SECURITY.md");
  for (const missing of includesAll(security, [
    "https://github.com/mcuadros/paseo-director/security/advisories/new",
    "Do not open a public issue",
    "Do not include credentials",
    "three business days",
    "seven calendar days",
    "coordinated disclosure",
    "No public beta or stable version has been released",
    "docs/compatibility.md",
    "SUPPORT.md",
  ])) errors.push(`SECURITY.md: missing private-reporting or response contract ${missing}`);

  const support = content.get("SUPPORT.md");
  for (const missing of includesAll(support, [
    "exact stable Paseo", "`0.7.2`", "Paseo `0.8`", "Node.js 22", "glibc",
    "npm ci --omit=dev --ignore-scripts --no-audit --no-fund",
    "`@getpaseo/client@0.7.2`", "Default `main`", "Tagged alpha, beta, and stable",
    "handler-scoped object", "canonical precompiled Dolt `2.3.2`", "Reload never compiles",
    "`0.147.0` provider tuple", "`2.1.258` provider tuple", "`1.18.18` provider tuple",
    "paseo plugin ls --json", "paseo plugin logs director --json", "docs/operations-diagnostics.md",
    "https://github.com/mcuadros/paseo-director/issues", "SECURITY.md",
    "live and plugin-scoped", "never restarts the daemon or host",
  ])) errors.push(`SUPPORT.md: missing support or compatibility contract ${missing}`);

  const compatibility = content.get("docs/compatibility.md");
  for (const missing of includesAll(compatibility, [
    "Linux amd64 with glibc", "Exact stable `0.7.2`", "`0.8` preview",
    "complete 580-entry npm lock graph", "546 exact coordinates",
    "seven shipped production coordinates", "539 CI/build-only coordinates",
    "`@getpaseo/cli@0.7.2`", "handler-scoped object",
    "github.com/go-sql-driver/mysql", "MPL-2.0", "Go `1.26.5`", "release bootstrap",
    "Default `main`", "Tagged alpha, beta, and stable", "Plugin reload never compiles",
    "canonical precompiled Dolt `2.3.2`", "live and plugin-scoped",
  ])) errors.push(`docs/compatibility.md: missing exact compatibility contract ${missing}`);

  const licensing = content.get("docs/licensing.md");
  for (const missing of includesAll(licensing, [
    "Apache License, Version 2.0", "third_party/license-policy.json",
    "tools/release/notices.mjs", "@getpaseo/client@0.7.2", "MPL-2.0",
    "580-entry npm lock", "CI/build-only", "--notices", "canonical Dolt `2.3.2`",
    "Tagged alpha, beta, and stable", "Plugin reload never", "not affiliated",
    "third_party/licenses/", "empty `GOMODCACHE`", "`GOPROXY=off`",
  ])) errors.push(`docs/licensing.md: missing license or notices contract ${missing}`);

  const changelog = content.get("CHANGELOG.md");
  for (const missing of includesAll(changelog, [
    "## [Unreleased]", "No tagged alpha, public beta, stable Director release",
    "Semantic Versioning", "restart-free and plugin-scoped", "Default `main`",
    "Tagged alpha/beta/stable", "exact bootstrap and Engine",
  ])) errors.push(`CHANGELOG.md: missing unreleased or compatibility status ${missing}`);
  if (/^## \[(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\]/mu.test(changelog)) {
    errors.push("CHANGELOG.md: must not claim a released version before channel publication");
  }

  const release = content.get("docs/release-process.md");
  for (const missing of includesAll(release, [
    "exact Candidate SHA", "one authoritative complete remote Linux CI",
    "independent top-level Reviewer", "THIRD_PARTY_NOTICES.txt",
    "current real backup and fresh restore", "20 complete real workflows",
    "14 consecutive beta days", "25 Workspaces", "10,000 historical Tasks",
    "500 open Tasks", "eight concurrent agents", "--match-head-commit",
    "M6.6 owner manual hold", "Separate explicit stable promotion",
    "1.0.0-alpha.1", "ADR-0025", "reload never compiles",
    "schema version 2", "Dolt", "3168ea518f4fb400551fca8221d6477466f8a2cd",
    "must not tell users to stop the Paseo daemon",
  ])) errors.push(`docs/release-process.md: missing non-bypassable release gate ${missing}`);

  const installation = content.get("docs/installation-update.md");
  for (const missing of includesAll(installation, [
    "DIRECTOR_INSTALL_LICENSE_AUDIT", "DIRECTOR_INSTALL_LIBC_UNSUPPORTED", "Default-main preparation",
    "Tagged installed plugins never", "reload never compiles",
    "canonical precompiled Dolt", "handler-scoped", "live and plugin-scoped",
    "DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER", "DIRECTOR_BOOTSTRAP_FOREIGN_OWNER",
    "DIRECTOR_BOOTSTRAP_INSTALL_NOT_PREPARED",
    "3168ea518f4fb400551fca8221d6477466f8a2cd",
  ])) errors.push(`docs/installation-update.md: missing release-support integration ${missing}`);
  for (const retired of ["DIRECTOR_RUNTIME_EXTERNAL_OWNER", "ENGINE_INSTALL_NOT_PREPARED"]) {
    if (installation.includes(retired)) errors.push(`docs/installation-update.md: contains retired diagnostic ${retired}`);
  }

  for (const path of ["README.md", "SUPPORT.md", "docs/compatibility.md", "docs/installation-update.md", "docs/release-process.md"]) {
    const text = content.get(path);
    if (/requires? a full daemon-operator credential/iu.test(text)) {
      errors.push(`${path}: contains the superseded secondary Paseo authority model`);
    }
  }

  for (const [path, text] of content) {
    if (unsafePublicContent(text)) errors.push(`${path}: contains a private path, credential, or internal runtime identity`);
    if (positiveRestartGuidance(text)) errors.push(`${path}: prescribes a daemon or machine restart`);
    if (text.includes("test:activation:real")) errors.push(`${path}: references the retired activation-real gate`);
    errors.push(...localLinkErrors(repositoryRoot, path, text));
  }
  return errors;
}

export function publicMaterialSummary(repositoryRoot) {
  const hash = createHash("sha256");
  for (const path of PUBLIC_PATHS) {
    hash.update(path).update("\0").update(readFileSync(resolve(repositoryRoot, path))).update("\0");
  }
  return { schemaVersion: 1, publicFiles: PUBLIC_PATHS.length, sha256: hash.digest("hex") };
}

const modulePath = fileURLToPath(import.meta.url);
if (process.argv[1] && resolve(process.argv[1]) === resolve(modulePath)) {
  const repositoryRoot = resolve(dirname(modulePath), "../..");
  const errors = publicMaterialErrors(repositoryRoot);
  if (errors.length > 0) {
    for (const error of errors) process.stderr.write(`public materials: ${error}\n`);
    process.exitCode = 1;
  } else {
    process.stdout.write(`${JSON.stringify(publicMaterialSummary(repositoryRoot))}\n`);
  }
}
