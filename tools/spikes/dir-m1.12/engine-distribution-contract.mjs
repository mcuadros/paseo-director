#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
  closeSync,
  existsSync,
  fsyncSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";

const VERSION = "0.0.0-spike";
const TARGET = "linux-amd64";
const FIXED_GIT_DATE = "2000-01-01T00:00:00Z";

class ContractError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? process.env,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(
      `${command} ${args.join(" ")} failed (${result.status}): ${result.stderr}`,
    );
  }
  return result.stdout.trim();
}

function write(path, contents, mode = 0o600) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(path, contents, { encoding: "utf8", mode });
}

function initializeRepository(path) {
  const gitEnvironment = {
    ...process.env,
    GIT_AUTHOR_DATE: FIXED_GIT_DATE,
    GIT_COMMITTER_DATE: FIXED_GIT_DATE,
  };
  run("git", ["init", "-b", "main"], { cwd: path, env: gitEnvironment });
  run("git", ["config", "user.name", "Director Distribution Fixture"], {
    cwd: path,
    env: gitEnvironment,
  });
  run(
    "git",
    ["config", "user.email", "director-distribution@example.invalid"],
    { cwd: path, env: gitEnvironment },
  );
  run("git", ["add", "-A"], { cwd: path, env: gitEnvironment });
  run("git", ["commit", "-m", "fixture: pin release closure"], {
    cwd: path,
    env: gitEnvironment,
  });
  return run("git", ["rev-parse", "HEAD"], {
    cwd: path,
    env: gitEnvironment,
  });
}

function isWithin(path, possibleParent) {
  const pathFromParent = relative(resolve(possibleParent), resolve(path));
  return pathFromParent === "" ||
    (!pathFromParent.startsWith("..") && !pathFromParent.startsWith("/"));
}

function goEnvironment() {
  return {
    ...process.env,
    CGO_ENABLED: "0",
    GOARCH: "amd64",
    GOOS: "linux",
    GOPROXY: "off",
    GOSUMDB: "off",
    GOTOOLCHAIN: "local",
  };
}

function buildEngine({
  sourceRoot,
  destination,
  mode,
  version,
  sourceCandidate,
  noticesSha,
}) {
  mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
  run(
    "go",
    [
      "build",
      "-trimpath",
      "-buildvcs=false",
      "-ldflags",
      [
        "-s -w",
        `-X main.buildMode=${mode}`,
        `-X main.version=${version}`,
        `-X main.sourceCandidate=${sourceCandidate}`,
        `-X main.noticesSha=${noticesSha}`,
      ].join(" "),
      "-o",
      destination,
      ".",
    ],
    { cwd: sourceRoot, env: goEnvironment() },
  );
  chmodSync(destination, 0o500);
}

async function listen(server) {
  await new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  assert(address && typeof address === "object");
  return `http://127.0.0.1:${address.port}`;
}

async function closeServer(server) {
  await new Promise((resolveClose, reject) => {
    server.close((error) => (error ? reject(error) : resolveClose()));
  });
}

async function cacheVerifiedAsset({ url, sha, destination }) {
  if (existsSync(destination)) {
    const cached = readFileSync(destination);
    if (sha256(cached) === sha) return { bytes: cached, downloaded: false };
    unlinkSync(destination);
  }
  mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
  const response = await fetch(url);
  if (!response.ok) {
    throw new ContractError(
      "ENGINE_RELEASE_FETCH_FAILED",
      `release fetch returned ${response.status}`,
    );
  }
  const bytes = Buffer.from(await response.arrayBuffer());
  const observedSha = sha256(bytes);
  if (observedSha !== sha) {
    throw new ContractError(
      "ENGINE_DIGEST_MISMATCH",
      `expected ${sha}, observed ${observedSha}`,
    );
  }
  const temporary = `${destination}.partial-${process.pid}`;
  writeFileSync(temporary, bytes, { mode: 0o600 });
  const descriptor = openSync(temporary, "r");
  fsyncSync(descriptor);
  closeSync(descriptor);
  renameSync(temporary, destination);
  return { bytes, downloaded: true };
}

function executeIdentity(binary) {
  const output = run(binary, []);
  return JSON.parse(output);
}

