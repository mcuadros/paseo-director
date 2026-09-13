// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { gzipSync } from "node:zlib";

import {
  DoltDistributionError,
  extractDoltExecutable,
  resolveDolt,
} from "../connector/dolt-distribution.server.ts";

function digest(bytes: Uint8Array | string): string {
  return createHash("sha256").update(bytes).digest("hex");
}

function tarEntry(path: string, bytes: Uint8Array, type = "0"): Buffer {
  const header = Buffer.alloc(512);
  header.write(path, 0, 100, "utf8");
  header.write("0000755\0", 100, 8, "ascii");
  header.write("0000000\0", 108, 8, "ascii");
  header.write("0000000\0", 116, 8, "ascii");
  header.write(`${bytes.byteLength.toString(8).padStart(11, "0")}\0`, 124, 12, "ascii");
  header.write("00000000000\0", 136, 12, "ascii");
  header.fill(0x20, 148, 156);
  header.write(type, 156, 1, "ascii");
  header.write("ustar\0", 257, 6, "ascii");
  const checksum = [...header].reduce((sum, byte) => sum + byte, 0);
  header.write(`${checksum.toString(8).padStart(6, "0")}\0 `, 148, 8, "ascii");
  const padding = Buffer.alloc(Math.ceil(bytes.byteLength / 512) * 512 - bytes.byteLength);
  return Buffer.concat([header, bytes, padding]);
}

function archive(path: string, bytes: Uint8Array, type = "0"): Buffer {
  return gzipSync(Buffer.concat([tarEntry(path, bytes, type), Buffer.alloc(1024)]));
}

test("Dolt release archive is verified, extracted, and atomically adopted", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-dolt-release-"));
  const binary = Buffer.from("fake-dolt-binary");
  const compressed = archive("dolt-linux-amd64/bin/dolt", binary);
  const metadata = {
    version: "2.3.2",
    archive: {
      name: "dolt-linux-amd64.tar.gz",
      url: "https://github.com/dolthub/dolt/releases/download/v2.3.2/dolt-linux-amd64.tar.gz",
      sha256: digest(compressed),
      size: compressed.byteLength,
    },
    executableSha256: digest(binary),
    executableSize: binary.byteLength,
  };
  let fetches = 0;
  try {
    const dependencies = {
      async fetchAsset() { fetches += 1; return compressed; },
      inspectBinary() { return "2.3.2"; },
    };
    const first = await resolveDolt(metadata, root, dependencies);
    const replay = await resolveDolt(metadata, root, dependencies);
    assert.equal(first.binaryPath, replay.binaryPath);
    assert.equal(first.binarySha256, digest(binary));
    assert.deepEqual(readFileSync(first.binaryPath), binary);
    assert.equal(fetches, 1);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("Dolt archive rejects traversal, links, ambiguity, and digest drift", async () => {
  const binary = Buffer.from("fake-dolt-binary");
  for (const [name, compressed] of [
    ["traversal", archive("../bin/dolt", binary)],
    ["link", archive("dolt-linux-amd64/bin/dolt", binary, "2")],
    ["ambiguous", gzipSync(Buffer.concat([
      tarEntry("one/bin/dolt", binary),
      tarEntry("two/bin/dolt", binary),
      Buffer.alloc(1024),
    ]))],
  ] as const) {
    assert.throws(
      () => extractDoltExecutable(compressed),
      (error: unknown) => error instanceof DoltDistributionError && error.code === "DOLT_RELEASE_ARCHIVE",
      name,
    );
  }

  const root = mkdtempSync(join(tmpdir(), "director-dolt-digest-"));
  const compressed = archive("dolt-linux-amd64/bin/dolt", binary);
  try {
    await assert.rejects(resolveDolt({
      version: "2.3.2",
      archive: {
        name: "dolt-linux-amd64.tar.gz",
        url: "https://github.com/dolthub/dolt/releases/download/v2.3.2/dolt-linux-amd64.tar.gz",
        sha256: "1".repeat(64),
        size: compressed.byteLength,
      },
      executableSha256: digest(binary),
      executableSize: binary.byteLength,
    }, root, {
      async fetchAsset() { return compressed; },
      inspectBinary() { return "2.3.2"; },
    }), (error: unknown) => error instanceof DoltDistributionError && error.code === "DOLT_RELEASE_DIGEST");
    assert.equal(existsSync(join(root, "dolt", "2.3.2", "linux-amd64", digest(binary))), false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
