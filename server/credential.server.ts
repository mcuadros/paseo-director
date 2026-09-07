// SPDX-License-Identifier: Apache-2.0

import { existsSync, readFileSync, realpathSync, statSync } from "node:fs";
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

export function loadConnectorCredential(options: {
  credentialPath: string | undefined;
  checkoutRoot: string;
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
  const checkoutRoot = canonicalProspectivePath(options.checkoutRoot);
  if (!pathsAreDisjoint(credentialDirectory, checkoutRoot)) {
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
  const directoryMode = statSync(credentialDirectory).mode & 0o777;
  if ((directoryMode & 0o022) !== 0) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_DIRECTORY_PERMISSIONS",
      "the connector credential directory must not be writable by group or other users",
    );
  }
  const mode = statSync(credentialPath).mode & 0o777;
  if ((mode & 0o077) !== 0) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_PERMISSIONS",
      "the connector credential must not be readable by group or other users",
    );
  }
  const credential = readFileSync(credentialPath, "utf8").trim();
  if (credential.length === 0) {
    throw new ConnectorCredentialError(
      "CONNECTOR_CREDENTIAL_EMPTY",
      "Director for Paseo requires a non-empty connector credential",
    );
  }
  return credential;
}
