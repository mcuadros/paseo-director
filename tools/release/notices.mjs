#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  closeSync,
  constants,
  existsSync,
  fsyncSync,
  lstatSync,
  openSync,
  readFileSync,
  readdirSync,
  realpathSync,
  writeFileSync,
} from "node:fs";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";

import { auditDependencyClosure } from "../ci/dependency-audit.mjs";

const SHA256_PATTERN = /^[0-9a-f]{64}$/u;
const SHA1_PATTERN = /^h1:[A-Za-z0-9+/]+={0,2}$/u;
const VERSION_PATTERN = /^[0-9A-Za-z][0-9A-Za-z.+_-]{0,127}$/u;
const NOTICE_FILE_PATTERN = /^(?:licen[cs]e|copying|notice|copyright|patents)(?:\..*)?$/iu;
const MAXIMUM_LICENSE_BYTES = 1024 * 1024;
const MAXIMUM_NOTICES_BYTES = 16 * 1024 * 1024;
const POLICY_PATH = "third_party/license-policy.json";
const PASEO_SOURCE_PATTERN = /^https:\/\/github\.com\/getpaseo\/paseo\/(?:tree|blob)\/[0-9a-f]{40}\//u;
const NPM_SHIPPED_SPDX = ["Apache-2.0", "MIT", "Unlicense"];
const NPM_BUILD_ONLY_SPDX = [
  "(AFL-2.1 OR BSD-3-Clause)", "(MIT OR CC0-1.0)", "0BSD",
  "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "BlueOak-1.0.0",
  "CC-BY-4.0", "ISC", "MIT", "Unlicense",
];
const GO_APPROVED_SPDX = new Set(["BSD-3-Clause", "MPL-2.0"]);

export class NoticesError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "NoticesError";
    this.code = code;
  }
}

function fail(code, message) {
  throw new NoticesError(code, message);
}

function sha256(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function strictKeys(value, expected) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  return isDeepStrictEqual(Object.keys(value).sort(), [...expected].sort());
}

function readJSON(path, code) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    fail(code, "license input is not valid JSON");
  }
}

function publicHTTPS(value) {
  if (typeof value !== "string") return false;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.username === "" && url.password === "" && url.search === "" && url.hash === "";
  } catch {
    return false;
  }
}

function registryTarball(value) {
  if (!publicHTTPS(value)) return false;
  const url = new URL(value);
  return url.origin === "https://registry.npmjs.org" && url.pathname.endsWith(".tgz");
}

function coordinate(name, version) {
  return `${name}\u0000${version}`;
}

function compareText(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

function packageName(packagePath) {
  const marker = packagePath.lastIndexOf("node_modules/");
  return marker < 0 ? "" : packagePath.slice(marker + "node_modules/".length);
}

function normalizeText(bytes) {
  let text;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    fail("NOTICES_LICENSE_TEXT", "a dependency notice is not valid UTF-8");
  }
  if (text.includes("\0")) fail("NOTICES_LICENSE_TEXT", "a dependency notice contains a NUL byte");
  return `${text.replace(/\r\n?/gu, "\n").trimEnd()}\n`;
}

