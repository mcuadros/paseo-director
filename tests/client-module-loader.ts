// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";

import ts from "typescript";

export const testAccessibilityInfo = {
  async isReduceMotionEnabled() {
    return false;
  },
  async isHighTextContrastEnabled() {
    return false;
  },
  async isDarkerSystemColorsEnabled() {
    return false;
  },
  addEventListener() {
    return { remove() {} };
  },
  announceForAccessibility() {},
  announceForAccessibilityWithOptions() {},
};

export function loadClientModule<T>(
  fileName: string,
  requireModule: (specifier: string) => unknown,
): T {
  const source = readFileSync(fileName, "utf8");
  const compiled = ts.transpileModule(source, {
    fileName,
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.CommonJS,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
  }).outputText;
  const module = { exports: {} as Record<string, unknown> };
  Function("require", "module", "exports", compiled)(
    requireModule,
    module,
    module.exports,
  );
  return module.exports as T;
}
