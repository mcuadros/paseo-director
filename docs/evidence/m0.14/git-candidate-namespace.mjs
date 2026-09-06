#!/usr/bin/env node

import { spawn } from "node:child_process";
import { access } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const commonDirectory = process.env.DIRECTOR_M014_COMMON_DIR;
const worktreeGitDirectory = process.env.DIRECTOR_M014_WORKTREE_GIT_DIR;
const agentScript = process.env.DIRECTOR_M014_GIT_AGENT_SCRIPT;
if (!commonDirectory || !worktreeGitDirectory || !agentScript) {
  throw new Error("Missing fixed Git namespace scope");
}

async function mount(args) {
  await new Promise((resolve, reject) => {
    const child = spawn("/usr/bin/mount", args, {
      env: { PATH: "/usr/bin:/bin", LANG: "C.UTF-8" },
      shell: false,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stderr = [];
    child.stderr.on("data", (chunk) => stderr.push(chunk));
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (code !== 0) {
        reject(
          new Error(
            `Mount failed (exit=${code ?? "null"}, signal=${signal ?? "none"}): ` +
              Buffer.concat(stderr).toString("utf8").trim().slice(-1_000),
          ),
        );
        return;
      }
      resolve();
    });
  });
}

await mount(["--make-rprivate", "/"]);
await mount(["--bind", commonDirectory, commonDirectory]);
await mount(["--bind", worktreeGitDirectory, worktreeGitDirectory]);
await mount(["-o", "remount,bind,ro", commonDirectory]);
await mount(["-o", "remount,bind,rw", worktreeGitDirectory]);
await access(commonDirectory, 4);
await import(pathToFileURL(agentScript).href);
