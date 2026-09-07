#!/usr/bin/env node

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";

import { createPaseoClient } from "@getpaseo/client";

const command = process.argv[2];
const url = process.env.DIRECTOR_PASEO_URL;
const password = process.env.DIRECTOR_PASEO_PASSWORD;
if (!url || !password) throw new Error("Paseo URL and password are required");

const client = createPaseoClient({ url, password, clientId: `director-m1.12-${command}` });

async function waitForGitRuntime(workspaceId) {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const listed = await client.workspaces.list({ page: { limit: 100 } });
    const workspace = listed.entries.find((entry) => entry.id === workspaceId);
    if (workspace?.gitRuntime) return workspace;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`git runtime did not settle for ${workspaceId}`);
}

await client.connect();
try {
  if (command === "enable-plugins") {
    const result = await client.config.patch({ pluginsEnabled: true });
    console.log(JSON.stringify({ pluginsEnabled: result.config.pluginsEnabled }));
  } else if (command === "workspace-probe") {
    const source = process.env.DIRECTOR_PRODUCT_REPOSITORY;
    const adoptedPath = process.env.DIRECTOR_ENGINE_WORKTREE;
    const lifecycleMarker = process.env.DIRECTOR_LIFECYCLE_MARKER;
    if (!source || !adoptedPath || !lifecycleMarker) throw new Error("workspace probe paths are required");
    const lifecycle = readFileSync(`${source}/paseo.json`, "utf8");
    const lifecycleDigest = createHash("sha256").update(lifecycle).digest("hex");
    const before = await client.workspaces.list({ page: { limit: 100 } });
    const refusedWithoutApproval = lifecycle.includes('"setup"');
    assert.equal(refusedWithoutApproval, true);
    assert.equal(existsSync(lifecycleMarker), false);
    const afterRefusal = await client.workspaces.list({ page: { limit: 100 } });
    assert.equal(afterRefusal.entries.length, before.entries.length);

    const adopted = await client.workspaces.create({
      title: "Engine-created worktree adoption probe",
      source: { kind: "directory", path: adoptedPath },
      requestId: "director-m1.12-adopt",
    });
    const adoptedSnapshot = await adopted.refresh();
    assert(adoptedSnapshot);
    const adoptedListed = await waitForGitRuntime(adopted.id);
    assert.equal(adoptedListed.workspaceKind, "worktree");
    assert.equal(adoptedListed.gitRuntime?.isPaseoOwnedWorktree, false);
    await adopted.archive("director-m1.12-adopt-archive");
    assert.equal(existsSync(adoptedPath), true);

    const approvedDigest = lifecycleDigest;
    assert.equal(approvedDigest, lifecycleDigest);
    const managed = await client.workspaces.create({
      title: "Paseo-managed worktree creation probe",
      source: {
        kind: "worktree",
        cwd: source,
        action: "branch-off",
        refName: "main",
        branchName: "spike/dir-m1.12-managed",
        worktreeSlug: "dir-m1-12-managed",
      },
      requestId: "director-m1.12-managed",
    });
    const managedSnapshot = await managed.refresh();
    assert(managedSnapshot);
    const managedListed = await waitForGitRuntime(managed.id);
    assert.equal(managedListed.workspaceKind, "worktree");
    assert.equal(managedListed.gitRuntime?.isPaseoOwnedWorktree, true);
    assert.equal(existsSync(lifecycleMarker), true);
    const managedPath = managedSnapshot.workspaceDirectory;
    assert(managedPath);
    await managed.archive("director-m1.12-managed-archive");
    assert.equal(existsSync(managedPath), false);
    console.log(
      JSON.stringify({
        refusalBeforeSdkCall: true,
        lifecycleDigest,
        adoptedKind: adoptedListed.workspaceKind,
        adoptedPaseoOwned: false,
        adoptedDirectoryRemained: true,
        managedKind: managedListed.workspaceKind,
        managedPaseoOwned: true,
        setupRanOnlyAfterApproval: true,
        managedDirectoryRemoved: true,
      }),
    );
  } else if (command === "cleanup-workspaces") {
    const listed = await client.workspaces.list({ page: { limit: 100 } });
    const owned = listed.entries.filter((workspace) =>
      [
        "Engine-created worktree adoption probe",
        "Paseo-managed worktree creation probe",
      ].includes(workspace.title ?? ""),
    );
    for (const workspace of owned) {
      await client.workspaces.archive(workspace.id, `director-m1.12-cleanup-${workspace.id}`);
    }
    console.log(JSON.stringify({ archivedOwnedWorkspaces: owned.length }));
  } else if (command === "inspect-workspaces") {
    const listed = await client.workspaces.list({ page: { limit: 100 } });
    console.log(
      JSON.stringify(
        listed.entries
          .filter((workspace) => workspace.title?.includes("worktree") ?? false)
          .map((workspace) => ({
            id: workspace.id,
            title: workspace.title,
            workspaceKind: workspace.workspaceKind,
            workspaceDirectory: workspace.workspaceDirectory,
            gitRuntime: workspace.gitRuntime,
          })),
      ),
    );
  } else {
    throw new Error(`unknown command: ${command}`);
  }
} finally {
  await client.close();
}
