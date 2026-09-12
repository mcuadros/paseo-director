// SPDX-License-Identifier: Apache-2.0
// Connector-only credential loading; credential material never crosses the host port.

import {
  closeSync,
  constants,
  existsSync,
  fstatSync,
  lstatSync,
  openSync,
  readFileSync,
  realpathSync,
  type Stats,
} from "node:fs";
import { dirname } from "node:path";

import {
  canonicalProspectivePath,
  pathsAreDisjoint,
} from "./engine-selection.server.ts";

export class ConnectorCredentialError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "ConnectorCredentialError";
    this.code = code;
  }
}

type DirectoryIdentity = {
  path: string;
  dev: number;
  ino: number;
  mode: number;
  uid: number;
};

function sameIdentity(left: Stats, right: Stats): boolean {
  return left.dev === right.dev && left.ino === right.ino;
}

function sameMutableCredentialMetadata(left: Stats, right: Stats): boolean {
  return (
    left.size === right.size &&
    left.mtimeMs === right.mtimeMs &&
    left.ctimeMs === right.ctimeMs &&
    left.mode === right.mode &&
    left.uid === right.uid &&
    left.nlink === right.nlink
  );
}

function credentialAncestorIdentities(
  credentialDirectory: string,
  credentialStatus: Stats,
): DirectoryIdentity[] {
  const identities: DirectoryIdentity[] = [];
  if (typeof process.geteuid !== "function") {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_PLATFORM",
      "connector credential ownership checks require Linux effective-user identity",
    );
  }
  const connectorUid = process.geteuid();
  let filesystemRoot = credentialDirectory;
  while (dirname(filesystemRoot) !== filesystemRoot) {
    filesystemRoot = dirname(filesystemRoot);
  }
  const filesystemRootUid = lstatSync(filesystemRoot).uid;
  const trustedOwner = (uid: number) =>
    uid === connectorUid || uid === filesystemRootUid;
  let protectedEntryStatus = credentialStatus;
  let current = credentialDirectory;
  while (true) {
    const status = lstatSync(current);
    if (!status.isDirectory()) {
      throw new ConnectorCredentialError(
        "CONNECTOR_CREDENTIAL_ANCESTOR_SUBSTITUTED",
        "a canonical connector credential ancestor is not a directory",
      );
    }
    const writableByGroupOrOther = (status.mode & 0o022) !== 0;
    const hasStickyBit = (status.mode & 0o1000) !== 0;
    const stickyProtectionIsSafe =
      hasStickyBit &&
      trustedOwner(status.uid) &&
      trustedOwner(protectedEntryStatus.uid);
    if (writableByGroupOrOther && !stickyProtectionIsSafe) {
      throw new ConnectorCredentialError(
        "CONNECTOR_CREDENTIAL_DIRECTORY_PERMISSIONS",
        "connector credential ancestors must not be writable by group or other users unless a trusted owner and child make Linux sticky-bit protection safe",
      );
    }
    identities.push({
      path: current,
      dev: status.dev,
      ino: status.ino,
      mode: status.mode,
      uid: status.uid,
    });
    protectedEntryStatus = status;
    const parent = dirname(current);
    if (parent === current) break;
    current = parent;
  }
  return identities;
}

function assertAncestorIdentitiesUnchanged(
  identities: readonly DirectoryIdentity[],
): void {
  for (const identity of identities) {
    const status = lstatSync(identity.path);
    if (
      !status.isDirectory() ||
      status.dev !== identity.dev ||
      status.ino !== identity.ino ||
      status.mode !== identity.mode ||
      status.uid !== identity.uid
    ) {
      throw new ConnectorCredentialError(
        "CONNECTOR_CREDENTIAL_ANCESTOR_SUBSTITUTED",
        "a canonical connector credential ancestor changed while loading the credential",
      );
    }
  }
}

export function loadConnectorCredential(options: {
  credentialPath: string | undefined;
  checkoutRoot?: string;
  disjointEnginePaths: readonly string[];
}): string {
  if (!options.credentialPath || !existsSync(options.credentialPath)) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_REQUIRED",
      "Director for Paseo requires its connector credential file",
    );
  }
  const credentialPath = realpathSync(options.credentialPath);
  const credentialDirectory = canonicalProspectivePath(dirname(credentialPath));
  if (
    options.checkoutRoot &&
    !pathsAreDisjoint(
      credentialDirectory,
      canonicalProspectivePath(options.checkoutRoot),
    )
  ) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_IN_CHECKOUT",
      "the connector credential directory must be disjoint from the plugin checkout",
    );
  }
  for (const enginePath of options.disjointEnginePaths) {
    if (!pathsAreDisjoint(credentialDirectory, enginePath)) {
      throw new ConnectorCredentialError(
        "CONNECTOR_CREDENTIAL_ENGINE_PATH",
        "the connector credential directory must be disjoint from every engine path",
      );
    }
  }
  const credentialStatus = lstatSync(credentialPath);
  if (!credentialStatus.isFile()) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_REQUIRED",
      "the connector credential must be a regular file",
    );
  }
  if ((credentialStatus.mode & 0o077) !== 0) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_PERMISSIONS",
      "the connector credential must not be readable by group or other users",
    );
  }
  const ancestorIdentities = credentialAncestorIdentities(
    credentialDirectory,
    credentialStatus,
  );
  const descriptor = openSync(
    credentialPath,
    constants.O_RDONLY | constants.O_NOFOLLOW,
  );
  let credential: string;
  try {
    const openedStatus = fstatSync(descriptor);
    if (!sameIdentity(credentialStatus, openedStatus)) {
      throw new ConnectorCredentialError(
        "CONNECTOR_CREDENTIAL_SUBSTITUTED",
        "the connector credential changed before it could be opened",
      );
    }
    credential = readFileSync(descriptor, "utf8").trim();
    if (!sameMutableCredentialMetadata(openedStatus, fstatSync(descriptor))) {
      throw new ConnectorCredentialError(
        "CONNECTOR_CREDENTIAL_METADATA_CHANGED",
        "the connector credential metadata changed between the pre-read and post-read checks",
      );
    }
    assertAncestorIdentitiesUnchanged(ancestorIdentities);
  } finally {
    closeSync(descriptor);
  }
  if (credential.length === 0) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_EMPTY",
      "Director for Paseo requires a non-empty connector credential",
    );
  }
  return credential;
}

export function assertConnectorCredentialOutsideCheckout(
  credentialPath: string,
  checkoutRoot: string,
): void {
  const credentialDirectory = canonicalProspectivePath(
    dirname(realpathSync(credentialPath)),
  );
  if (
    !pathsAreDisjoint(
      credentialDirectory,
      canonicalProspectivePath(checkoutRoot),
    )
  ) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_IN_CHECKOUT",
      "the connector credential directory must be disjoint from the plugin checkout",
    );
  }
}