function validatePolicy(policy) {
  if (!strictKeys(policy, ["go", "npm", "schemaVersion"]) || policy.schemaVersion !== 1) {
    fail("NOTICES_POLICY", "license policy identity is invalid");
  }
  if (!strictKeys(policy.npm, [
    "buildOnlyAllowedSpdx", "buildOnlyLicenseReferences",
    "missingMetadataOverrides", "missingShippedNoticeOverrides",
    "platformExcludedLicenseReferences", "shippedAllowedSpdx",
  ]) || !Array.isArray(policy.npm.shippedAllowedSpdx) ||
      !Array.isArray(policy.npm.buildOnlyAllowedSpdx) ||
      !Array.isArray(policy.npm.missingMetadataOverrides) ||
      !Array.isArray(policy.npm.missingShippedNoticeOverrides) ||
      !Array.isArray(policy.npm.buildOnlyLicenseReferences) ||
      !Array.isArray(policy.npm.platformExcludedLicenseReferences)) {
    fail("NOTICES_POLICY", "npm license policy is invalid");
  }
  if (!isDeepStrictEqual(policy.npm.shippedAllowedSpdx, NPM_SHIPPED_SPDX) ||
      !isDeepStrictEqual(policy.npm.buildOnlyAllowedSpdx, NPM_BUILD_ONLY_SPDX)) {
    fail("NOTICES_LICENSE_INCOMPATIBLE", "npm SPDX policy contains an unknown, stale, or incompatible expression");
  }
  const allSpdx = new Set([...NPM_SHIPPED_SPDX, ...NPM_BUILD_ONLY_SPDX]);
  for (const value of policy.npm.missingMetadataOverrides) {
    if (!strictKeys(value, ["attribution", "licenseSha256", "licenseSource", "name", "source", "spdx", "version"]) ||
        typeof value.name !== "string" || !VERSION_PATTERN.test(value.version ?? "") ||
        !allSpdx.has(value.spdx) || typeof value.attribution !== "string" || value.attribution.length === 0 ||
        !publicHTTPS(value.source) || !publicHTTPS(value.licenseSource) ||
        !PASEO_SOURCE_PATTERN.test(value.source) || !PASEO_SOURCE_PATTERN.test(value.licenseSource) ||
        !SHA256_PATTERN.test(value.licenseSha256 ?? "")) {
      fail("NOTICES_POLICY", "npm metadata override is invalid");
    }
  }
  for (const value of policy.npm.missingShippedNoticeOverrides) {
    if (!strictKeys(value, ["attribution", "name", "version"]) || typeof value.name !== "string" ||
        !VERSION_PATTERN.test(value.version ?? "") || typeof value.attribution !== "string" || value.attribution.length === 0) {
      fail("NOTICES_POLICY", "npm notice override is invalid");
    }
  }
  for (const value of policy.npm.buildOnlyLicenseReferences) {
    if (!strictKeys(value, ["attribution", "declaredLicense", "licenseFile", "licenseSha256", "name", "version"]) ||
        typeof value.name !== "string" || !VERSION_PATTERN.test(value.version ?? "") ||
        !/^SEE LICENSE IN [A-Za-z0-9._-]+$/u.test(value.declaredLicense ?? "") ||
        typeof value.attribution !== "string" || value.attribution.length === 0 ||
        !NOTICE_FILE_PATTERN.test(value.licenseFile ?? "") || !SHA256_PATTERN.test(value.licenseSha256 ?? "")) {
      fail("NOTICES_POLICY", "build-only license reference is invalid");
    }
  }
  for (const value of policy.npm.platformExcludedLicenseReferences) {
    if (!strictKeys(value, ["declaredLicense", "name", "version"]) ||
        typeof value.name !== "string" || !VERSION_PATTERN.test(value.version ?? "") ||
        !/^SEE LICENSE IN [A-Za-z0-9._-]+$/u.test(value.declaredLicense ?? "")) {
      fail("NOTICES_POLICY", "platform-excluded license reference is invalid");
    }
  }
  if (!strictKeys(policy.go, ["modules", "toolchain"]) || !Array.isArray(policy.go.modules) ||
      !strictKeys(policy.go.toolchain, ["attribution", "licenseFiles", "source", "spdx", "version"])) {
    fail("NOTICES_POLICY", "Go license policy is invalid");
  }
  for (const value of [policy.go.toolchain, ...policy.go.modules]) {
    const toolchain = value === policy.go.toolchain;
    const expected = toolchain
      ? ["attribution", "licenseFiles", "source", "spdx", "version"]
      : ["attribution", "licenseFiles", "name", "source", "spdx", "version"];
    if (!strictKeys(value, expected) || typeof value.version !== "string" || typeof value.spdx !== "string" ||
        typeof value.attribution !== "string" || value.attribution.length === 0 || !publicHTTPS(value.source) ||
        !Array.isArray(value.licenseFiles) || value.licenseFiles.length === 0) {
      fail("NOTICES_POLICY", "Go dependency policy is invalid");
    }
    if (!GO_APPROVED_SPDX.has(value.spdx)) {
      fail("NOTICES_LICENSE_INCOMPATIBLE", "Go dependency policy contains an unknown or incompatible SPDX identity");
    }
    const names = new Set();
    const paths = new Set();
    for (const file of value.licenseFiles) {
      const fileKeys = toolchain ? ["name", "sha256"] : ["name", "path", "sha256"];
      const repositoryPathValid = toolchain || (
        typeof file.path === "string" &&
        /^third_party\/licenses\/[A-Za-z0-9._/-]+$/u.test(file.path) &&
        !file.path.includes("//") && !file.path.split("/").includes("..") &&
        file.path.endsWith(`/${file.name}`) && !paths.has(file.path)
      );
      if (!strictKeys(file, fileKeys) || !NOTICE_FILE_PATTERN.test(file.name ?? "") ||
          !SHA256_PATTERN.test(file.sha256 ?? "") || names.has(file.name) || !repositoryPathValid) {
        fail("NOTICES_POLICY", "Go license-file policy is invalid");
      }
      names.add(file.name);
      if (!toolchain) paths.add(file.path);
    }
  }
  return policy;
}

export function loadLicensePolicy(repositoryRoot) {
  return validatePolicy(readJSON(join(repositoryRoot, POLICY_PATH), "NOTICES_POLICY"));
}

function uniquePolicyMap(values, label) {
  const result = new Map();
  for (const value of values) {
    const key = coordinate(value.name, value.version);
    if (result.has(key)) fail("NOTICES_POLICY", `${label} contains a duplicate entry`);
    result.set(key, value);
  }
  return result;
}

function sameSet(left, right) {
  return left.size === right.size && [...left].every((value) => right.has(value));
}

function licenseFiles(directory, appliesTo, texts) {
  const files = readdirSync(directory)
    .filter((name) => NOTICE_FILE_PATTERN.test(name))
    .sort(compareText);
  const result = [];
  for (const name of files) {
    const path = join(directory, name);
    const status = lstatSync(path);
    if (!status.isFile() || status.isSymbolicLink() || status.size > MAXIMUM_LICENSE_BYTES) {
      fail("NOTICES_LICENSE_TEXT", "a dependency notice is unsafe or oversized");
    }
    const bytes = readFileSync(path);
    const digest = sha256(bytes);
    if (texts) {
      const existing = texts.get(digest);
      if (existing && !existing.bytes.equals(bytes)) fail("NOTICES_LICENSE_TEXT", "a dependency notice digest conflicted");
      if (existing) {
        existing.appliesTo.add(appliesTo);
      } else {
        texts.set(digest, { appliesTo: new Set([appliesTo]), bytes, names: new Set([name]) });
      }
      texts.get(digest).names.add(name);
    }
    result.push({ name, sha256: digest });
  }
  return result;
}

