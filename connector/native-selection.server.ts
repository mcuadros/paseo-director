// SPDX-License-Identifier: Apache-2.0
// M6.11 supplies one authoritative native selection to this policy-free resolver.

import { isAbsolute } from "node:path";

import { canonicalProspectivePath } from "./runtime-configuration.server.mjs";

const IDENTITY_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$/u;
const MAXIMUM_NAME_LENGTH = 256;
const MAXIMUM_PATH_LENGTH = 4_096;

function safeName(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 &&
    value.length <= MAXIMUM_NAME_LENGTH && value.trim() === value &&
    !/[\u0000-\u001f\u007f]/u.test(value);
}

function absoluteBoundedPath(value: unknown): value is string {
  return typeof value === "string" && value.length <= MAXIMUM_PATH_LENGTH &&
    isAbsolute(value);
}

export class NativeSelectionError extends Error {
  readonly code: string;

  constructor(code: string) {
    super(code);
    this.name = "NativeSelectionError";
    this.code = code;
  }
}

export type SelectedPaseoDefaults = {
  projectId: string;
  projectName: string;
  workspaceId: string;
  workspaceName: string;
  repositoryRoot: string;
  workspaceDirectory: string;
};

export type NativeProjectSelection = {
  projectId: string;
  projectDisplayName: string;
  projectCustomName?: string | null;
  projectRootPath: string;
};

export type NativeWorkspaceSelection = {
  id: string;
  projectId: string;
  projectRootPath: string;
  workspaceDirectory?: string;
  name: string;
  title?: string | null;
  archivingAt: string | null;
};

export function resolveSelectedPaseoDefaults(
  project: NativeProjectSelection,
  workspace: NativeWorkspaceSelection,
): SelectedPaseoDefaults {
  const projectName = project.projectCustomName ?? project.projectDisplayName;
  const workspaceName = workspace.title ?? workspace.name;
  if (
    !IDENTITY_PATTERN.test(project.projectId) ||
    !IDENTITY_PATTERN.test(workspace.id) ||
    workspace.projectId !== project.projectId ||
    workspace.archivingAt !== null ||
    !safeName(projectName) ||
    !safeName(workspaceName) ||
    !absoluteBoundedPath(project.projectRootPath) ||
    !absoluteBoundedPath(workspace.projectRootPath) ||
    !absoluteBoundedPath(workspace.workspaceDirectory)
  ) {
    throw new NativeSelectionError("NATIVE_SELECTION_INVALID");
  }
  const repositoryRoot = canonicalProspectivePath(project.projectRootPath);
  if (canonicalProspectivePath(workspace.projectRootPath) !== repositoryRoot) {
    throw new NativeSelectionError("NATIVE_SELECTION_PROJECT_MISMATCH");
  }
  return {
    projectId: project.projectId,
    projectName,
    workspaceId: workspace.id,
    workspaceName,
    repositoryRoot,
    workspaceDirectory: canonicalProspectivePath(workspace.workspaceDirectory),
  };
}
