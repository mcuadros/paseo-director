// SPDX-License-Identifier: Apache-2.0

import { createHash } from "node:crypto";
import {
  closeSync,
  constants,
  existsSync,
  fstatSync,
  lstatSync,
  openSync,
  readFileSync,
  realpathSync,
} from "node:fs";
import { homedir } from "node:os";
import {
  basename,
  dirname,
  isAbsolute,
  join,
  relative,
  resolve,
} from "node:path";

const MAXIMUM_CONFIGURATION_BYTES = 64 * 1024;
const MAXIMUM_CREDENTIAL_BYTES = 64 * 1024;
const LEGACY_DIRECTOR_ENVIRONMENT = new Set([
  "DIRECTOR_ENGINE_MODE",
  "DIRECTOR_ENGINE_RELEASE_METADATA",
  "DIRECTOR_ENGINE_SOURCE_ROOT",
  "DIRECTOR_ENGINE_URL",
  "DIRECTOR_PASEO_CREDENTIAL_FILE",
  "DIRECTOR_PASEO_PASSWORD",
  "DIRECTOR_PASEO_URL",
]);

export const DEFAULT_ENGINE_MODE = "release";
export const DEFAULT_ENGINE_URL = "http://127.0.0.1:7041";
export const DEFAULT_PASEO_URL = "ws://127.0.0.1:6767/ws";

export class RuntimeConfigurationError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "RuntimeConfigurationError";
    this.code = code;
  }
}

function fail(code, message) {
  throw new RuntimeConfigurationError(code, message);
}

function strictKeys(value, expected) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return actual.length === wanted.length &&
    actual.every((key, index) => key === wanted[index]);
}

function optionalStrictKeys(value, required, optional) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const keys = Object.keys(value);
  return required.every((key) => keys.includes(key)) &&
    keys.every((key) => required.includes(key) || optional.includes(key));
}

function boundedString(value, maximum = 4_096) {
  return typeof value === "string" && value.length > 0 && value.length <= maximum;
}

function rejectDuplicateJSONKeys(text) {
  let offset = 0;
  const whitespace = () => {
    while (/\s/u.test(text[offset] ?? "")) offset += 1;
  };
  const string = () => {
    if (text[offset] !== '"') throw new Error("string expected");
    const start = offset;
    offset += 1;
    while (offset < text.length) {
      if (text[offset] === "\\") {
        offset += 2;
        continue;
      }
      if (text[offset] === '"') {
        offset += 1;
        return JSON.parse(text.slice(start, offset));
      }
      offset += 1;
    }
    throw new Error("unterminated string");
  };
  const value = () => {
    whitespace();
    if (text[offset] === "{") return object();
    if (text[offset] === "[") return array();
    if (text[offset] === '"') {
      string();
      return;
    }
    const start = offset;
    while (offset < text.length && !/[\s,}\]]/u.test(text[offset])) offset += 1;
    if (start === offset) throw new Error("value expected");
    JSON.parse(text.slice(start, offset));
  };
  const object = () => {
    const keys = new Set();
    offset += 1;
    whitespace();
    if (text[offset] === "}") {
      offset += 1;
      return;
    }
    while (offset < text.length) {
      whitespace();
      const key = string();
      if (keys.has(key)) throw new Error("duplicate key");
      keys.add(key);
      whitespace();
      if (text[offset] !== ":") throw new Error("colon expected");
      offset += 1;
      value();
      whitespace();
      if (text[offset] === "}") {
        offset += 1;
        return;
      }
      if (text[offset] !== ",") throw new Error("comma expected");
      offset += 1;
    }
    throw new Error("unterminated object");
  };
  const array = () => {
    offset += 1;
    whitespace();
    if (text[offset] === "]") {
      offset += 1;
      return;
    }
    while (offset < text.length) {
      value();
      whitespace();
      if (text[offset] === "]") {
        offset += 1;
        return;
      }
      if (text[offset] !== ",") throw new Error("comma expected");
      offset += 1;
    }
    throw new Error("unterminated array");
  };
  value();
  whitespace();
  if (offset !== text.length) throw new Error("trailing input");
}