function registerLicenseText(bytes, name, appliesTo, texts) {
  const digest = sha256(bytes);
  const existing = texts.get(digest);
  if (existing && !existing.bytes.equals(bytes)) fail("NOTICES_LICENSE_TEXT", "a dependency notice digest conflicted");
  if (existing) {
    existing.appliesTo.add(appliesTo);
    existing.names.add(name);
  } else {
    texts.set(digest, { appliesTo: new Set([appliesTo]), bytes, names: new Set([name]) });
  }
  return digest;
}

function installedDirectory(repositoryRoot, packagePath) {
  const path = resolve(repositoryRoot, packagePath);
  let status;
  try {
    status = lstatSync(path);
  } catch {
    fail("NOTICES_NPM_INSTALL", "a required locked npm package is not installed");
  }
  if (!status.isDirectory() || status.isSymbolicLink()) {
    fail("NOTICES_NPM_INSTALL", "an installed npm package is not a real directory");
  }
  return path;
}

function installedRequired(value, installedScope) {
  if (installedScope === "none") return false;
  if (installedScope === "production") return value.dev !== true && value.optional !== true;
  return !platformExcluded(value);
}

function platformExcluded(value) {
  if (value.optional !== true) return false;
  const libc = process.report.getReport().header.glibcVersionRuntime ? "glibc" : "musl";
  return (Array.isArray(value.os) && !value.os.includes(process.platform)) ||
    (Array.isArray(value.cpu) && !value.cpu.includes(process.arch)) ||
    (Array.isArray(value.libc) && !value.libc.includes(libc));
}