async function main() {
  const root = mkdtempSync(join(tmpdir(), "director-m1.12-distribution."));
  const sourceRoot = join(root, "engine-source");
  const releaseRoot = join(root, "release-assets");
  const pluginCheckout = join(root, "paseo-managed-plugin-checkout");
  const cacheHome = join(root, "xdg-cache");
  const badCacheHome = join(root, "bad-xdg-cache");
  let server;
  let result;

  try {
    mkdirSync(sourceRoot, { recursive: true, mode: 0o700 });
    write(
      join(sourceRoot, "go.mod"),
      `module example.invalid/director-engine\n\ngo 1.26\n\nrequire example.invalid/releasefixture v0.0.0\n\nreplace example.invalid/releasefixture => ./third_party/releasefixture\n`,
    );
    write(
      join(sourceRoot, "main.go"),
      `package main\n\nimport (\n\t\"encoding/json\"\n\t\"os\"\n\n\t\"example.invalid/releasefixture\"\n)\n\nvar buildMode = \"unset\"\nvar version = \"unset\"\nvar sourceCandidate = \"unset\"\nvar noticesSha = \"unset\"\n\nfunc main() {\n\t_ = json.NewEncoder(os.Stdout).Encode(map[string]string{\n\t\t\"mode\": buildMode,\n\t\t\"version\": version,\n\t\t\"sourceCandidate\": sourceCandidate,\n\t\t\"noticesSha256\": noticesSha,\n\t\t\"dependencyMarker\": releasefixture.Marker(),\n\t})\n}\n`,
    );
    write(
      join(sourceRoot, "third_party/releasefixture/go.mod"),
      "module example.invalid/releasefixture\n\ngo 1.26\n",
    );
    write(
      join(sourceRoot, "third_party/releasefixture/fixture.go"),
      `package releasefixture\n\nfunc Marker() string { return \"linked-fixture-dependency\" }\n`,
    );
    const dependencyLicense =
      "MIT License\n\nCopyright (c) 2000 Release Fixture\n\nPermission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files to deal in the Software without restriction.\n";
    write(
      join(sourceRoot, "third_party/releasefixture/LICENSE"),
      dependencyLicense,
    );
    const sourceCandidate = initializeRepository(sourceRoot);

    const notices = [
      "Director Engine third-party notices",
      `source-candidate: ${sourceCandidate}`,
      "module: example.invalid/releasefixture v0.0.0 (local fixture)",
      "license: MIT",
      "",
      dependencyLicense.trim(),
      "",
    ].join("\n");
    const noticesSha = sha256(notices);
    const releaseBinary = join(
      releaseRoot,
      VERSION,
      "director-engine-linux-amd64",
    );
    const releaseNotices = join(
      releaseRoot,
      VERSION,
      "THIRD_PARTY_NOTICES.txt",
    );
    buildEngine({
      sourceRoot,
      destination: releaseBinary,
      mode: "release",
      version: VERSION,
      sourceCandidate,
      noticesSha,
    });
    write(releaseNotices, notices, 0o400);
    const binarySha = sha256(readFileSync(releaseBinary));
    assert.equal(sha256(readFileSync(releaseNotices)), noticesSha);

    const pin = {
      schemaVersion: 1,
      version: VERSION,
      sourceCandidate,
      target: TARGET,
      binary: {
        asset: `/${VERSION}/director-engine-linux-amd64`,
        sha256: binarySha,
      },
      notices: {
        asset: `/${VERSION}/THIRD_PARTY_NOTICES.txt`,
        sha256: noticesSha,
      },
    };
    mkdirSync(pluginCheckout, { recursive: true, mode: 0o700 });
    write(
      join(pluginCheckout, "engine-release.json"),
      `${JSON.stringify(pin, null, 2)}\n`,
    );
    const installedCommit = initializeRepository(pluginCheckout);

    server = createServer((request, response) => {
      const requested = request.url ?? "";
      const asset = requested === pin.binary.asset
        ? releaseBinary
        : requested === pin.notices.asset
          ? releaseNotices
          : undefined;
      if (!asset) {
        response.writeHead(404).end();
        return;
      }
      response.writeHead(200, { "content-type": "application/octet-stream" });
      response.end(readFileSync(asset));
    });
    const releaseOrigin = await listen(server);

    let releaseCompilations = 0;
    async function launchRelease(selectedPin, selectedCacheHome) {
      const engineRoot = join(
        selectedCacheHome,
        "director",
        "engines",
        "release",
        selectedPin.version,
        selectedPin.target,
        selectedPin.binary.sha256,
      );
      if (isWithin(engineRoot, pluginCheckout)) {
        throw new ContractError(
          "ENGINE_CACHE_INSIDE_PLUGIN",
          "engine cache resolved inside the managed plugin checkout",
        );
      }
      const binary = join(engineRoot, "director-engine");
      const noticeFile = join(engineRoot, "THIRD_PARTY_NOTICES.txt");
      const binaryResult = await cacheVerifiedAsset({
        url: `${releaseOrigin}${selectedPin.binary.asset}`,
        sha: selectedPin.binary.sha256,
        destination: binary,
      });
      const noticeResult = await cacheVerifiedAsset({
        url: `${releaseOrigin}${selectedPin.notices.asset}`,
        sha: selectedPin.notices.sha256,
        destination: noticeFile,
      });
      chmodSync(binary, 0o500);
      const identity = executeIdentity(binary);
      return {
        binary,
        cacheOutsidePluginCheckout: !isWithin(binary, pluginCheckout),
        downloadedAssets:
          Number(binaryResult.downloaded) + Number(noticeResult.downloaded),
        identity,
      };
    }

    const release = await launchRelease(pin, cacheHome);
    assert.equal(release.identity.mode, "release");
    assert.equal(release.identity.version, VERSION);
    assert.equal(release.identity.sourceCandidate, sourceCandidate);
    assert.equal(release.identity.noticesSha256, noticesSha);
    assert.equal(sha256(readFileSync(release.binary)), binarySha);

    const badPin = structuredClone(pin);
    badPin.binary.sha256 = "0".repeat(64);
    let digestMismatch;
    try {
      await launchRelease(badPin, badCacheHome);
      throw new Error("digest mismatch fixture unexpectedly executed");
    } catch (error) {
      assert(error instanceof ContractError);
      assert.equal(error.code, "ENGINE_DIGEST_MISMATCH");
      digestMismatch = {
        code: error.code,
        executed: false,
        compilationFallbacks: releaseCompilations,
      };
    }

    const developmentBinary = join(
      cacheHome,
      "director",
      "engines",
      "development",
      sourceCandidate,
      TARGET,
      "director-engine",
    );
    releaseCompilations += 1;
    buildEngine({
      sourceRoot,
      destination: developmentBinary,
      mode: "development",
      version: "dev",
      sourceCandidate,
      noticesSha,
    });
    const developmentIdentity = executeIdentity(developmentBinary);
    assert.equal(developmentIdentity.mode, "development");
    assert.equal(developmentIdentity.version, "dev");

    let missingMode;
    try {
      const selectedMode = undefined;
      if (selectedMode !== "release" && selectedMode !== "development") {
        throw new ContractError(
          "ENGINE_MODE_REQUIRED",
          "engine mode must be explicitly release or development",
        );
      }
    } catch (error) {
      assert(error instanceof ContractError);
      missingMode = error.code;
    }

    result = {
      compatibility: {
        os: "linux",
        architecture: "amd64",
        go: run("go", ["version"]),
      },
      release: {
        sourceCandidate,
        installedCommit,
        version: VERSION,
        target: TARGET,
        binarySha256: binarySha,
        noticesSha256: noticesSha,
        cacheOutsidePluginCheckout: release.cacheOutsidePluginCheckout,
        downloadedAssets: release.downloadedAssets,
        identity: release.identity,
      },
      digestMismatch,
      development: {
        goToolchainIsDevelopmentOnly: true,
        compilations: releaseCompilations,
        binarySha256: sha256(readFileSync(developmentBinary)),
        identity: developmentIdentity,
      },
      explicitMode: {
        missingMode,
        releaseMismatchDidNotCompile: digestMismatch.compilationFallbacks === 0,
      },
    };
  } finally {
    if (server?.listening) await closeServer(server);
    rmSync(root, { recursive: true, force: true });
  }

  result.cleanup = { ownedRootAbsent: !existsSync(root) };
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
}

await main();
