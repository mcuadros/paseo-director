// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

import { buildRelease } from "./build-engine.mjs";

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
  run("git", ["init", "-b", "main"], source);
  run("git", ["config", "user.name", "Release Fixture"], source);
  run("git", ["config", "user.email", "release@example.invalid"], source);
  run("git", ["add", "go.mod", "cmd/director-engine/main.go"], source);
  run("git", ["commit", "-m", "fixture source"], source);
  const candidate = run("git", ["rev-parse", "HEAD"], source);
  const notices = join(root, "THIRD_PARTY_NOTICES.txt");
  writeFileSync(notices, `Director release fixture\nsource-candidate: ${candidate}\n`);
  return { root, source, candidate, notices, output: join(root, "release", "1.2.3") };
}

test("release builder emits one verified static asset closure from an exact Candidate", () => {
  const value = fixture();
  try {
    const result = buildRelease(["--source", value.source, "--candidate", value.candidate, "--version", "1.2.3", "--notices", value.notices, "--output", value.output]);
    assert.equal(result.metadata.sourceCandidate, value.candidate);
    assert.equal(result.identity.sourceCandidate, value.candidate);
    assert.equal(result.identity.noticesSha256, result.metadata.notices.sha256);
    assert.deepEqual(JSON.parse(readFileSync(join(value.output, "engine.json"), "utf8")), result.metadata);
    const replay = buildRelease(["--source", value.source, "--candidate", value.candidate, "--version", "1.2.3", "--notices", value.notices, "--output", value.output]);
    assert.deepEqual(replay.metadata, result.metadata);
    chmodSync(join(value.output, "THIRD_PARTY_NOTICES.txt"), 0o600);
    writeFileSync(join(value.output, "THIRD_PARTY_NOTICES.txt"), "poisoned\n");
    assert.throws(() => buildRelease(["--source", value.source, "--candidate", value.candidate, "--version", "1.2.3", "--notices", value.notices, "--output", value.output]), (error) => error.code === "RELEASE_OUTPUT_POISONED");
  } finally {
    rmSync(value.root, { recursive: true, force: true });
  }
});

test("release builder fails closed on source or notices identity without partial output", () => {
  const value = fixture();
  try {
    writeFileSync(value.notices, "wrong source\n");
    assert.throws(() => buildRelease(["--source", value.source, "--candidate", value.candidate, "--version", "1.2.3", "--notices", value.notices, "--output", value.output]), (error) => error.code === "RELEASE_NOTICES_IDENTITY");
    assert.throws(() => buildRelease(["--source", value.source, "--candidate", "9".repeat(40), "--version", "1.2.3", "--notices", value.notices, "--output", value.output]), (error) => error.code === "RELEASE_SOURCE_IDENTITY");
  } finally {
    rmSync(value.root, { recursive: true, force: true });
  }
});