export function npmLicenseInventory(repositoryRoot, options = {}) {
  const installedScope = options.installedScope ?? "all";
  if (!["all", "production", "none"].includes(installedScope)) {
    fail("NOTICES_ARGUMENT", "installed npm scope is invalid");
  }
  const policy = validatePolicy(options.policy ?? loadLicensePolicy(repositoryRoot));
  const packageJSON = options.packageJSON ?? readJSON(join(repositoryRoot, "package.json"), "NOTICES_NPM_LOCK");
  const lock = options.lock ?? readJSON(join(repositoryRoot, "package-lock.json"), "NOTICES_NPM_LOCK");
  const dependencyAudit = auditDependencyClosure(repositoryRoot, { installed: installedScope === "production" });
  if (dependencyAudit.errors.length > 0) fail("NOTICES_NPM_LOCK", "the locked npm dependency closure is invalid");
  if (packageJSON.license !== "Apache-2.0" || lock.packages?.[""]?.license !== "Apache-2.0") {
    fail("NOTICES_ROOT_LICENSE", "root npm SPDX metadata must be Apache-2.0");
  }

  const shippedAllowed = new Set(policy.npm.shippedAllowedSpdx);
  const buildAllowed = new Set(policy.npm.buildOnlyAllowedSpdx);
  const metadataOverrides = uniquePolicyMap(policy.npm.missingMetadataOverrides, "npm metadata policy");
  const noticeOverrides = uniquePolicyMap(policy.npm.missingShippedNoticeOverrides, "npm shipped-notice policy");
  const buildReferences = uniquePolicyMap(policy.npm.buildOnlyLicenseReferences, "npm build-only license-reference policy");
  const platformReferences = uniquePolicyMap(policy.npm.platformExcludedLicenseReferences, "npm platform-excluded license-reference policy");
  const requiredMetadataOverrides = new Set();
  const observedShippedSpdx = new Set();
  const observedBuildSpdx = new Set();
  const observedBuildReferences = new Set();
  const observedPlatformReferences = new Set();
  const missingShippedNotices = new Set();
  const texts = new Map();
  const packages = new Map();
  const verifiedCoordinates = new Set();
  let lockEntryCount = 0;
  let platformExcludedEntryCount = 0;

  for (const [packagePath, value] of Object.entries(lock.packages ?? {}).sort(([left], [right]) => compareText(left, right))) {
    if (packagePath === "") continue;
    lockEntryCount += 1;
    const name = packageName(packagePath);
    if (!name || !VERSION_PATTERN.test(value.version ?? "") || !registryTarball(value.resolved) ||
        typeof value.integrity !== "string" || !value.integrity.startsWith("sha512-") ||
        Buffer.from(value.integrity.slice("sha512-".length), "base64").length !== 64) {
      fail("NOTICES_NPM_LOCK", "a locked npm package lacks exact registry identity");
    }
    const metadataKey = coordinate(name, value.version);
    const metadataOverride = metadataOverrides.get(metadataKey);
    const shipped = value.dev !== true;
    const excluded = platformExcluded(value);
    if (excluded) platformExcludedEntryCount += 1;
    if (shipped && excluded) fail("NOTICES_NPM_LOCK", "a shipped npm package is excluded from the supported platform");

    let spdx = null;
    let declaredLicense = value.license ?? null;
    let licenseStatus;
    let reference = null;
    if (typeof declaredLicense !== "string" || declaredLicense.length === 0) {
      requiredMetadataOverrides.add(metadataKey);
      if (!metadataOverride) fail("NOTICES_LICENSE_MISSING", "a locked npm package has no reviewed license identity");
      spdx = metadataOverride.spdx;
      licenseStatus = "reviewed-missing-metadata";
    } else if (metadataOverride) {
      fail("NOTICES_POLICY_STALE", "an npm metadata override is stale");
    } else if (declaredLicense.startsWith("SEE LICENSE IN ")) {
      if (shipped) fail("NOTICES_LICENSE_INCOMPATIBLE", "a shipped npm package has no SPDX license identity");
      reference = excluded ? platformReferences.get(metadataKey) : buildReferences.get(metadataKey);
      if (!reference || reference.declaredLicense !== declaredLicense) {
        fail("NOTICES_LICENSE_MISSING", "a build-only npm license reference is unreviewed");
      }
      (excluded ? observedPlatformReferences : observedBuildReferences).add(metadataKey);
      licenseStatus = excluded ? "platform-excluded-license-reference" : "verified-build-only-license-reference";
    } else {
      spdx = declaredLicense;
      const allowed = shipped ? shippedAllowed : buildAllowed;
      if (!allowed.has(spdx)) fail("NOTICES_LICENSE_INCOMPATIBLE", "a locked npm package has an unapproved SPDX expression");
      (shipped ? observedShippedSpdx : observedBuildSpdx).add(spdx);
      licenseStatus = "verified-spdx";
    }
    if (metadataOverride) {
      const allowed = shipped ? shippedAllowed : buildAllowed;
      if (!allowed.has(spdx)) fail("NOTICES_LICENSE_INCOMPATIBLE", "a reviewed npm metadata override is incompatible with its scope");
      (shipped ? observedShippedSpdx : observedBuildSpdx).add(spdx);
    }

    const packageKey = JSON.stringify([name, value.version, value.resolved, value.integrity]);
    const role = shipped ? "shipped-plugin" : "ci-build-only";
    const current = packages.get(packageKey) ?? {
      ecosystem: "npm",
      name,
      version: value.version,
      scope: new Set(),
      shipped,
      optional: value.optional === true,
      platformExcluded: excluded,
      spdx,
      declaredLicense,
      licenseStatus,
      source: value.resolved,
      integrity: value.integrity,
      packagePaths: [],
      attribution: metadataOverride?.attribution ?? reference?.attribution ?? null,
      licenseEvidence: metadataOverride ? {
        source: metadataOverride.licenseSource,
        sha256: metadataOverride.licenseSha256,
      } : reference && !excluded ? {
        file: reference.licenseFile,
        sha256: reference.licenseSha256,
      } : null,
      licenseFiles: null,
    };
    if (current.spdx !== spdx || current.declaredLicense !== declaredLicense || current.licenseStatus !== licenseStatus ||
        current.shipped !== shipped || current.optional !== (value.optional === true) || current.platformExcluded !== excluded) {
      fail("NOTICES_DUPLICATE", "duplicate npm package coordinates disagree");
    }
    current.scope.add(role);
    current.packagePaths.push(packagePath);

    if (installedRequired(value, installedScope)) {
      const directory = installedDirectory(repositoryRoot, packagePath);
      const installed = readJSON(join(directory, "package.json"), "NOTICES_NPM_INSTALL");
      const installedLicense = typeof installed.license === "string" ? installed.license : installed.license?.type;
      const expectedInstalledLicense = metadataOverride ? undefined : declaredLicense;
      if (installed.name !== name || installed.version !== value.version || installedLicense !== expectedInstalledLicense) {
        fail("NOTICES_NPM_INSTALL", "installed npm metadata does not match the lock and license policy");
      }
      const observedFiles = licenseFiles(directory, `${name}@${value.version}`, shipped ? texts : null);
      if (reference && !excluded) {
        if (!observedFiles.some((file) => file.name === reference.licenseFile && file.sha256 === reference.licenseSha256)) {
          fail("NOTICES_POLICY_STALE", "a reviewed build-only license reference changed");
        }
      }
      let files = shipped ? observedFiles : [];
      if (shipped && files.length === 0) {
        missingShippedNotices.add(metadataKey);
        const noticeOverride = noticeOverrides.get(metadataKey);
        if (!metadataOverride || !noticeOverride) {
          fail("NOTICES_LICENSE_MISSING", "a shipped npm package lacks reviewed notice attribution");
        }
        const rootLicense = readFileSync(join(repositoryRoot, "LICENSE"));
        const digest = registerLicenseText(rootLicense, "LICENSE", `${name}@${value.version}`, texts);
        files = [{ name: "LICENSE", sha256: digest, source: "director-root-apache-2.0-copy" }];
      }
      if (current.licenseFiles && !isDeepStrictEqual(current.licenseFiles, files)) {
        fail("NOTICES_DUPLICATE", "duplicate npm package coordinates have different notices");
      }
      current.licenseFiles = files;
      verifiedCoordinates.add(packageKey);
    }
    packages.set(packageKey, current);
  }

  if (!sameSet(requiredMetadataOverrides, new Set(metadataOverrides.keys()))) {
    fail("NOTICES_POLICY_STALE", "npm metadata overrides do not exactly match the locked closure");
  }
  if (!sameSet(observedShippedSpdx, shippedAllowed) || !sameSet(observedBuildSpdx, buildAllowed)) {
    fail("NOTICES_POLICY_STALE", "npm SPDX policy does not exactly match the shipped and build-only closures");
  }
  if (!sameSet(observedBuildReferences, new Set(buildReferences.keys())) ||
      !sameSet(observedPlatformReferences, new Set(platformReferences.keys()))) {
    fail("NOTICES_POLICY_STALE", "npm non-SPDX build-only license policy does not exactly match the locked closure");
  }
  if (installedScope === "all" && !sameSet(missingShippedNotices, new Set(noticeOverrides.keys()))) {
    fail("NOTICES_LICENSE_MISSING", "shipped npm packages without archive notices do not exactly match reviewed attribution policy");
  }

  const inventory = [...packages.values()].map((value) => {
    const noticeOverride = noticeOverrides.get(coordinate(value.name, value.version));
    return {
      ecosystem: value.ecosystem,
      name: value.name,
      version: value.version,
      scope: [...value.scope].sort(),
      shipped: value.shipped,
      optional: value.optional,
      platformExcluded: value.platformExcluded,
      spdx: value.spdx,
      declaredLicense: value.declaredLicense,
      licenseStatus: value.licenseStatus,
      source: value.source,
      integrity: value.integrity,
      packagePaths: value.packagePaths.sort(),
      attribution: value.attribution ?? noticeOverride?.attribution ?? null,
      licenseEvidence: value.licenseEvidence,
      licenseFiles: value.licenseFiles ?? [],
    };
  }).sort(inventoryOrder);

  return { inventory, lockEntryCount, platformExcludedEntryCount, texts, verifiedCoordinateCount: verifiedCoordinates.size };
}

