// SPDX-License-Identifier: Apache-2.0

export type ShellMetrics = {
  padding: number;
  gap: number;
  boardDirection: "column" | "row";
  titleSize: number;
};

export function shellMetrics(compact: boolean): ShellMetrics {
  return compact
    ? { padding: 16, gap: 8, boardDirection: "column", titleSize: 22 }
    : { padding: 24, gap: 12, boardDirection: "row", titleSize: 28 };
}
