#!/usr/bin/env bash

set -euo pipefail

for command in jq mktemp; do
  command -v "${command}" >/dev/null 2>&1 || {
    echo "required command is missing: ${command}" >&2
    exit 2
  }
done

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../../.." && pwd)
audit="${repo_root}/tests/e2e/gpu/supply-chain/audit-runtime-images.sh"
fixture="${repo_root}/tests/e2e/gpu/contracts/fixtures/gpu-runtime-pods.json"
tmp_dir=$(mktemp -d)
trap 'rm -rf "${tmp_dir}"' EXIT

"${audit}" --pod-json "${fixture}" >"${tmp_dir}/good.out"
grep -Fq 'PASS GPU runtime image provenance: containers=4' "${tmp_dir}/good.out"

jq '.items[0].spec.containers[0].image = "registry.example/driver:latest"' \
  "${fixture}" >"${tmp_dir}/latest.json"
if "${audit}" --pod-json "${tmp_dir}/latest.json" >"${tmp_dir}/latest.out" 2>"${tmp_dir}/latest.err"; then
  echo "floating latest image passed provenance audit" >&2
  exit 1
fi
grep -Fq 'floating latest image is forbidden' "${tmp_dir}/latest.err"

jq '.items[1].status.containerStatuses[0].imageID = "registry.example/scheduler:v1.35.3"' \
  "${fixture}" >"${tmp_dir}/mutable-runtime.json"
if "${audit}" --pod-json "${tmp_dir}/mutable-runtime.json" >"${tmp_dir}/mutable.out" 2>"${tmp_dir}/mutable.err"; then
  echo "mutable runtime image ID passed provenance audit" >&2
  exit 1
fi
grep -Fq 'runtime image is not resolved to sha256' "${tmp_dir}/mutable.err"

echo 'GPU image provenance contracts passed: versioned desired refs and sha256 runtime IDs'