function run(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? process.env,
    shell: false,
    timeout: options.timeout ?? 30_000,
    maxBuffer: 4 * 1024 * 1024,
  });
  if (result.status !== 0 || (result.error && result.status === null)) {
    fail(options.code ?? "NOTICES_COMMAND", "a license inventory command failed");
  }
  return result.stdout.trim();
}

function goEnvironment() {
  return {
    ...(process.env.PATH ? { PATH: process.env.PATH } : {}),
    ...(process.env.HOME ? { HOME: process.env.HOME } : {}),
    ...(process.env.GOMODCACHE ? { GOMODCACHE: process.env.GOMODCACHE } : {}),
    GOTOOLCHAIN: "local",
    GOWORK: "off",
    GOPROXY: "off",
    GOSUMDB: "off",
  };
}

function registerPolicyLicenseFiles(directory, policyFiles, appliesTo, texts, repositoryRoot = null) {
  const observed = [];
  for (const expected of policyFiles) {
    const path = expected.path === undefined
      ? join(directory, expected.name)
      : resolve(repositoryRoot, expected.path);
    let status;
    try {
      status = lstatSync(path);
    } catch {
      fail("NOTICES_GO_LICENSE", "a reviewed Go license file is missing");
    }
    const repositoryPrefix = repositoryRoot === null
      ? null
      : `${resolve(repositoryRoot, "third_party/licenses")}/`;
    if (!status.isFile() || status.isSymbolicLink() || status.size > MAXIMUM_LICENSE_BYTES ||
        (repositoryPrefix !== null && (!path.startsWith(repositoryPrefix) || realpathSync(path) !== path))) {
      fail("NOTICES_GO_LICENSE", "a reviewed Go license file is unsafe");
    }
    const bytes = readFileSync(path);
    if (sha256(bytes) !== expected.sha256) fail("NOTICES_POLICY_STALE", "a reviewed Go license file changed");
    const existing = texts.get(expected.sha256);
    if (existing && !existing.bytes.equals(bytes)) fail("NOTICES_GO_LICENSE", "a Go license digest conflicted");
    if (existing) {
      existing.appliesTo.add(appliesTo);
      existing.names.add(expected.name);
    } else {
      texts.set(expected.sha256, { appliesTo: new Set([appliesTo]), bytes, names: new Set([expected.name]) });
    }
    observed.push({ name: expected.name, sha256: expected.sha256 });
  }
  return observed;
}

