// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { buildRelease, verifyReleaseNotices } from "./build-engine.mjs";
import { NoticesError, renderThirdPartyNotices } from "./notices.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

function run(command, args, cwd) {
  const result = spawnSync(command, args, { cwd, encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "director-release-builder-"));
  const source = join(root, "source");
  mkdirSync(join(source, "cmd/director-engine"), { recursive: true });
  writeFileSync(join(source, "go.mod"), "module example.invalid/director-release-fixture\n\ngo 1.26.5\n");
  writeFileSync(join(source, "cmd/director-engine/main.go"), `package main
import ("crypto/sha256"; "encoding/hex"; "encoding/json"; "os")
var version="dev"
var buildMode="development"
var sourceCandidate="uncommitted"
var noticesSha=""
func main(){ path,_:=os.Executable(); bytes,_:=os.ReadFile(path); executable:=sha256.Sum256(bytes); sum:=sha256.Sum256([]byte("contract")); _=json.NewEncoder(os.Stdout).Encode(map[string]any{"name":"director-engine","version":version,"buildMode":buildMode,"sourceCandidate":sourceCandidate,"target":"linux-amd64","executableSha256":hex.EncodeToString(executable[:]),"noticesSha256":noticesSha,"contractVersion":"fixture/v1","contractSha256":hex.EncodeToString(sum[:]),"productBehavior":true}) }
`);
  mkdirSync(join(source, "cmd/director-bootstrap"), { recursive: true });
  writeFileSync(join(source, "cmd/director-bootstrap/main.go"), `package main
import ("encoding/json"; "os"; "runtime")
var bootstrapVersion="dev"
var bootstrapMode="development"
var bootstrapCandidate="uncommitted"
func main(){ _=json.NewEncoder(os.Stdout).Encode(map[string]any{"name":"director-bootstrap","version":bootstrapVersion,"buildMode":bootstrapMode,"sourceCandidate":bootstrapCandidate,"target":runtime.GOOS+"-"+runtime.GOARCH}) }
`);
  run("git", ["init", "-b", "main"], source);
  run("git", ["config", "user.name", "Release Fixture"], source);
  run("git", ["config", "user.email", "release@example.invalid"], source);
  run("git", ["add", "go.mod", "cmd/director-engine/main.go", "cmd/director-bootstrap/main.go"], source);
  run("git", ["commit", "-m", "fixture source"], source);
  const candidate = run("git", ["rev-parse", "HEAD"], source);
  const notices = join(root, "THIRD_PARTY_NOTICES.txt");
  writeFileSync(notices, `Director release fixture\nsource-candidate: ${candidate}\n`);
  const doltArchive = join(root, "dolt-linux-amd64.tar.gz");
  const doltExecutable = join(root, "dolt");
  writeFileSync(doltArchive, "verified Dolt archive fixture");
  writeFileSync(doltExecutable, "#!/bin/sh\necho 'dolt version 2.3.2'\n", { mode: 0o500 });
  chmodSync(doltExecutable, 0o500);
  return { root, source, candidate, notices, doltArchive, doltExecutable, output: join(root, "release", "1.2.3") };
}

function buildArguments(value) {
  return [
    "--source", value.source,
    "--candidate", value.candidate,
    "--version", "1.2.3",
    "--notices", value.notices,
    "--dolt-version", "2.3.2",
    "--dolt-archive", value.doltArchive,
    "--dolt-executable", value.doltExecutable,
    "--output", value.output,
  ];
}

function build(value, overrides = {}) {
  return buildRelease(buildArguments(value), {
    verifyNotices(_repositoryRoot, candidate, bytes) {
      assert.match(bytes.toString("utf8"), new RegExp(`^source-candidate: ${candidate}$`, "mu"));
    },
    verifyBinaryModules() {
      return { moduleCount: 0, toolchain: "go1.26.5" };
    },
    ...overrides,
  });
}

