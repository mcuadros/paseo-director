#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { realpathSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ALLOWED_ADAPTER_DEPENDENCIES = [
  "filippo.io/edwards25519",
  "github.com/go-sql-driver/mysql",
];

function run(repositoryRoot) {
  const result = spawnSync(
    "go",
    [
      "-C",
      "engine",
      "list",
      "-buildvcs=false",
      "-deps",
      "-f",
      "{{if not .Standard}}{{.ImportPath}}{{end}}",
      "./...",
    ],
    { cwd: repositoryRoot, encoding: "utf8", env: process.env },
  );
  if (result.status !== 0) {
    console.error(result.stderr || result.error?.message || "go list failed");
    return 1;
  }
  const nonStandard = result.stdout.split("\n").filter(Boolean);
  const unexpected = nonStandard.filter(
    (path) =>
      !path.startsWith("github.com/mcuadros/director-engine") &&
      !ALLOWED_ADAPTER_DEPENDENCIES.some(
        (allowed) => path === allowed || path.startsWith(`${allowed}/`),
      ),
  );
  if (unexpected.length > 0) {
    console.error(
      `Standalone engine has external module dependencies: ${unexpected.join(", ")}`,
    );
    return 1;
  }
  if (nonStandard.some((path) => /(?:paseo|director-for-paseo)/i.test(path))) {
    console.error("Standalone engine imports the Paseo host package");
    return 1;
  }
  console.log(
    `Standalone engine dependency check passed for ${nonStandard.length} engine and pinned direct-Dolt adapter packages.`,
  );
  return 0;
}

const modulePath = realpathSync(fileURLToPath(import.meta.url));
const invokedPath = process.argv[1]
  ? realpathSync(resolve(process.argv[1]))
  : null;
if (invokedPath === modulePath) {
  process.exitCode = run(fileURLToPath(new URL("../../", import.meta.url)));
}