export function lockedGoModuleInventory(modMetadata, goSum, policyValue) {
  const policy = validatePolicy(policyValue);
  if (!strictKeys(modMetadata, ["Exclude", "Go", "Ignore", "Module", "Replace", "Require", "Retract", "Tool"]) ||
      !strictKeys(modMetadata.Module, ["Path"]) ||
      modMetadata.Module.Path !== "github.com/mcuadros/director-engine" ||
      !Array.isArray(modMetadata.Require) ||
      modMetadata.Exclude !== null || modMetadata.Replace !== null || modMetadata.Retract !== null ||
      modMetadata.Tool !== null || modMetadata.Ignore !== null) {
    fail("NOTICES_GO_MODULES", "engine/go.mod contains unsupported or replaced module metadata");
  }
  if (modMetadata.Go !== policy.go.toolchain.version.slice(2)) {
    fail("NOTICES_GO_TOOLCHAIN", "engine/go.mod does not declare the reviewed Go toolchain");
  }

  const modulePolicy = uniquePolicyMap(policy.go.modules, "Go module policy");
  const required = new Map();
  for (const value of modMetadata.Require) {
    const expectedKeys = value?.Indirect === true ? ["Indirect", "Path", "Version"] : ["Path", "Version"];
    if (!strictKeys(value, expectedKeys) || typeof value.Path !== "string" ||
        !VERSION_PATTERN.test(value.Version ?? "")) {
      fail("NOTICES_GO_MODULES", "engine/go.mod contains incomplete module requirements");
    }
    const key = coordinate(value.Path, value.Version);
    if (required.has(key)) fail("NOTICES_DUPLICATE", "engine/go.mod contains a duplicate module requirement");
    if (!modulePolicy.has(key)) fail("NOTICES_LICENSE_INCOMPATIBLE", "a required Go module has no reviewed license policy");
    required.set(key, { name: value.Path, version: value.Version });
  }
  if (!sameSet(new Set(required.keys()), new Set(modulePolicy.keys()))) {
    fail("NOTICES_POLICY_STALE", "Go module policy does not exactly match engine/go.mod");
  }

  const sums = new Map();
  const lines = String(goSum).split("\n").filter(Boolean);
  for (const line of lines) {
    const fields = line.split(" ");
    if (fields.length !== 3 || fields.some((field) => field.length === 0) || !SHA1_PATTERN.test(fields[2])) {
      fail("NOTICES_GO_MODULES", "engine/go.sum contains malformed module checksums");
    }
    const goMod = fields[1].endsWith("/go.mod");
    const version = goMod ? fields[1].slice(0, -"/go.mod".length) : fields[1];
    const key = coordinate(fields[0], version);
    if (!required.has(key)) fail("NOTICES_GO_MODULES", "engine/go.sum contains an unlocked or stale module checksum");
    const current = sums.get(key) ?? {};
    const field = goMod ? "goModSum" : "sum";
    if (current[field] !== undefined) fail("NOTICES_DUPLICATE", "engine/go.sum contains a duplicate module checksum");
    current[field] = fields[2];
    sums.set(key, current);
  }
  if (!sameSet(new Set(sums.keys()), new Set(required.keys())) ||
      [...sums.values()].some((value) => !SHA1_PATTERN.test(value.sum ?? "") || !SHA1_PATTERN.test(value.goModSum ?? ""))) {
    fail("NOTICES_GO_MODULES", "engine/go.sum does not contain the complete locked module closure");
  }
  return [...required].map(([key, value]) => ({ ...value, ...sums.get(key) }))
    .sort((left, right) => compareText(coordinate(left.name, left.version), coordinate(right.name, right.version)));
}

export function goLicenseInventory(repositoryRoot, options = {}) {
  const policy = validatePolicy(options.policy ?? loadLicensePolicy(repositoryRoot));
  const engineRoot = join(repositoryRoot, "engine");
  const environment = goEnvironment();
  const version = run("go", ["version"], { env: environment, code: "NOTICES_GO_TOOLCHAIN", timeout: 10_000 });
  if (version !== `go version ${policy.go.toolchain.version} linux/amd64`) {
    fail("NOTICES_GO_TOOLCHAIN", "the exact reviewed linux-amd64 Go toolchain is required");
  }
  const goModMetadataText = run("go", ["mod", "edit", "-json"], {
    cwd: engineRoot,
    env: environment,
    code: "NOTICES_GO_MODULES",
  });
  let goModMetadata;
  try {
    goModMetadata = JSON.parse(goModMetadataText);
  } catch {
    fail("NOTICES_GO_MODULES", "engine/go.mod metadata is not valid JSON");
  }
  const moduleRows = lockedGoModuleInventory(
    goModMetadata,
    readFileSync(join(engineRoot, "go.sum"), "utf8"),
    policy,
  );
  const goRoot = realpathSync(run("go", ["env", "GOROOT"], { env: environment, code: "NOTICES_GO_TOOLCHAIN", timeout: 10_000 }));
  const texts = new Map();
  const toolchain = policy.go.toolchain;
  const inventory = [{
    ecosystem: "go-toolchain",
    name: "go-standard-library",
    version: toolchain.version,
    scope: ["release-bootstrap-binary", "release-engine-binary"],
    optional: false,
    spdx: toolchain.spdx,
    source: toolchain.source,
    integrity: null,
    packagePaths: [],
    attribution: toolchain.attribution,
    licenseEvidence: null,
    licenseFiles: registerPolicyLicenseFiles(goRoot, toolchain.licenseFiles, `go-standard-library@${toolchain.version}`, texts),
  }];

  const modulePolicy = uniquePolicyMap(policy.go.modules, "Go module policy");
  for (const { name, version: moduleVersion, sum, goModSum } of moduleRows) {
    const key = coordinate(name, moduleVersion);
    const entry = modulePolicy.get(key);
    inventory.push({
      ecosystem: "go-module",
      name,
      version: moduleVersion,
      scope: ["release-engine-binary"],
      optional: false,
      spdx: entry.spdx,
      source: entry.source,
      integrity: sum,
      goModIntegrity: goModSum,
      packagePaths: [],
      attribution: entry.attribution,
      licenseEvidence: null,
      licenseFiles: registerPolicyLicenseFiles(null, entry.licenseFiles, `${name}@${moduleVersion}`, texts, repositoryRoot),
    });
  }
  return { inventory: inventory.sort(inventoryOrder), texts };
}

