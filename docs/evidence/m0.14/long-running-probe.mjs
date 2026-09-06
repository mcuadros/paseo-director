#!/usr/bin/env node

import { spawn } from "node:child_process";
import { writeFile } from "node:fs/promises";

const readyPath = process.env.DIRECTOR_M014_READY_PATH;
if (!readyPath) throw new Error("Missing owned readiness path");

const child = spawn("/bin/sleep", ["3600"], { shell: false, stdio: "ignore" });
await new Promise((resolve, reject) => {
  child.once("spawn", resolve);
  child.once("error", reject);
});
await writeFile(readyPath, "ready\n", { encoding: "utf8", mode: 0o600 });

for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(signal, () => {
    child.kill(signal);
  });
}

await new Promise((resolve) => child.once("close", resolve));
