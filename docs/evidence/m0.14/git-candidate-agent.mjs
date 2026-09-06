#!/usr/bin/env node

import { spawn } from "node:child_process";
import { writeFile } from "node:fs/promises";
import path from "node:path";

const required = [
  "DIRECTOR_M014_WORKTREE",
  "DIRECTOR_M014_PRIVATE_OBJECTS",
  "DIRECTOR_M014_SHARED_OBJECTS",
  "DIRECTOR_M014_HOOKS",
  "DIRECTOR_M014_BASE",
  "DIRECTOR_M014_NONCE",
  "DIRECTOR_M014_SHARED_WRITE_PROBE",
];
for (const name of required) {
  if (!process.env[name]) throw new Error(`Missing ${name}`);
}

const worktree = process.env.DIRECTOR_M014_WORKTREE;
const expectedBase = process.env.DIRECTOR_M014_BASE;
const nonce = process.env.DIRECTOR_M014_NONCE;
const sharedWriteProbe = process.env.DIRECTOR_M014_SHARED_WRITE_PROBE;

function runGit(args, { acceptedCodes = [0] } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(
      "/usr/bin/git",
      [
        "-c",
        `core.hooksPath=${process.env.DIRECTOR_M014_HOOKS}`,
        "-c",
        "core.fsmonitor=false",
        ...args,
      ],
      {
        cwd: worktree,
        env: {
          PATH: "/usr/bin:/bin",
          HOME: process.env.HOME,
          LANG: "C.UTF-8",
          GIT_CONFIG_NOSYSTEM: "1",
          GIT_CONFIG_GLOBAL: "/dev/null",
          GIT_TERMINAL_PROMPT: "0",
          GIT_OBJECT_DIRECTORY: process.env.DIRECTOR_M014_PRIVATE_OBJECTS,
          GIT_ALTERNATE_OBJECT_DIRECTORIES: process.env.DIRECTOR_M014_SHARED_OBJECTS,
        },
        shell: false,
        stdio: ["ignore", "pipe", "pipe"],
      },
    );
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => stdout.push(chunk));
    child.stderr.on("data", (chunk) => stderr.push(chunk));
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (!acceptedCodes.includes(code)) {
        reject(
          new Error(
            `Git command failed (exit=${code ?? "null"}, signal=${signal ?? "none"}): ` +
              Buffer.concat(stderr).toString("utf8").trim().slice(-1_000),
          ),
        );
        return;
      }
      resolve({ code, stdout: Buffer.concat(stdout).toString("utf8") });
    });
  });
}

const gitVersion = (await runGit(["--version"])).stdout.trim();
await writeFile(path.join(worktree, "candidate.txt"), `candidate ${nonce}\n`, {
  encoding: "utf8",
  mode: 0o600,
});
await runGit(["add", "--", "candidate.txt"]);
await runGit([
  "-c",
  "commit.gpgsign=false",
  "-c",
  "user.name=Director M0.14 fixture",
  "-c",
  "user.email=director-m0.14.invalid",
  "commit",
  "-m",
  "test: produce isolated candidate",
]);

const candidate = (await runGit(["rev-parse", "HEAD"])).stdout.trim();
const parent = (await runGit(["rev-parse", "HEAD^"])).stdout.trim();
if (parent !== expectedBase) throw new Error("Candidate parent changed");
await runGit(["update-ref", "refs/worktree/director-candidate", candidate]);
const worktreeRef = (
  await runGit(["rev-parse", "refs/worktree/director-candidate"])
).stdout.trim();
if (worktreeRef !== candidate) throw new Error("Per-worktree Candidate ref changed");
await runGit(["fsck", "--strict", "--no-reflogs", candidate]);
const status = (await runGit(["status", "--porcelain=v1"])).stdout.trim();
if (status !== "") throw new Error("Candidate worktree is not clean");

let sharedWriteDenied = false;
try {
  await writeFile(sharedWriteProbe, `${candidate}\n`, { encoding: "utf8", mode: 0o600 });
} catch (cause) {
  if (cause && typeof cause === "object" && ["EACCES", "EROFS"].includes(cause.code)) {
    sharedWriteDenied = true;
  } else {
    throw cause;
  }
}
if (!sharedWriteDenied) throw new Error("Shared common directory accepted a direct write");

process.stdout.write(
  `${JSON.stringify({
    gitVersion,
    candidate,
    parent,
    worktreeRef,
    sharedWriteDenied,
    worktreeClean: true,
  })}\n`,
);