export function verifyReleaseBinaryModules(repositoryRoot, binaryPath, options = {}) {
  const status = lstatSync(binaryPath);
  if (!status.isFile() || status.isSymbolicLink()) fail("NOTICES_BINARY", "release binary is not a regular file");
  const kind = options.kind ?? "engine";
  if (!["bootstrap", "engine"].includes(kind)) fail("NOTICES_ARGUMENT", "release binary kind is invalid");
  const expectedInventory = goLicenseInventory(resolve(repositoryRoot)).inventory;
  const toolchain = expectedInventory.find((entry) => entry.ecosystem === "go-toolchain");
  const expected = new Map(expectedInventory
    .filter((entry) => entry.ecosystem === "go-module" && kind === "engine")
    .map((entry) => [coordinate(entry.name, entry.version), entry.integrity]));
  const output = run("go", ["version", "-m", binaryPath], { code: "NOTICES_BINARY", timeout: 10_000 });
  const lines = output.split("\n");
  if (!lines[0]?.endsWith(`: ${toolchain.version}`)) fail("NOTICES_BINARY", "release binary Go toolchain identity changed");
  const observed = new Map();
  for (const line of lines) {
    const fields = line.trim().split("\t");
    if (fields[0] !== "dep") continue;
    if (fields.length !== 4 || observed.has(coordinate(fields[1], fields[2]))) {
      fail("NOTICES_BINARY", "release binary module identity is duplicated or incomplete");
    }
    observed.set(coordinate(fields[1], fields[2]), fields[3]);
  }
  if (!sameSet(new Set(observed.keys()), new Set(expected.keys())) ||
      [...expected].some(([key, sum]) => observed.get(key) !== sum)) {
    fail("NOTICES_BINARY", "release binary modules do not match the reviewed notices inventory");
  }
  return { kind, moduleCount: observed.size, toolchain: toolchain.version };
}

function inventoryOrder(left, right) {
  return compareText(
    `${left.ecosystem}\u0000${left.name}\u0000${left.version}\u0000${left.source}`,
    `${right.ecosystem}\u0000${right.name}\u0000${right.version}\u0000${right.source}`,
  );
}

function mergeTexts(...maps) {
  const merged = new Map();
  for (const map of maps) {
    for (const [digest, value] of map) {
      const existing = merged.get(digest);
      if (existing && !existing.bytes.equals(value.bytes)) fail("NOTICES_LICENSE_TEXT", "license text digests conflicted");
      if (!existing) {
        merged.set(digest, { appliesTo: new Set(value.appliesTo), bytes: value.bytes, names: new Set(value.names) });
        continue;
      }
      for (const item of value.appliesTo) existing.appliesTo.add(item);
      for (const item of value.names) existing.names.add(item);
    }
  }
  return merged;
}

export function unsafeNoticeContent(text) {
  const patterns = [
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
  ];
  return patterns.some((pattern) => pattern.test(text));
}

export function auditLicenseInventory(repositoryRoot, options = {}) {
  const root = resolve(repositoryRoot);
  const policy = loadLicensePolicy(root);
  const installedScope = options.installedScope ?? "all";
  const npm = npmLicenseInventory(root, { installedScope, policy });
  const go = goLicenseInventory(root, { policy });
  const fullInventory = [...npm.inventory, ...go.inventory].sort(inventoryOrder);
  const inventory = [
    ...npm.inventory.filter((item) => item.shipped),
    ...go.inventory,
  ].sort(inventoryOrder);
  const texts = mergeTexts(npm.texts, go.texts);
  const licenses = {};
  for (const item of inventory) licenses[item.spdx] = (licenses[item.spdx] ?? 0) + 1;
  const summary = {
    schemaVersion: 1,
    npmInstalledScope: installedScope,
    npmLockEntries: npm.lockEntryCount,
    npmCoordinates: npm.inventory.length,
    npmLinuxCoordinates: npm.inventory.filter((item) => !item.platformExcluded).length,
    npmInstalledVerifiedCoordinates: npm.verifiedCoordinateCount,
    npmPlatformExcludedEntries: npm.platformExcludedEntryCount,
    npmShippedCoordinates: npm.inventory.filter((item) => item.shipped).length,
    npmBuildOnlyCoordinates: npm.inventory.filter((item) => !item.shipped).length,
    goModules: go.inventory.filter((item) => item.ecosystem === "go-module").length,
    goToolchains: go.inventory.filter((item) => item.ecosystem === "go-toolchain").length,
    licenseTexts: texts.size,
    licenses: Object.fromEntries(Object.entries(licenses).sort(([left], [right]) => compareText(left, right))),
    inventorySha256: sha256(JSON.stringify(inventory)),
    fullLockInventorySha256: sha256(JSON.stringify(npm.inventory)),
    packageLockSha256: sha256(readFileSync(join(root, "package-lock.json"))),
    goModSha256: sha256(readFileSync(join(root, "engine/go.mod"))),
    goSumSha256: sha256(readFileSync(join(root, "engine/go.sum"))),
    policySha256: sha256(readFileSync(join(root, POLICY_PATH))),
  };
  return { fullInventory, inventory, summary, texts };
}

