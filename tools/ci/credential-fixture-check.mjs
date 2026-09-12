#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { execFileSync } from "node:child_process";
import { lstatSync, readFileSync } from "node:fs";
import { dirname, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const maximumFindings = 64;

const forbiddenPatterns = [
  ["github-fine-grained-pat", /\bgithub_pat_[A-Za-z0-9_]{16,}/gu],
  ["github-legacy-token", /\bgh[pousr]_[A-Za-z0-9_]{16,}/gu],
  ["openai-api-key", /\bsk-(?:live_|test_|proj-)?[A-Za-z0-9_-]{16,}/gu],
  ["aws-access-key-id", /\b(?:AKIA|ASIA)[0-9A-Z]{16}\b/gu],
  ["gitlab-access-token", /\bglpat-[A-Za-z0-9_-]{16,}/gu],
  ["npm-access-token", /\bnpm_[A-Za-z0-9]{16,}/gu],
  ["slack-token", /\bxox[baprs]-[A-Za-z0-9-]{10,}/gu],
  ["google-api-key", /\bAIza[0-9A-Za-z_-]{20,}/gu],
  ["jwt", /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}/gu],
  ["private-key-header", /-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----/gu],
];

export function scanText(text) {
  const findings = [];
  for (const [id, pattern] of forbiddenPatterns) {
    pattern.lastIndex = 0;
    if (pattern.test(text)) findings.push(id);
  }
  return findings;
}

function candidatePaths(root) {
  const output = execFileSync(
    "git",
    ["ls-files", "-z", "--cached", "--others", "--exclude-standard"],
    { cwd: root, encoding: "utf8", maxBuffer: 16 * 1024 * 1024 },
  );
  return output.split("\0").filter(Boolean).sort();
}

export function verifyRepositoryBoundary(root = repositoryRoot) {
  const findings = [];
  for (const path of candidatePaths(root)) {
    const absolute = resolve(root, path);
    const confined = relative(root, absolute);
    if (confined === ".." || confined.startsWith(`..${sep}`)) {
      findings.push("repository-boundary: unsafe tracked path");
      break;
    }
    let metadata;
    let bytes;
    try {
      metadata = lstatSync(absolute);
      if (!metadata.isFile()) continue;
      bytes = readFileSync(absolute);
    } catch {
      findings.push(`${path}: unreadable candidate file`);
      if (findings.length >= maximumFindings) break;
      continue;
    }
    if (bytes.includes(0)) continue;
    for (const id of scanText(bytes.toString("utf8"))) {
      findings.push(`${path}: forbidden provider credential fixture (${id})`);
      if (findings.length >= maximumFindings) break;
    }
    if (findings.length >= maximumFindings) break;
  }
  return findings;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const findings = verifyRepositoryBoundary();
  if (findings.length > 0) {
    for (const finding of findings) process.stderr.write(`${finding}\n`);
    if (findings.length === maximumFindings) process.stderr.write("credential fixture finding limit reached\n");
    process.exitCode = 1;
  } else {
    process.stdout.write("tracked-source provider credential fixtures verified absent\n");
  }
}