function currentUID() {
  if (typeof process.geteuid !== "function") {
    fail(
      "DIRECTOR_RUNTIME_PLATFORM",
      "Director runtime configuration requires Linux effective-user identity",
    );
  }
  return process.geteuid();
}

function assertSafeAncestors(filePath, fileStatus, code) {
  const uid = currentUID();
  let root = dirname(filePath);
  while (dirname(root) !== root) root = dirname(root);
  const rootUID = lstatSync(root).uid;
  let protectedStatus = fileStatus;
  let current = dirname(filePath);
  while (true) {
    const status = lstatSync(current);
    const trustedOwner = (owner) => owner === uid || owner === rootUID;
    const stickyProtection =
      (status.mode & 0o1000) !== 0 &&
      trustedOwner(status.uid) &&
      trustedOwner(protectedStatus.uid);
    if (
      !status.isDirectory() ||
      status.isSymbolicLink() ||
      ((status.mode & 0o022) !== 0 && !stickyProtection)
    ) {
      fail(code, "Director runtime configuration has an unsafe ancestor");
    }
    protectedStatus = status;
    const parent = dirname(current);
    if (parent === current) break;
    current = parent;
  }
}

function readPrivateRegularFile(filePath, maximumBytes, code) {
  let before;
  try {
    before = lstatSync(filePath);
  } catch {
    fail(code, "Director runtime configuration is unavailable");
  }
  const uid = currentUID();
  if (
    !before.isFile() ||
    before.isSymbolicLink() ||
    before.uid !== uid ||
    (before.mode & 0o077) !== 0 ||
    before.size <= 0 ||
    before.size > maximumBytes ||
    before.nlink !== 1
  ) {
    fail(code, "Director runtime configuration is not a private regular file");
  }
  assertSafeAncestors(filePath, before, code);
  const descriptor = openSync(filePath, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const opened = fstatSync(descriptor);
    if (
      opened.dev !== before.dev ||
      opened.ino !== before.ino ||
      opened.mode !== before.mode ||
      opened.uid !== before.uid ||
      opened.size !== before.size ||
      opened.mtimeMs !== before.mtimeMs ||
      opened.ctimeMs !== before.ctimeMs ||
      opened.nlink !== before.nlink
    ) {
      fail(code, "Director runtime configuration changed while opening");
    }
    const bytes = readFileSync(descriptor);
    const after = fstatSync(descriptor);
    if (
      after.mode !== opened.mode ||
      after.uid !== opened.uid ||
      after.size !== opened.size ||
      after.mtimeMs !== opened.mtimeMs ||
      after.ctimeMs !== opened.ctimeMs ||
      after.nlink !== opened.nlink
    ) {
      fail(code, "Director runtime configuration changed while reading");
    }
    return bytes;
  } finally {
    closeSync(descriptor);
  }
}

export function canonicalProspectivePath(value) {
  let existing = resolve(value);
  const missing = [];
  while (!existsSync(existing)) {
    const parent = dirname(existing);
    if (parent === existing) break;
    missing.unshift(basename(existing));
    existing = parent;
  }
  return resolve(realpathSync(existing), ...missing);
}

export function pathIsWithin(candidate, parent) {
  const fromParent = relative(
    canonicalProspectivePath(parent),
    canonicalProspectivePath(candidate),
  );
  return fromParent === "" ||
    (!fromParent.startsWith("..") && !isAbsolute(fromParent));
}

export function pathsAreDisjoint(left, right) {
  return !pathIsWithin(left, right) && !pathIsWithin(right, left);
}

