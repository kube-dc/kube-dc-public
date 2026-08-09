#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../../.." && pwd)
hardware_dir="${repo_root}/tests/e2e/gpu/hardware"
dra_dir="${repo_root}/tests/e2e/gpu/dra"

for script in "${hardware_dir}"/run-*.sh "${dra_dir}"/run-*.sh; do
  guard_line=$(grep -nE '^[[:space:]]*source .*require-opt-in\.sh' "${script}" | head -1 | cut -d: -f1)
  [[ -n "${guard_line}" ]] || { echo "${script} does not source the hardware guard" >&2; exit 1; }
  mutation_line=$(awk '
    {
      if (start == 0) { start = NR; logical = $0 } else { logical = logical " " $0 }
      if (logical ~ /\\[[:space:]]*$/) { sub(/\\[[:space:]]*$/, "", logical); next }
      if (logical !~ /^[[:space:]]*#/ &&
          logical ~ /(kubectl|"\$\{k\[@\]\}")/ &&
          logical ~ /(^|[[:space:]])(apply|create|delete|patch|replace|run|exec|annotate|label|scale|set|cordon|uncordon|taint)([[:space:]]|$)/) {
        print start
        exit
      }
      start = 0
      logical = ""
    }
  ' "${script}")
  if [[ -n "${mutation_line}" && "${guard_line}" -ge "${mutation_line}" ]]; then
    echo "${script} sources the hardware guard after a mutating kubectl" >&2
    exit 1
  fi
done

# Every DRA harness must arm lock cleanup before attempting the create. This is
# intentionally class-wide: a failed create can still leave an ambiguous server
# outcome, so fixing only the script that most recently regressed is unsafe.
for script in "${dra_dir}"/run-dra-*.sh; do
  grep -q 'create configmap "${lock_name}"' "${script}" || continue
  awk '
    /^lock_acquired=true$/ { lock_flag = NR }
    /create configmap "\$\{lock_name\}"/ {
      if (lock_flag == 0 || lock_flag >= NR) exit 1
      lock_create = NR
    }
    END { if (lock_create == 0) exit 1 }
  ' "${script}" || {
    echo "${script} arms hardware-lock cleanup after the create" >&2
    exit 1
  }
done