test("release builder emits one verified static asset closure from an exact Candidate", () => {
  const value = fixture();
  try {
    const verifiedKinds = [];
    const result = build(value, {
      verifyBinaryModules(_repositoryRoot, _binaryPath, options) {
        verifiedKinds.push(options.kind);
      },
    });
    assert.deepEqual(verifiedKinds, ["engine", "bootstrap"]);
    assert.equal(result.metadata.sourceCandidate, value.candidate);
    assert.equal(result.identity.sourceCandidate, value.candidate);
    assert.equal(result.identity.noticesSha256, result.metadata.notices.sha256);
    assert.equal(result.metadata.dolt.version, "2.3.2");
    assert.equal(result.metadata.dolt.archive.name, "dolt-linux-amd64.tar.gz");
    assert.match(result.metadata.dolt.archive.sha256, /^[0-9a-f]{64}$/u);
    assert.match(result.metadata.dolt.executableSha256, /^[0-9a-f]{64}$/u);
    assert.equal(result.bootstrapMetadata.sourceCandidate, value.candidate);
    assert.equal(result.bootstrapIdentity.buildMode, "release");
    assert.match(result.bootstrapMetadata.binary.sha256, /^[0-9a-f]{64}$/u);
    assert.equal(result.metadata.binary.size, readFileSync(join(value.output, "director-engine-linux-amd64")).length);
    assert.equal(result.metadata.notices.size, readFileSync(join(value.output, "THIRD_PARTY_NOTICES.txt")).length);
    assert.equal(result.metadata.dolt.archive.size, readFileSync(value.doltArchive).length);
    assert.equal(result.metadata.dolt.executableSize, readFileSync(value.doltExecutable).length);
    assert.equal(result.bootstrapMetadata.binary.size, readFileSync(join(value.output, "director-bootstrap-linux-amd64")).length);
    assert.deepEqual(JSON.parse(readFileSync(join(value.output, "engine.json"), "utf8")), result.metadata);
    const replay = build(value);
    assert.deepEqual(replay.metadata, result.metadata);
    chmodSync(join(value.output, "THIRD_PARTY_NOTICES.txt"), 0o600);
    writeFileSync(join(value.output, "THIRD_PARTY_NOTICES.txt"), "poisoned\n");
    assert.throws(() => build(value), (error) => error.code === "RELEASE_OUTPUT_POISONED");
  } finally {
    rmSync(value.root, { recursive: true, force: true });
  }
});

test("release builder fails closed on source or notices identity without partial output", () => {
  const value = fixture();
  try {
    writeFileSync(value.notices, "wrong source\n");
    assert.throws(() => buildRelease(buildArguments(value), {
      verifyNotices() {},
      verifyBinaryModules() {},
    }), (error) => error.code === "RELEASE_NOTICES_IDENTITY");
    const wrongCandidate = buildArguments(value);
    wrongCandidate[wrongCandidate.indexOf("--candidate") + 1] = "9".repeat(40);
    assert.throws(() => buildRelease(wrongCandidate, {
      verifyNotices() {},
      verifyBinaryModules() {},
    }), (error) => error.code === "RELEASE_SOURCE_IDENTITY");
  } finally {
    rmSync(value.root, { recursive: true, force: true });
  }
});

test("release builder rejects a stale generated notices inventory before building", () => {
  const value = fixture();
  try {
    assert.throws(() => build(value, {
      verifyNotices() {
        const error = new Error("third-party notices are stale");
        error.code = "NOTICES_STALE";
        throw error;
      },
    }), (error) => error.code === "NOTICES_STALE");
  } finally {
    rmSync(value.root, { recursive: true, force: true });
  }
});

test("release builder's default notice gate exercises the real exact-source verifier", () => {
  const candidate = "7".repeat(40);
  const notices = renderThirdPartyNotices(repositoryRoot, candidate);
  assert.doesNotThrow(() => verifyReleaseNotices(repositoryRoot, candidate, notices));
  const stale = Buffer.from(notices.toString("utf8").replace("@getpaseo/client", "@getpaseo/client-stale"));
  assert.throws(
    () => verifyReleaseNotices(repositoryRoot, candidate, stale),
    (error) => error instanceof NoticesError && error.code === "NOTICES_STALE",
  );
});
