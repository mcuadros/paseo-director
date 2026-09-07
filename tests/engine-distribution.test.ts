// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  EMPTY_SHA256,
  EngineDistributionError,
  parseReleaseMetadata,
  resolveEngine,
} from "../server/engine-distribution.server.ts";
import {
  EngineSelectionError,
  selectEngine,
  type DevelopmentEngineSelection,
  type ReleaseEngineSelection,
} from "../server/engine-selection.server.ts";

function digest(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("hex");
}

function releaseFixture() {
  const root = mkdtempSync(join(tmpdir(), "director-release-test-"));
  const checkoutRoot = join(root, "checkout");
  const cacheRoot = join(root, "cache");
  const metadataPath = join(checkoutRoot, "release.json");
  const binary = Buffer.from("fake-director-engine");
  const notices = Buffer.from("fake third-party notices\n");
  mkdirSync(checkoutRoot);
  writeFileSync(
    metadataPath,
    `${JSON.stringify({
      schemaVersion: 1,
      state: "published",
      version: "1.2.3",
      target: "linux-amd64",
      binary: {
        url: "https://github.com/mcuadros/paseo-director/releases/download/v1.2.3/director-engine-linux-amd64",
        sha256: digest(binary),
      },
      notices: {
        url: "https://github.com/mcuadros/paseo-director/releases/download/v1.2.3/THIRD_PARTY_NOTICES.txt",
        sha256: digest(notices),
      },
    })}\n`,
  );
  const selection: ReleaseEngineSelection = {
    mode: "release",
    checkoutRoot,
    cacheRoot,
    metadataPath,
  };
  return { root, selection, binary, notices };
}