export function runtimeConfigurationPath(environment = process.env, home = homedir()) {
  const configuredBase = environment.XDG_CONFIG_HOME;
  if (configuredBase !== undefined && !isAbsolute(configuredBase)) {
    fail(
      "DIRECTOR_RUNTIME_CONFIG_BASE",
      "XDG_CONFIG_HOME must be absolute when set",
    );
  }
  const base = configuredBase ?? join(home, ".config");
  return resolve(base, "director", "runtime.json");
}

function assertLoopbackURL(value, protocol, path, code) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    fail(code, "Director runtime URL is invalid");
  }
  const hostname = parsed.hostname.startsWith("[")
    ? parsed.hostname.slice(1, -1)
    : parsed.hostname;
  const loopback = hostname === "::1" || /^127(?:\.[0-9]{1,3}){3}$/u.test(hostname);
  if (
    parsed.protocol !== protocol ||
    !loopback ||
    parsed.port === "" ||
    parsed.username !== "" ||
    parsed.password !== "" ||
    parsed.pathname !== path ||
    parsed.search !== "" ||
    parsed.hash !== ""
  ) {
    fail(code, "Director runtime URL must be an origin-only loopback endpoint");
  }
}

export function legacyDirectorEnvironmentState(environment = process.env) {
  return Object.keys(environment).some(
    (name) => LEGACY_DIRECTOR_ENVIRONMENT.has(name) && environment[name] !== undefined,
  ) ? "ignored" : "absent";
}

export function parseRuntimeConfiguration(bytes, environment = process.env) {
  let value;
  try {
    const text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    rejectDuplicateJSONKeys(text);
    value = JSON.parse(text);
  } catch {
    fail("DIRECTOR_RUNTIME_CONFIG_JSON", "Director runtime configuration is not valid JSON");
  }
  if (!strictKeys(value, ["engine", "paseo", "schemaVersion"]) || value.schemaVersion !== 1) {
    fail("DIRECTOR_RUNTIME_CONFIG_SCHEMA", "Director runtime configuration fields do not match schema 1");
  }
  if (!strictKeys(value.paseo, ["credentialFile"])) {
    fail("DIRECTOR_RUNTIME_CONFIG_SCHEMA", "Director Paseo runtime fields do not match schema 1");
  }
  if (
    !optionalStrictKeys(value.engine, [], ["mode", "moduleCache", "sourceRoot", "url"])
  ) {
    fail("DIRECTOR_RUNTIME_CONFIG_SCHEMA", "Director Engine runtime fields do not match schema 1");
  }
  if (!boundedString(value.paseo.credentialFile)) {
    fail("DIRECTOR_RUNTIME_CONFIG_SCHEMA", "Director Paseo runtime values are invalid");
  }
  if (!isAbsolute(value.paseo.credentialFile)) {
    fail("DIRECTOR_RUNTIME_CREDENTIAL_PATH", "The connector credential file path must be absolute");
  }
  const mode = value.engine.mode ?? DEFAULT_ENGINE_MODE;
  const engineURL = value.engine.url ?? DEFAULT_ENGINE_URL;
  if (mode !== "release" && mode !== "development") {
    fail("DIRECTOR_RUNTIME_ENGINE_MODE", "Director Engine mode must be release or development");
  }
  assertLoopbackURL(engineURL, "http:", "/", "DIRECTOR_RUNTIME_ENGINE_URL");
  for (const field of ["sourceRoot", "moduleCache"]) {
    const fieldValue = value.engine[field];
    if (fieldValue !== undefined && (!boundedString(fieldValue) || !isAbsolute(fieldValue))) {
      fail("DIRECTOR_RUNTIME_ENGINE_PATH", "Director Engine paths must be absolute");
    }
  }
  if (mode === "release" && (value.engine.sourceRoot !== undefined || value.engine.moduleCache !== undefined)) {
    fail("DIRECTOR_RUNTIME_ENGINE_CONFLICT", "Release mode cannot select development paths");
  }
  if (mode === "development" && value.engine.sourceRoot === undefined) {
    fail("DIRECTOR_RUNTIME_ENGINE_SOURCE", "Development mode requires an explicit source root");
  }

  const diagnostics = {
    schemaVersion: 1,
    sha256: createHash("sha256").update(bytes).digest("hex"),
    legacyEnvironment: legacyDirectorEnvironmentState(environment),
    settings: [
      { name: "paseo.url", source: "defaulted" },
      { name: "engine.mode", source: value.engine.mode === undefined ? "defaulted" : "overridden" },
      { name: "engine.url", source: value.engine.url === undefined ? "defaulted" : "overridden" },
      {
        name: "engine.cache-base",
        source: environment.XDG_CACHE_HOME === undefined ? "defaulted" : "overridden",
      },
      ...(mode === "development"
        ? [{ name: "engine.module-cache", source: value.engine.moduleCache === undefined ? "defaulted" : "overridden" }]
        : []),
    ],
  };
  return {
    schemaVersion: 1,
    paseo: {
      url: DEFAULT_PASEO_URL,
      credentialFile: canonicalProspectivePath(value.paseo.credentialFile),
    },
    engine: {
      mode,
      url: engineURL,
      ...(value.engine.sourceRoot
        ? { sourceRoot: canonicalProspectivePath(value.engine.sourceRoot) }
        : {}),
      ...(value.engine.moduleCache
        ? { moduleCache: canonicalProspectivePath(value.engine.moduleCache) }
        : {}),
    },
    diagnostics,
  };
}

