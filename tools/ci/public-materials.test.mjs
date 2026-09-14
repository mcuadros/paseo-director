// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { publicMaterialErrors, publicMaterialSummary } from "./public-materials.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

function source(path) {
  return readFileSync(resolve(repositoryRoot, path), "utf8");
}

test("public license, security, support, compatibility, changelog, and release paths are complete", () => {
  assert.deepEqual(publicMaterialErrors(repositoryRoot), []);
  assert.deepEqual(publicMaterialSummary(repositoryRoot), {
    schemaVersion: 1,
    publicFiles: 10,
    sha256: publicMaterialSummary(repositoryRoot).sha256,
  });
  assert.match(publicMaterialSummary(repositoryRoot).sha256, /^[0-9a-f]{64}$/u);
});

test("security reporting cannot fall back to a public issue or lose safe-evidence guidance", () => {
  const unsafe = source("SECURITY.md")
    .replace("https://github.com/mcuadros/paseo-director/security/advisories/new", "https://github.com/mcuadros/paseo-director/issues/new")
    .replace("Do not include credentials", "Include credentials");
  const errors = publicMaterialErrors(repositoryRoot, { "SECURITY.md": unsafe });
  assert.ok(errors.some((error) => error.includes("SECURITY.md")));
});

test("support and compatibility mutations fail closed", () => {
  const support = source("SUPPORT.md")
    .replace("exact stable Paseo", "any compatible Paseo")
    .replace("with glibc", "with any libc");
  assert.ok(publicMaterialErrors(repositoryRoot, { "SUPPORT.md": support }).some((error) => error.includes("SUPPORT.md")));
  const compatibility = source("docs/compatibility.md")
    .replace("Exact stable `0.7.2`", "Any `0.7.x`")
    .replace("Linux amd64 with glibc", "Linux amd64");
  assert.ok(publicMaterialErrors(repositoryRoot, { "docs/compatibility.md": compatibility }).some((error) => error.includes("compatibility")));
});

test("release material cannot omit sole CI, independent Review, or manual gates", () => {
  const release = source("docs/release-process.md")
    .replace("one authoritative complete remote Linux CI", "a validation run")
    .replace("independent top-level Reviewer", "author review")
    .replace("M6.6 owner manual hold", "automatic beta promotion");
  const errors = publicMaterialErrors(repositoryRoot, { "docs/release-process.md": release });
  assert.ok(errors.filter((error) => error.includes("release-process")).length >= 3);
});

test("main compilation and tagged precompiled channels cannot collapse into a fallback", () => {
  const support = source("SUPPORT.md")
    .replace("Default `main` is a named development channel", "Every channel is precompiled")
    .replace("Reload never compiles", "Reload compiles when needed");
  assert.ok(publicMaterialErrors(repositoryRoot, { "SUPPORT.md": support }).some((error) => error.includes("SUPPORT.md")));

  const compatibility = source("docs/compatibility.md")
    .replace("Tagged alpha, beta, and stable releases", "Tagged releases may use source")
    .replace("release bootstrap", "release runtime");
  assert.ok(publicMaterialErrors(repositoryRoot, { "docs/compatibility.md": compatibility }).some((error) => error.includes("compatibility")));

  const release = source("docs/release-process.md")
    .replace("1.0.0-alpha.1", "first alpha")
    .replace("reload never compiles", "reload may compile");
  assert.ok(publicMaterialErrors(repositoryRoot, { "docs/release-process.md": release }).some((error) => error.includes("release-process")));
});

test("public material rejects restart instructions, private paths, credentials, and runtime identities", () => {
  for (const injected of [
    "Restart the Paseo daemon to activate Director.",
    "Diagnostic path: /home/operator/private/project/state.json",
    "Diagnostic path: /tmp/director-private/state.json",
    "Diagnostic path: C:\\Users\\operator\\private\\state.json",
    "Authorization: Bearer example-secret-value",
    "password=example-secret-value",
    "Workspace wks_private123",
    "Actor paseo:12345678-1234-1234-1234-123456789abc",
  ]) {
    const support = `${source("SUPPORT.md")}\n${injected}\n`;
    assert.ok(publicMaterialErrors(repositoryRoot, { "SUPPORT.md": support }).some((error) => error.includes("SUPPORT.md")), injected);
  }
});

test("broken local links and invented stable changelog entries are rejected", () => {
  const readme = source("README.md").replace("docs/licensing.md", "docs/missing-license-policy.md");
  assert.ok(publicMaterialErrors(repositoryRoot, { "README.md": readme }).some((error) => error.includes("local link")));
  const changelog = `${source("CHANGELOG.md")}\n## [1.0.0] - 2026-09-12\n`;
  assert.ok(publicMaterialErrors(repositoryRoot, { "CHANGELOG.md": changelog }).some((error) => error.includes("released version")));
});

test("canonical Dolt metadata and the retired activation-real gate cannot drift", () => {
  const dolt = JSON.parse(readFileSync(resolve(repositoryRoot, "release/dolt-linux-amd64.json"), "utf8"));
  dolt.archive.sha256 = "0".repeat(64);
  assert.ok(publicMaterialErrors(repositoryRoot, {
    "release/dolt-linux-amd64.json": JSON.stringify(dolt),
  }).some((error) => error.includes("canonical Dolt")));

  const packageJSON = JSON.parse(readFileSync(resolve(repositoryRoot, "package.json"), "utf8"));
  packageJSON.scripts["test:activation:real"] = "node obsolete.mjs";
  assert.ok(publicMaterialErrors(repositoryRoot, {
    "package.json": JSON.stringify(packageJSON),
  }).some((error) => error.includes("retired activation-real")));
});