for script in "${hardware_dir}"/run-*.sh "${dra_dir}"/run-*.sh; do
  grep -q '^readonly_project_infrastructure=true$' "${script}" || continue
  grep -q 'GPU_DRA_DUAL_HOME_NAMESPACE:?' "${script}"

  forbidden_mutation=$(awk '
    {
      if (start == 0) { start = NR; logical = $0 } else { logical = logical " " $0 }
      if (logical ~ /\\[[:space:]]*$/) { sub(/\\[[:space:]]*$/, "", logical); next }
      if (logical !~ /^[[:space:]]*#/ &&
          logical ~ /(kubectl|"\$\{k\[@\]\}")/ &&
          logical ~ /(^|[[:space:]])(apply|create|delete|patch|replace|annotate|label|set)([[:space:]]|$)/ &&
          logical ~ /(^|[[:space:]])(namespace|namespaces|ns|resourcequota|resourcequotas|quota|quotas)([[:space:]]|$)/) {
        print start ":" logical
        exit
      }
      start = 0
      logical = ""
    }
  ' "${script}")
  if [[ -n "${forbidden_mutation}" ]]; then
    echo "${script} mutates readonly project infrastructure at ${forbidden_mutation}" >&2
    exit 1
  fi

  grep -q '^failed_objects_owned=false$' "${script}"
  grep -q '^workload_objects_owned=false$' "${script}"
  grep -q 'if \[\[ "${failed_objects_owned}" == true \]\]; then' "${script}"
  grep -q 'if \[\[ "${workload_objects_owned}" == true \]\]; then' "${script}"
  lock_line=$(grep -n 'create configmap "${lock_name}"' "${script}" | head -1 | cut -d: -f1)
  reuse_line=$(grep -n '^for name in "${workload}" "${failed_workload}"; do$' "${script}" | cut -d: -f1)
  failed_owned_line=$(grep -n '^failed_objects_owned=true$' "${script}" | cut -d: -f1)
  workload_owned_line=$(grep -n '^workload_objects_owned=true$' "${script}" | cut -d: -f1)
  [[ -n "${lock_line}" && -n "${reuse_line}" && "${lock_line}" -lt "${reuse_line}" &&
     "${failed_owned_line}" -gt "${reuse_line}" && "${workload_owned_line}" -gt "${reuse_line}" ]] || {
    echo "${script} does not acquire the lock and establish ownership after the reuse guard" >&2
    exit 1
  }
  grep -q 'PodInfraAttachmentCustomSecondaryNetworkMessage' "${script}"
  grep -q 'INVOLVED_UID=' "${script}"
  grep -q 'STARTED_AT=' "${script}"
done

for script in "${dra_dir}/run-dra-lifecycle.sh" "${dra_dir}/run-dra-driver-outage.sh"; do
  grep -q '^namespace_created=false$' "${script}"
  grep -q 'if \[\[ "${namespace_created}" == true \]\]; then' "${script}"
  created_line=$(grep -nE 'create namespace "\$\{namespace\}"' "${script}" | tail -1 | cut -d: -f1)
  owned_line=$(grep -n '^namespace_created=true$' "${script}" | tail -1 | cut -d: -f1)
  [[ -n "${created_line}" && -n "${owned_line}" && "${owned_line}" -gt "${created_line}" ]] || {
    echo "${script} marks namespace ownership before successful creation" >&2
    exit 1
  }
done

for script in "${dra_dir}/run-dra-driver-outage.sh" "${dra_dir}/run-dra-stale-driver-recovery.sh"; do
  grep -q '^flux_parent_name=${GPU_DRA_FLUX_PARENT_KUSTOMIZATION:-flux-system}$' "${script}"
  grep -q '^flux_stabilize_seconds=${GPU_DRA_FLUX_STABILIZE_SECONDS:-30}$' "${script}"
  grep -q '^suspend_flux_owners() {$' "${script}"
  grep -q 'patch kustomization "${flux_parent_name}".*"suspend":true' "${script}"
  grep -q 'resume_flux_owner "${flux_parent_name}"' "${script}"
  grep -q '^resume_flux_owner() {$' "${script}"
  grep -q 'retaining hardware lock .* restoration is incomplete' "${script}"
  if grep -E 'patch kustomization .*"suspend":false.*\|\| true' "${script}" >/dev/null; then
    echo "${script} swallows a Flux resume failure" >&2
    exit 1
  fi
  awk '
    /^lock_acquired=true$/ { lock_flag = NR }
    /create configmap "\$\{lock_name\}"/ {
      if (lock_flag == 0 || lock_flag >= NR) exit 1
      lock_create = NR
    }
    /flux_parent_(changed|mutated)=true/ { parent_flag = NR }
    /patch kustomization "\$\{flux_parent_name\}".*"suspend":true/ {
      if (parent_flag == 0 || parent_flag >= NR) exit 1
      parent_patch = NR
    }
    /^[[:space:]]+flux_(changed|mutated)=true$/ { child_flag = NR }
    /patch kustomization "\$\{flux_name\}".*"suspend":true/ {
      if (child_flag == 0 || child_flag >= NR) exit 1
      child_patch = NR
    }
    /^suspend_flux_owners$/ { mutation_phase = 1 }
    mutation_phase && /^driver_(changed|mutated)=true$/ { driver_flag = NR }
    mutation_phase && /patch daemonset "\$\{driver_name\}"/ && driver_patch == 0 {
      if (driver_flag == 0 || driver_flag >= NR) exit 1
      driver_patch = NR
    }
    END {
      if (lock_create == 0 || parent_patch == 0 || child_patch == 0 || driver_patch == 0) exit 1
    }
  ' "${script}" || {
    echo "${script} records restoration ownership after a mutation" >&2
    exit 1
  }
done
grep -q 'injected driver Pod disappeared before recovery' "${dra_dir}/run-dra-stale-driver-recovery.sh"
grep -q '^postflight_attempts=${GPU_DRA_POSTFLIGHT_ATTEMPTS:-90}$' "${dra_dir}/run-dra-stale-driver-recovery.sh"
grep -q 'DRA postflight did not recover after the allocation-canary window' "${dra_dir}/run-dra-stale-driver-recovery.sh"

for script in "${hardware_dir}/run-hami-core-throttle.sh" "${hardware_dir}/run-hami-plugin-outage.sh"; do
  mapfile -t preflight_lines < <(grep -n '^preflight$' "${script}" | cut -d: -f1)
  lock_line=$(grep -n 'create configmap "${lock_name}"' "${script}" | head -1 | cut -d: -f1)
  canary_patch_line=$(grep -n 'patch cronjob "${canary_name}"' "${script}" | tail -1 | cut -d: -f1)
  [[ "${#preflight_lines[@]}" -eq 2 && "${preflight_lines[0]}" -lt "${lock_line}" &&
     "${preflight_lines[1]}" -gt "${lock_line}" && "${preflight_lines[1]}" -lt "${canary_patch_line}" ]] || {
    echo "${script} must preflight before lock and recheck before canary mutation" >&2
    exit 1
  }
