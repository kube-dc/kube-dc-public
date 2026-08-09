#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../../.." && pwd)
script="${repo_root}/tests/e2e/gpu/dra/run-dra-dual-home-lifecycle.sh"
mock="${repo_root}/tests/e2e/gpu/contracts/fixtures/mock-dra-dual-home-kubectl.sh"
events="${repo_root}/tests/e2e/gpu/contracts/fixtures/dra-dual-home-events.json"
event_filter="${repo_root}/tests/e2e/gpu/dra/current-denial-event.yq"
tmp_dir=$(mktemp -d)
trap 'rm -rf "${tmp_dir}"' EXIT
ln -s "${mock}" "${tmp_dir}/kubectl"

run_script() {
  local mode=$1
  local log="${tmp_dir}/${mode}.log"
  local holder="${tmp_dir}/${mode}.holder"
  set +e
  PATH="${tmp_dir}:${PATH}" \
    MOCK_KUBECTL_MODE="${mode}" MOCK_KUBECTL_LOG="${log}" MOCK_KUBECTL_HOLDER="${holder}" \
    KUBECONFIG=/fixture GPU_DRA_DUAL_HOME_NAMESPACE=fixture-project \
    KUBE_DC_GPU_HARDWARE_TESTS=acknowledge-hardware-mutation GPU_HARDWARE_TARGET=fixture-cluster \
    "${script}" >"${tmp_dir}/${mode}.out" 2>&1
  status=$?
  set -e
  [[ "${status}" == "2" ]] || {
    echo "${mode} fixture exited ${status}, expected 2" >&2
    cat "${tmp_dir}/${mode}.out" >&2
    exit 1
  }
}

# The mandatory acknowledgement must stop execution before even a read-only
# kubectl invocation.
set +e
PATH="${tmp_dir}:${PATH}" MOCK_KUBECTL_MODE=lock-held \
  MOCK_KUBECTL_LOG="${tmp_dir}/no-opt-in.log" MOCK_KUBECTL_HOLDER="${tmp_dir}/no-opt-in.holder" \
  KUBECONFIG=/fixture GPU_DRA_DUAL_HOME_NAMESPACE=fixture-project GPU_HARDWARE_TARGET=fixture-cluster \
  "${script}" >"${tmp_dir}/no-opt-in.out" 2>&1
status=$?
set -e
[[ "${status}" == "64" && ! -s "${tmp_dir}/no-opt-in.log" ]] || {
  echo "missing opt-in reached kubectl or returned ${status}" >&2
  exit 1
}

# A lock loser owns no objects and therefore cannot delete anything.
run_script lock-held
if grep -Eq '(^|[[:space:]])delete([[:space:]]|$)' "${tmp_dir}/lock-held.log"; then
  echo "lock loser attempted cleanup of objects it does not own" >&2
  exit 1
fi

# Even after acquiring the lock, the reuse guard owns only that lock. It may
# release it, but must not delete the pre-existing Deployment/template/claim.
run_script reuse
grep -q 'delete configmap' "${tmp_dir}/reuse.log"
if grep -Eq 'delete (deployment|resourceclaimtemplate|resourceclaim)' "${tmp_dir}/reuse.log"; then
  echo "reuse guard deleted pre-existing workload objects" >&2
  exit 1
fi

# Only the event for this run's ReplicaSet UID, after this run's timestamp and
# containing the production-owned message may satisfy admission evidence.
matched=$(STARTED_AT=2026-07-20T12:00:00Z INVOLVED_UID=current-run MESSAGE=__SHARED_MESSAGE__ \
  yq e --from-file "${event_filter}" "${events}")
[[ "${matched}" == "1" ]] || { echo "current-run event filter matched ${matched}, expected 1" >&2; exit 1; }
late=$(STARTED_AT=2026-07-20T12:00:02Z INVOLVED_UID=current-run MESSAGE=__SHARED_MESSAGE__ \
  yq e --from-file "${event_filter}" "${events}")
[[ "${late}" == "0" ]] || { echo "stale event filter matched ${late}, expected 0" >&2; exit 1; }

message_source="${repo_root}/internal/webhook/core/pod_infra_attachment_webhook.go"
shared_message=$(sed -n 's/^[[:space:]]*PodInfraAttachmentCustomSecondaryNetworkMessage = "\(.*\)"$/\1/p' "${message_source}")
[[ -n "${shared_message}" ]] || { echo "shared webhook denial message is not shell-readable" >&2; exit 1; }

echo "GPU DRA dual-home script contracts: PASS"