export function renderThirdPartyNotices(repositoryRoot, candidate, options = {}) {
  if (!/^[0-9a-f]{40}$/u.test(candidate ?? "")) fail("NOTICES_ARGUMENT", "source Candidate is invalid");
  const installedScope = options.installedScope ?? "all";
  const { inventory, summary, texts } = auditLicenseInventory(repositoryRoot, { installedScope });
  const lines = [
    "Director for Paseo third-party notices",
    "schema-version: 1",
    `source-candidate: ${candidate}`,
    "target: linux-amd64",
    "root-license: Apache-2.0",
    `inventory-sha256: ${summary.inventorySha256}`,
    `package-lock-sha256: ${summary.packageLockSha256}`,
    `go-mod-sha256: ${summary.goModSha256}`,
    `go-sum-sha256: ${summary.goSumSha256}`,
    `license-policy-sha256: ${summary.policySha256}`,
    "generated-by: tools/release/notices.mjs",
    "",
    "Director source is licensed under Apache-2.0; see the root LICENSE file.",
    "The complete npm lock is audited, but only the seven-package installed production closure is shipped with the plugin.",
    "CI/build-only and platform-excluded lock entries are not release contents and are omitted below.",
    "Go standard-library entries identify content linked into both linux-amd64 release binaries; module entries identify content linked only into Director Engine.",
    "The MPL-2.0-covered Go module remains available in Source Code Form at the exact source URL below.",
    "",
    "== Canonical dependency inventory ==",
    JSON.stringify({ schemaVersion: 1, summary, dependencies: inventory }, null, 2),
    "",
    "== License and NOTICE texts ==",
  ];
  for (const [digest, value] of [...texts].sort(([left], [right]) => compareText(left, right))) {
    lines.push(
      "",
      `----- BEGIN NOTICE ${digest} -----`,
      `filenames: ${[...value.names].sort().join(", ")}`,
      `applies-to: ${[...value.appliesTo].sort().join(", ")}`,
      "",
      normalizeText(value.bytes).trimEnd(),
      `----- END NOTICE ${digest} -----`,
    );
  }
  const output = `${lines.join("\n")}\n`;
  if (Buffer.byteLength(output) > MAXIMUM_NOTICES_BYTES) fail("NOTICES_SIZE", "generated notices exceed the release bound");
  if (unsafeNoticeContent(output)) fail("NOTICES_REDACTION", "generated notices contain a private path, secret, or internal identity");
  return Buffer.from(output);
}

export function verifyThirdPartyNotices(repositoryRoot, candidate, bytes, options = {}) {
  const installedScope = options.installedScope ?? "all";
  const expected = renderThirdPartyNotices(repositoryRoot, candidate, { installedScope });
  if (!Buffer.from(bytes).equals(expected)) fail("NOTICES_STALE", "third-party notices are missing, stale, duplicated, or mutated");
  return { ...auditLicenseInventory(repositoryRoot, { installedScope }).summary, noticesSha256: sha256(expected), noticesBytes: expected.length };
}

function assertExactSource(repositoryRoot, candidate) {
  const head = run("git", ["-C", repositoryRoot, "rev-parse", "HEAD"], { code: "NOTICES_SOURCE", timeout: 10_000 });
  const status = run("git", ["-C", repositoryRoot, "status", "--porcelain"], { code: "NOTICES_SOURCE", timeout: 10_000 });
  if (head !== candidate || status !== "") fail("NOTICES_SOURCE", "source is not the exact clean Candidate");
}

function writePrivate(path, bytes) {
  if (!isAbsolute(path) || existsSync(path)) fail("NOTICES_OUTPUT", "notices output must be a new absolute path");
  const descriptor = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try {
    writeFileSync(descriptor, bytes);
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

function argumentsFor(argv) {
  const [action, ...rest] = argv;
  if (!["check", "generate", "verify"].includes(action) || rest.length % 2 !== 0) {
    fail("NOTICES_ARGUMENT", "usage is check, generate, or verify with explicit paths");
  }
  const values = new Map();
  for (let index = 0; index < rest.length; index += 2) {
    const name = rest[index];
    const value = rest[index + 1];
    if (!name.startsWith("--") || values.has(name)) fail("NOTICES_ARGUMENT", "notices arguments are invalid");
    values.set(name, value);
  }
  const permitted = action === "check" ? ["--installed-scope", "--source"] : action === "generate"
    ? ["--candidate", "--installed-scope", "--output", "--source"]
    : ["--candidate", "--installed-scope", "--notices", "--source"];
  if ([...values.keys()].some((name) => !permitted.includes(name)) || !values.get("--source")) {
    fail("NOTICES_ARGUMENT", "notices arguments are invalid");
  }
  const installedScope = values.get("--installed-scope") ?? "all";
  if (!["all", "production"].includes(installedScope)) fail("NOTICES_ARGUMENT", "installed npm scope is invalid");
  return { action, installedScope, values, source: resolve(values.get("--source")) };
}

const modulePath = fileURLToPath(import.meta.url);
if (process.argv[1] && resolve(process.argv[1]) === resolve(modulePath)) {
  try {
    const parsed = argumentsFor(process.argv.slice(2));
    let result;
    if (parsed.action === "check") {
      result = auditLicenseInventory(parsed.source, { installedScope: parsed.installedScope }).summary;
    } else {
      const candidate = parsed.values.get("--candidate");
      assertExactSource(parsed.source, candidate);
      if (parsed.action === "generate") {
        const bytes = renderThirdPartyNotices(parsed.source, candidate, { installedScope: parsed.installedScope });
        writePrivate(resolve(parsed.values.get("--output")), bytes);
        result = { ...auditLicenseInventory(parsed.source, { installedScope: parsed.installedScope }).summary, noticesSha256: sha256(bytes), noticesBytes: bytes.length };
      } else {
        const notices = resolve(parsed.values.get("--notices"));
        const status = lstatSync(notices);
        if (!status.isFile() || status.isSymbolicLink()) fail("NOTICES_OUTPUT", "notices input must be a regular file");
        result = verifyThirdPartyNotices(parsed.source, candidate, readFileSync(notices), { installedScope: parsed.installedScope });
      }
    }
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } catch (error) {
    const code = error instanceof NoticesError ? error.code : "NOTICES_FAILED";
    const message = error instanceof NoticesError ? error.message : "third-party notices verification failed";
    process.stderr.write(`${code}: ${message}\n`);
    process.exitCode = 1;
  }
}