export function loadRuntimeConfiguration(environment = process.env, home = homedir()) {
  const path = runtimeConfigurationPath(environment, home);
  const bytes = readPrivateRegularFile(
    path,
    MAXIMUM_CONFIGURATION_BYTES,
    "DIRECTOR_RUNTIME_CONFIG_REQUIRED",
  );
  return { path, configuration: parseRuntimeConfiguration(bytes, environment) };
}

export function assertCredentialFileDeployment(credentialFile) {
  readPrivateRegularFile(
    credentialFile,
    MAXIMUM_CREDENTIAL_BYTES,
    "DIRECTOR_RUNTIME_CREDENTIAL_REQUIRED",
  );
}

export function assertRuntimeDeployment(repositoryRoot, loaded, environment = process.env) {
  const checkout = canonicalProspectivePath(repositoryRoot);
  const configDirectory = dirname(loaded.path);
  const credentialDirectory = dirname(loaded.configuration.paseo.credentialFile);
  const cacheBase = environment.XDG_CACHE_HOME
    ? canonicalProspectivePath(environment.XDG_CACHE_HOME)
    : canonicalProspectivePath(join(homedir(), ".cache"));
  const cacheRoot = canonicalProspectivePath(join(cacheBase, "director", "engines"));
  const protectedPaths = [checkout, cacheRoot];
  if (loaded.configuration.engine.sourceRoot) {
    protectedPaths.push(loaded.configuration.engine.sourceRoot);
  }
  if (loaded.configuration.engine.moduleCache) {
    protectedPaths.push(loaded.configuration.engine.moduleCache);
  }
  if (!pathsAreDisjoint(configDirectory, checkout)) {
    fail("DIRECTOR_RUNTIME_CONFIG_IN_CHECKOUT", "Director runtime configuration must be outside the plugin checkout");
  }
  if (!pathsAreDisjoint(cacheRoot, checkout)) {
    fail("DIRECTOR_RUNTIME_CACHE_IN_CHECKOUT", "Director Engine cache must be outside the plugin checkout");
  }
  if (protectedPaths.some((protectedPath) => !pathsAreDisjoint(credentialDirectory, protectedPath))) {
    fail("DIRECTOR_RUNTIME_CREDENTIAL_OVERLAP", "The connector credential must be outside plugin and engine paths");
  }
  assertCredentialFileDeployment(loaded.configuration.paseo.credentialFile);
  return loaded.configuration;
}
