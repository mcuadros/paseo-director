// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { BootstrapBuildError, PRODUCTION_GO_TOOLCHAIN_CANDIDATES, buildMainBootstrap, cachePublishedBootstrap, resolveBootstrapGoToolchain, runBootstrapPreparation } from "./bootstrap-build.mjs";
import { createHermeticBootstrapBuildCore, evaluateSystemExecutableTrustForTest } from "./bootstrap-build.test-support.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

function testOwnedELF(root, name = "go") {
  const directory = join(root, "toolchain");
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const executable = join(directory, name);
  copyFileSync(process.execPath, executable);
  chmodSync(executable, 0o500);
  return executable;
}

function commandResult(stdout = "") {
  return { status: 0, stdout, stderr: "", error: null, signal: null };
}

test("main install compiles the exact Go bootstrap once outside PATH and never builds the Engine", { timeout: 360_000 }, () => {
  const root = mkdtempSync(join(tmpdir(), "director-bootstrap-build-"));
  const candidate = "a".repeat(40);
  const go = testOwnedELF(root);
  const calls = [];
  let bootstrapBuilds = 0;
  let engineBuilds = 0;
  const deterministic = (executable, args, options) => {
    calls.push({ executable, args: [...args], options });
    if (executable === go && args[0] === "version") return commandResult("go version go1.26.5 linux/amd64\n");
    if (executable === go && args[0] === "env") return commandResult("linux\namd64\ngo1.26.5\n");
    if (executable === go && args[0] === "build") {
      if (args.at(-1) === "./cmd/director-bootstrap") bootstrapBuilds += 1;
      if (args.at(-1) === "./cmd/director-engine") engineBuilds += 1;
      const output = args[args.indexOf("-o") + 1];
      copyFileSync(process.execPath, output);
      chmodSync(output, 0o500);
      return commandResult();
    }
    if (executable !== go && args.length === 1 && args[0] === "version") {
      return commandResult(JSON.stringify({ name: "director-bootstrap", buildMode: "main", sourceCandidate: candidate, target: "linux-amd64" }));
    }
    throw new Error("unexpected hermetic bootstrap command");
  };
  try {
    const moduleCache = join(root, "module-cache");
    const environment = { HOME: root, XDG_CACHE_HOME: join(root, "cache"), GOMODCACHE: moduleCache, PATH: "/poison" };
    const core = createHermeticBootstrapBuildCore({ fixedCandidates: [go], spawnSync: deterministic });
    const first = core.buildMainBootstrap({ repositoryRoot, sourceCandidate: candidate, environment });
    assert.equal(first.compilerInvocations, 1);
    assert.equal(bootstrapBuilds, 1);
    assert.equal(engineBuilds, 0);
    assert.equal(first.metadata.target, "linux-amd64");
    const versionProbe = calls.find((call) => call.executable === go && call.args[0] === "version");
    const capabilityProbe = calls.find((call) => call.executable === go && call.args[0] === "env");
    const build = calls.find((call) => call.executable === go && call.args[0] === "build");
    assert.deepEqual(versionProbe.options.env, { GOENV: "off", GOTOOLCHAIN: "local", GOWORK: "off" });
    assert.deepEqual(capabilityProbe.options.env, { GOENV: "off", GOTOOLCHAIN: "local", GOWORK: "off" });
    assert.equal(versionProbe.options.cwd, root);
    assert.equal(capabilityProbe.options.cwd, root);
    const output = build.args[build.args.indexOf("-o") + 1];
    assert.deepEqual(build.args, ["build", "-trimpath", "-buildvcs=false", "-mod=readonly", "-ldflags", `-s -w -X main.bootstrapMode=main -X main.bootstrapCandidate=${candidate}`, "-o", output, "./cmd/director-bootstrap"]);
    assert.deepEqual(build.options.env, { HOME: root, GOCACHE: join(root, "cache", "director", "go-build-cache", "go1.26.5"), GOMODCACHE: moduleCache,
      CGO_ENABLED: "0", GOARCH: "amd64", GOOS: "linux", GOENV: "off", GOWORK: "off", GOPROXY: "off", GOSUMDB: "off", GOTOOLCHAIN: "local" });
    assert.equal(build.options.env.PATH, undefined);
    const callCount = calls.length;
    const replay = core.buildMainBootstrap({ repositoryRoot, sourceCandidate: candidate, environment });
    assert.equal(replay.compilerInvocations, 0);
    assert.equal(bootstrapBuilds, 1);
    assert.equal(engineBuilds, 0);
    assert.equal(calls.length, callCount);
    assert.deepEqual(replay.metadata, first.metadata);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test("installed production build is fixed to two system candidates and exposes no injection seam", () => {
  assert.deepEqual(PRODUCTION_GO_TOOLCHAIN_CANDIDATES, ["/usr/local/go/bin/go", "/usr/bin/go"]);
  assert.equal(Object.isFrozen(PRODUCTION_GO_TOOLCHAIN_CANDIDATES), true);
  assert.doesNotMatch(buildMainBootstrap.toString(), /fixedCandidates|spawnSync|toolchain/iu);
  assert.doesNotMatch(resolveBootstrapGoToolchain.toString(), /arguments|options|fixedCandidates|spawnSync/iu);
});

test("published bootstrap is copied from its exact manifest pin with zero Go", () => {
  const root = mkdtempSync(join(tmpdir(), "director-bootstrap-release-"));
  const repository = join(root, "package");
  const release = join(repository, "release");
  mkdirSync(release, { recursive: true, mode: 0o700 });
  const bytes = Buffer.from("published bootstrap fixture\n");
  const binary = join(release, "director-bootstrap-linux-amd64");
  writeFileSync(binary, bytes, { mode: 0o500 }); chmodSync(binary, 0o500);
  const manifest = { schemaVersion: 1, state: "published", version: "1.0.0", target: "linux-amd64", sourceCandidate: "b".repeat(40),
    binary: { name: "director-bootstrap-linux-amd64", url: "https://github.com/mcuadros/paseo-director/releases/download/v1.0.0/director-bootstrap-linux-amd64", sha256: createHash("sha256").update(bytes).digest("hex"), size: bytes.byteLength } };
  try {
    const result = cachePublishedBootstrap({ repositoryRoot: repository, sourceCandidate: manifest.sourceCandidate, manifest, environment: { HOME: root, XDG_CACHE_HOME: join(root, "cache") } });
    assert.equal(result.compilerInvocations, 0);
    assert.equal(readFileSync(result.metadata.path, "utf8"), bytes.toString());
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test("bootstrap preparation executes only the fixed Go controller and accepts bounded state", () => {
  const bootstrap = { schemaVersion: 1, target: "linux-amd64", path: "/private/director-bootstrap", sha256: "c".repeat(64), size: 42 };
  const candidate = "d".repeat(40);
  const calls = [];
  const result = runBootstrapPreparation({ repositoryRoot: "/private/checkout", sourceCandidate: candidate, bootstrap, environment: { HOME: "/private/home" },
    spawnSync(executable, args, options) {
      calls.push({ executable, args, options });
      return { status: 0, stdout: JSON.stringify({ schemaVersion: 1, code: "DIRECTOR_BOOTSTRAP_PREPARED", channel: "main", sourceCandidate: candidate, bootstrap: { sha256: bootstrap.sha256 }, engineBuilds: 1 }), stderr: "", error: null, signal: null };
    } });
  assert.equal(result.engineBuilds, 1);
  assert.deepEqual(calls[0].args, ["prepare", "--candidate", candidate, "--bootstrap-sha256", bootstrap.sha256, "--host-socket", ""]);
  assert.equal(calls[0].executable, bootstrap.path);
  assert.equal(calls[0].options.env.PATH, undefined);
});

test("bootstrap preparation promotes only bounded Go controller diagnostics", () => {
  const bootstrap = { schemaVersion: 1, target: "linux-amd64", path: "/private/director-bootstrap", sha256: "c".repeat(64), size: 42 };
  assert.throws(() => runBootstrapPreparation({ repositoryRoot: "/private/checkout", sourceCandidate: "d".repeat(40), bootstrap, environment: { HOME: "/private/home" },
    spawnSync() { return { status: 1, stdout: "", stderr: "DIRECTOR_MAIN_BUILD_FAILED\n", error: null, signal: null }; } }),
  (error) => error instanceof BootstrapBuildError && error.code === "DIRECTOR_MAIN_BUILD_FAILED" && !error.message.includes("/private/checkout"));
  assert.throws(() => runBootstrapPreparation({ repositoryRoot: "/private/checkout", sourceCandidate: "d".repeat(40), bootstrap, environment: { HOME: "/private/home" },
    spawnSync() { return { status: 1, stdout: "", stderr: "private/path leaked\n", error: null, signal: null }; } }),
  (error) => error instanceof BootstrapBuildError && error.code === "DIRECTOR_BOOTSTRAP_PREPARATION_FAILED" && !error.message.includes("private/path"));
});

test("missing fixed-system Go fails with a bounded path-free diagnostic", () => {
  let commands = 0;
  const core = createHermeticBootstrapBuildCore({ fixedCandidates: [], spawnSync() { commands += 1; throw new Error("must not execute"); } });
  assert.throws(() => core.resolveBootstrapGoToolchain(),
    (error) => error instanceof BootstrapBuildError && !(error instanceof TypeError) && error.code === "DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING" && !error.message.includes(repositoryRoot));
  assert.equal(commands, 0);
});

test("wrong fixed-system Go version and capability fail with exact bounded diagnostics", () => {
  const root = mkdtempSync(join(tmpdir(), "director-bootstrap-toolchain-"));
  try {
    const candidate = testOwnedELF(root);
    const response = (version, capability) => (_executable, args) => commandResult(args[0] === "version" ? version : capability);
    const wrongVersion = createHermeticBootstrapBuildCore({ fixedCandidates: [candidate], spawnSync: response("go version go1.25.0 linux/amd64\n", "linux\namd64\ngo1.25.0\n") });
    assert.throws(() => wrongVersion.resolveBootstrapGoToolchain(),
      (error) => error instanceof BootstrapBuildError && error.code === "DIRECTOR_MAIN_GO_TOOLCHAIN_VERSION");
    const wrongCapability = createHermeticBootstrapBuildCore({ fixedCandidates: [candidate], spawnSync: response("go version go1.26.5 linux/amd64\n", "linux\narm64\ngo1.26.5\n") });
    assert.throws(() => wrongCapability.resolveBootstrapGoToolchain(),
      (error) => error instanceof BootstrapBuildError && error.code === "DIRECTOR_MAIN_GO_TOOLCHAIN_CAPABILITY");
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test("unsafe mode, symlink, and foreign ownership fail the real toolchain trust policy", () => {
  const root = mkdtempSync(join(tmpdir(), "director-bootstrap-trust-"));
  let commands = 0;
  const unavailable = (candidate) => createHermeticBootstrapBuildCore({ fixedCandidates: [candidate], spawnSync() { commands += 1; throw new Error("must not execute"); } });
  try {
    const unsafeMode = testOwnedELF(root, "unsafe-mode");
    chmodSync(unsafeMode, 0o720);
    const symlinkTarget = testOwnedELF(root, "symlink-target");
    const symlink = join(dirname(symlinkTarget), "symlink");
    symlinkSync(symlinkTarget, symlink);
    assert.throws(() => unavailable(unsafeMode).resolveBootstrapGoToolchain(), (error) => error instanceof BootstrapBuildError && error.code === "DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING");
    assert.throws(() => unavailable(symlink).resolveBootstrapGoToolchain(), (error) => error instanceof BootstrapBuildError && error.code === "DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING");
    assert.equal(evaluateSystemExecutableTrustForTest({
      file: { isFile: true, isSymbolicLink: false, mode: 0o100500, uid: 4242 },
      parents: [
        { isDirectory: true, isSymbolicLink: false, mode: 0o040755, uid: 4243 },
        { isDirectory: true, isSymbolicLink: false, mode: 0o040755, uid: 0 },
      ],
      realpathMatches: true,
      effectiveUserID: 1000,
      elf: true,
    }), false);
    assert.equal(commands, 0);
  } finally { rmSync(root, { recursive: true, force: true }); }
});
