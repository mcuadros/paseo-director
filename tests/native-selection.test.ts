// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  NativeSelectionError,
  resolveSelectedPaseoDefaults,
} from "../connector/native-selection.server.ts";

test("native Project and workspace facts resolve without duplicated operator JSON", () => {
  const root = mkdtempSync(join(tmpdir(), "director-native-selection-"));
  const workspaceDirectory = join(root, "worktree");
  mkdirSync(workspaceDirectory);
  try {
    const resolved = resolveSelectedPaseoDefaults(
      {
        projectId: "project-1",
        projectDisplayName: "Repository",
        projectCustomName: "Selected Project",
        projectRootPath: root,
      },
      {
        id: "workspace-1",
        projectId: "project-1",
        projectRootPath: root,
        workspaceDirectory,
        name: "worktree",
        title: "Selected Workspace",
        archivingAt: null,
      },
    );
    assert.deepEqual(resolved, {
      projectId: "project-1",
      projectName: "Selected Project",
      workspaceId: "workspace-1",
      workspaceName: "Selected Workspace",
      repositoryRoot: root,
      workspaceDirectory,
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("native selection fails closed on cross-Project or archived facts", () => {
  const project = {
    projectId: "project-1",
    projectDisplayName: "Project",
    projectRootPath: "/tmp/project",
  };
  const workspace = {
    id: "workspace-1",
    projectId: "project-2",
    projectRootPath: "/tmp/project",
    workspaceDirectory: "/tmp/project/worktree",
    name: "worktree",
    archivingAt: null,
  };
  assert.throws(
    () => resolveSelectedPaseoDefaults(project, workspace),
    (error: unknown) =>
      error instanceof NativeSelectionError && error.code === "NATIVE_SELECTION_INVALID",
  );
  assert.throws(
    () => resolveSelectedPaseoDefaults(
      project,
      { ...workspace, projectId: "project-1", projectRootPath: "/tmp/other" },
    ),
    (error: unknown) =>
      error instanceof NativeSelectionError &&
      error.code === "NATIVE_SELECTION_PROJECT_MISMATCH",
  );
  assert.throws(
    () => resolveSelectedPaseoDefaults(
      project,
      { ...workspace, projectId: "project-1", archivingAt: "2026-09-12T00:00:00Z" },
    ),
    (error: unknown) =>
      error instanceof NativeSelectionError && error.code === "NATIVE_SELECTION_INVALID",
  );
});
