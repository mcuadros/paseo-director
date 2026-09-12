// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { auditDependencyClosure } from "./dependency-audit.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "director-dependency-audit-"));
  for (const name of ["package.json", "package-lock.json", "paseo-plugin.json"]) {
    copyFileSync(join(repositoryRoot, name), join(root, name));
  }
  return root;
}

test("the committed registry closure and disabled lifecycle set are fully audited", () => {
  const result = auditDependencyClosure(repositoryRoot, { installed: true });
  assert.deepEqual(result.errors, []);
  assert.equal(result.packageCount, 319);
  assert.equal(result.productionPackageCount, 7);
  assert.equal(result.lifecycleScriptsExecuted, 0);
  assert.deepEqual(result.auditedInstallScriptPackages, ["node_modules/fsevents"]);
  assert.match(result.sha256, /^[0-9a-f]{64}$/u);
});

test("dependency audit rejects registry, integrity, graph, lifecycle, and manifest drift", () => {
  for (const mutate of [
    (lock, _manifest) => { lock.packages["node_modules/@getpaseo/client"].resolved = "https://example.invalid/client.tgz"; },
    (lock, _manifest) => { delete lock.packages["node_modules/@getpaseo/client"].integrity; },
    (lock, _manifest) => { lock.packages["node_modules/@getpaseo/client"].dependencies.missing = "1.0.0"; },
    (lock, _manifest) => { lock.packages["node_modules/@getpaseo/client"].hasInstallScript = true; },
    (lock, _manifest) => { lock.packages["node_modules/unreachable"] = { version: "1.0.0", resolved: "https://registry.npmjs.org/unreachable/-/unreachable-1.0.0.tgz", integrity: `sha512-${Buffer.alloc(64).toString("base64")}`, license: "MIT" }; },
    (_lock, manifest) => { manifest.build[0][1] = "install"; },
  ]) {
    const root = fixture();
    try {
      const lock = JSON.parse(readFileSync(join(root, "package-lock.json"), "utf8"));
      const manifest = JSON.parse(readFileSync(join(root, "paseo-plugin.json"), "utf8"));
      mutate(lock, manifest);
      writeFileSync(join(root, "package-lock.json"), `${JSON.stringify(lock)}\n`);
      writeFileSync(join(root, "paseo-plugin.json"), `${JSON.stringify(manifest)}\n`);
      assert.notDeepEqual(auditDependencyClosure(root).errors, []);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  }
  const root = fixture();
  try {
    const packageJSON = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
    packageJSON.scripts.preinstall = "node -e process.exit(7)";
    writeFileSync(join(root, "package.json"), `${JSON.stringify(packageJSON)}\n`);
    assert.ok(auditDependencyClosure(root).errors.includes("package.json lifecycle script preinstall is not permitted"));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("installed production packages cannot be replaced by symlinks", () => {
  const root = fixture();
  try {
    const source = join(repositoryRoot, "node_modules/@getpaseo/client");
    const destination = join(root, "node_modules/@getpaseo/client");
    mkdirSync(dirname(destination), { recursive: true });
    symlinkSync(source, destination);
    assert.ok(auditDependencyClosure(root, { installed: true }).errors.some((error) =>
      error.includes("not a real installed package directory")));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
