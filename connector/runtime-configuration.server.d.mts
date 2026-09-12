// SPDX-License-Identifier: Apache-2.0

export type RuntimeSettingSource = "defaulted" | "overridden";
export type RuntimeSettingName =
  | "paseo.url"
  | "engine.mode"
  | "engine.url"
  | "engine.cache-base"
  | "engine.module-cache";

export type RuntimeConfiguration = {
  schemaVersion: 1;
  paseo: { url: string; credentialFile: string };
  engine: {
    mode: "release" | "development";
    url: string;
    sourceRoot?: string;
    moduleCache?: string;
  };
  diagnostics: {
    schemaVersion: 1;
    sha256: string;
    legacyEnvironment: "absent" | "ignored";
    settings: Array<{ name: RuntimeSettingName; source: RuntimeSettingSource }>;
  };
};

export class RuntimeConfigurationError extends Error {
  readonly code: string;
}

export const DEFAULT_ENGINE_MODE: "release";
export const DEFAULT_ENGINE_URL: "http://127.0.0.1:7041";
export const DEFAULT_PASEO_URL: "ws://127.0.0.1:6767/ws";
export function canonicalProspectivePath(value: string): string;
export function pathIsWithin(candidate: string, parent: string): boolean;
export function pathsAreDisjoint(left: string, right: string): boolean;
export function runtimeConfigurationPath(environment?: NodeJS.ProcessEnv, home?: string): string;
export function legacyDirectorEnvironmentState(environment?: NodeJS.ProcessEnv): "absent" | "ignored";
export function parseRuntimeConfiguration(bytes: Uint8Array, environment?: NodeJS.ProcessEnv): RuntimeConfiguration;
export function loadRuntimeConfiguration(environment?: NodeJS.ProcessEnv, home?: string): {
  path: string;
  configuration: RuntimeConfiguration;
};
export function assertCredentialFileDeployment(credentialFile: string): void;
export function assertRuntimeDeployment(
  repositoryRoot: string,
  loaded: { path: string; configuration: RuntimeConfiguration },
  environment?: NodeJS.ProcessEnv,
): RuntimeConfiguration;