done

trust_script="${dra_dir}/run-dra-trust-boundary.sh"
for metric in driver_desired driver_ready unready_driver_pods driver_discovery_success; do
  grep -q "kube_dc_gpu_dra_${metric}" "${trust_script}"
done
grep -q '^observe_seconds=${GPU_DRA_TRUST_OBSERVE_SECONDS:-75}$' "${trust_script}"
grep -q 'GPU_DRA_TRUST_OBSERVE_SECONDS must be an integer from 65 through 75' "${trust_script}"
grep -Fq '[[ "${observe_seconds}" =~ ^[0-9]+$ && "${observe_seconds}" -ge 65 && "${observe_seconds}" -le 75 ]] || {' "${trust_script}"
[[ "$(grep -Fc 'for _ in $(seq 1 30); do' "${trust_script}")" == "2" ]]
grep -q 'expected exactly one global .* series' "${trust_script}"
grep -Fq '.data.result[0].metric | has("profile")' "${trust_script}"
grep -q 'global .* still carries a profile label' "${trust_script}"
grep -q 'kube-dc.com/project: ""' "${trust_script}"
grep -q 'app.kubernetes.io/name: hami-dra-driver' "${trust_script}"
grep -q 'hami-dra-driver-component: kubelet-plugin' "${trust_script}"
grep -q 'tenant forgery unexpectedly created .* ResourceClaims' "${trust_script}"
grep -q 'ResourceClaim fingerprint did not return to baseline after forgery cleanup' "${trust_script}"
grep -q 'trusted DRA inventory changed during trust-boundary acceptance' "${trust_script}"
grep -q 'CLI postflight trusted or disclosed the tenant forgery' "${trust_script}"
grep -q 'recovery-plan named a tenant Pod' "${trust_script}"
grep -q 'retaining hardware lock .* forgery cleanup is incomplete' "${trust_script}"
grep -Fq 'if [[ "${owner}" != "${run_id}" ]]' "${trust_script}"
remove_line=$(grep -n '^remove_forgery$' "${trust_script}" | tail -1 | cut -d: -f1)
claim_line=$(grep -n 'current_claims=$(claim_fingerprint)' "${trust_script}" | tail -1 | cut -d: -f1)
metric_line=$(grep -n 'driver_metric_fingerprint)" == "${baseline_metrics}"' "${trust_script}" | tail -1 | cut -d: -f1)
[[ -n "${remove_line}" && -n "${claim_line}" && -n "${metric_line}" &&
   "${remove_line}" -lt "${claim_line}" && "${claim_line}" -lt "${metric_line}" ]] || {
  echo "${trust_script} does not run post-cleanup claim convergence before the metric assertion" >&2
  exit 1
}
lock_flag_line=$(grep -n '^lock_acquired=true$' "${trust_script}" | tail -1 | cut -d: -f1)
lock_create_line=$(grep -n 'create configmap "${lock_name}"' "${trust_script}" | tail -1 | cut -d: -f1)
namespace_flag_line=$(grep -n '^namespace_cleanup_armed=true$' "${trust_script}" | tail -1 | cut -d: -f1)
namespace_create_line=$(grep -n '"${k\[@\]}" create -f -' "${trust_script}" | tail -1 | cut -d: -f1)
[[ -n "${lock_flag_line}" && -n "${lock_create_line}" && "${lock_flag_line}" -lt "${lock_create_line}" &&
   -n "${namespace_flag_line}" && -n "${namespace_create_line}" && "${namespace_flag_line}" -lt "${namespace_create_line}" ]] || {
  echo "${trust_script} arms lock/namespace cleanup after mutation" >&2
  exit 1
}
if grep -E '"${k\[@\]}" -n "${driver_namespace}".*(create|delete|patch|set|apply)' "${trust_script}" >/dev/null; then
  echo "${trust_script} mutates the trusted DRA driver" >&2
  exit 1
fi

if bash "${hardware_dir}/require-opt-in.sh" >/dev/null 2>&1; then
  echo "hardware guard accepted a missing opt-in" >&2
  exit 1
fi
if KUBE_DC_GPU_HARDWARE_TESTS=acknowledge-hardware-mutation \
  bash "${hardware_dir}/require-opt-in.sh" >/dev/null 2>&1; then
  echo "hardware guard accepted a missing target" >&2
  exit 1
fi
KUBE_DC_GPU_HARDWARE_TESTS=acknowledge-hardware-mutation \
  GPU_HARDWARE_TARGET=fixture-cluster \
  bash "${hardware_dir}/require-opt-in.sh"

echo "GPU hardware execution boundary: PASS"