test("release mode verifies and externally caches only pinned release assets", async () => {
  const fixture = releaseFixture();
  let fetches = 0;
  let compiles = 0;
  try {
    const fetchAsset = async (url: string) => {
      fetches += 1;
      return url.endsWith("THIRD_PARTY_NOTICES.txt")
        ? fixture.notices
        : fixture.binary;
    };
    const first = await resolveEngine(fixture.selection, {
      fetchAsset,
      compile() {
        compiles += 1;
      },
    });
    assert.equal(first.mode, "release");
    assert.equal(fetches, 2);
    assert.equal(compiles, 0);
    assert.equal(existsSync(first.binaryPath), true);
    assert.deepEqual(readFileSync(first.binaryPath), fixture.binary);
    assert.ok(first.noticesPath);
    assert.deepEqual(readFileSync(first.noticesPath), fixture.notices);
    assert.equal(first.binaryPath.startsWith(fixture.selection.cacheRoot), true);
    assert.equal(first.binaryPath.startsWith(fixture.selection.checkoutRoot), false);

    await resolveEngine(fixture.selection, { fetchAsset });
    assert.equal(fetches, 2, "verified cache should avoid a second download");
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("the committed scaffold release is unpublished and fails before fetch", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-unpublished-test-"));
  let fetches = 0;
  let compiles = 0;
  try {
    await assert.rejects(
      resolveEngine(
        {
          mode: "release",
          checkoutRoot: process.cwd(),
          cacheRoot: join(root, "cache"),
          metadataPath: join(process.cwd(), "release", "engine.json"),
        },
        {
          async fetchAsset() {
            fetches += 1;
            return new Uint8Array();
          },
          compile() {
            compiles += 1;
          },
        },
      ),
      (error: unknown) =>
        error instanceof EngineDistributionError &&
        error.code === "ENGINE_RELEASE_UNPUBLISHED",
    );
    assert.equal(fetches, 0);
    assert.equal(compiles, 0);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("published metadata rejects the empty-input digest and dot path segments", () => {
  const valid = {
    schemaVersion: 1,
    state: "published",
    version: "1.2.3",
    target: "linux-amd64",
    binary: {
      url: "https://github.com/mcuadros/paseo-director/releases/download/v1.2.3/director-engine-linux-amd64",
      sha256: "1".repeat(64),
    },
    notices: {
      url: "https://github.com/mcuadros/paseo-director/releases/download/v1.2.3/THIRD_PARTY_NOTICES.txt",
      sha256: "2".repeat(64),
    },
  };
  for (const metadata of [
    { ...valid, binary: { ...valid.binary, sha256: EMPTY_SHA256 } },
    { ...valid, notices: { ...valid.notices, sha256: EMPTY_SHA256 } },
    { ...valid, version: "." },
    { ...valid, version: ".." },
  ]) {
    assert.throws(
      () => parseReleaseMetadata(Buffer.from(JSON.stringify(metadata))),
      (error: unknown) =>
        error instanceof EngineDistributionError &&
        error.code === "ENGINE_RELEASE_METADATA",
    );
  }
});

test("release digest failure never falls back to compilation", async () => {
  const fixture = releaseFixture();
  let compiles = 0;
  try {
    await assert.rejects(
      resolveEngine(fixture.selection, {
        async fetchAsset() {
          return Buffer.from("wrong");
        },
        compile() {
          compiles += 1;
        },
      }),
      (error: unknown) =>
        error instanceof EngineDistributionError &&
        error.code === "ENGINE_RELEASE_DIGEST",
    );
    assert.equal(compiles, 0);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("development mode compiles and never attempts a release download", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-development-test-"));
  const checkoutRoot = join(root, "checkout");
  const sourceRoot = join(root, "source");
  const cacheRoot = join(root, "cache");
  mkdirSync(checkoutRoot);
  mkdirSync(sourceRoot);
  const selection: DevelopmentEngineSelection = {
    mode: "development",
    checkoutRoot,
    sourceRoot,
    cacheRoot,
  };
  let compiles = 0;
  let downloads = 0;
  try {
    const result = await resolveEngine(selection, {
      compile(_selected, destination) {
        compiles += 1;
        mkdirSync(dirname(destination), { recursive: true });
        writeFileSync(destination, "compiled");
      },
      async fetchAsset() {
        downloads += 1;
        return new Uint8Array();
      },
    });
    assert.equal(result.mode, "development");
    assert.equal(compiles, 1);
    assert.equal(downloads, 0);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("engine mode selection is explicit and disjoint", () => {
  const root = mkdtempSync(join(tmpdir(), "director-mode-test-"));
  const checkoutRoot = join(root, "checkout");
  mkdirSync(checkoutRoot);
  try {
    assert.throws(
      () => selectEngine({ XDG_CACHE_HOME: join(root, "cache") }, checkoutRoot),
      (error: unknown) =>
        error instanceof EngineSelectionError &&
        error.code === "ENGINE_MODE_REQUIRED",
    );
    assert.throws(
      () =>
        selectEngine(
          {
            DIRECTOR_ENGINE_MODE: "release",
            DIRECTOR_ENGINE_SOURCE_ROOT: join(root, "source"),
            XDG_CACHE_HOME: join(root, "cache"),
          },
          checkoutRoot,
        ),
      (error: unknown) =>
        error instanceof EngineSelectionError &&
        error.code === "ENGINE_MODE_CONFLICT",
    );
    assert.equal(
      selectEngine(
        {
          DIRECTOR_ENGINE_MODE: "development",
          DIRECTOR_ENGINE_SOURCE_ROOT: join(root, "source"),
          XDG_CACHE_HOME: join(root, "cache"),
        },
        checkoutRoot,
      ).mode,
      "development",
    );
    const cacheLink = join(root, "cache-link");
    symlinkSync(checkoutRoot, cacheLink);
    assert.throws(
      () =>
        selectEngine(
          {
            DIRECTOR_ENGINE_MODE: "release",
            XDG_CACHE_HOME: cacheLink,
          },
          checkoutRoot,
        ),
      (error: unknown) =>
        error instanceof EngineSelectionError &&
        error.code === "ENGINE_CACHE_IN_CHECKOUT",
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
