#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

readonly dolt_version="2.3.2"
readonly archive_sha256="7a2949fa2b2b3799ee1e57e6d64519a8d65d675fd832f6469d4e07e5a1c72b14"
readonly archive_url="https://github.com/dolthub/dolt/releases/download/v${dolt_version}/dolt-linux-amd64.tar.gz"
readonly runner_temp="${RUNNER_TEMP:?RUNNER_TEMP is required}"
readonly github_path="${GITHUB_PATH:?GITHUB_PATH is required}"
readonly install_root="${runner_temp}/director-dolt-${dolt_version}"
readonly archive_path="${install_root}/dolt-linux-amd64.tar.gz"
readonly binary_root="${runner_temp}/director-test-bin"

mkdir -p "${install_root}" "${binary_root}"
curl --proto '=https' --tlsv1.2 --location --fail --silent --show-error \
  "${archive_url}" --output "${archive_path}"
printf '%s  %s\n' "${archive_sha256}" "${archive_path}" | sha256sum --check --status
tar --extract --gzip --file "${archive_path}" --directory "${install_root}"
install --mode=0755 "${install_root}/dolt-linux-amd64/bin/dolt" "${binary_root}/dolt"
printf '%s\n' "${binary_root}" >>"${github_path}"
