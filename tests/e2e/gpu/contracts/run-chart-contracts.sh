#!/usr/bin/env bash

# Hardware-free GPU chart contract checks. Live admission behavior remains in
# ../admission; this script pins disabled defaults, the validated fixture, and
# fail-closed catalog mistakes on every ordinary pull request.

set -euo pipefail

for command in diff helm grep node sed; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "${script_dir}/../../../.." && pwd)
chart="${repo_root}/charts/kube-dc"
fixture="${repo_root}/tests/e2e/gpu/contracts/values.yaml"
dra_fixture="${repo_root}/charts/kube-dc/gpu-dra-test-values.yaml"
catalog_golden="${repo_root}/tests/e2e/gpu/contracts/golden/gpu-profiles-configmap.yaml"
billing_plans_template="${chart}/templates/billing-plans-configmap.yaml"
tmp_dir=$(mktemp -d)
trap 'rm -rf "${tmp_dir}"' EXIT

require_present() {
  local file=$1
  local text=$2
  if ! grep -Fq -- "${text}" "${file}"; then
    echo "expected rendered GPU contract is missing: ${text}" >&2
    exit 1
  fi
}

require_absent() {
  local file=$1
  local text=$2
  if grep -Fq -- "${text}" "${file}"; then
    echo "disabled GPU chart unexpectedly rendered: ${text}" >&2
    exit 1
  fi
}

require_count() {
  local file=$1
  local text=$2
  local expected=$3
  local actual
  actual=$(grep -Fc -- "${text}" "${file}" || true)
  if [[ "${actual}" -ne "${expected}" ]]; then
    echo "expected ${expected} occurrences of ${text}, found ${actual}" >&2
    exit 1
  fi
}

require_env_value() {
  local file=$1
  local name=$2
  local value=$3
  local block
  block=$(grep -A1 -F -- "- name: ${name}" "${file}" || true)
  if [[ "${block}" != *"value: \"${value}\""* ]]; then
    echo "expected ${name}=${value} in rendered backend environment" >&2
    exit 1
  fi
}

expect_render_failure() {
  local label=$1
  shift
  if helm template gpu-contract "${chart}" -f "${fixture}" "$@" >"${tmp_dir}/${label}.out" 2>"${tmp_dir}/${label}.err"; then
    echo "invalid GPU fixture rendered successfully: ${label}" >&2
    exit 1
  fi
}

helm lint "${chart}"
require_present "${billing_plans_template}" '(not (hasKey $addon "selfService"))'
require_absent "${billing_plans_template}" 'get $addon "selfService" | default true'
helm template gpu-off "${chart}" >"${tmp_dir}/off.yaml"
node "${script_dir}/check-vap-match-condition-maps.js" "${tmp_dir}/off.yaml"
require_present "${tmp_dir}/off.yaml" 'billing.kube-dc.com/plan-seed-migration: "gpu-v100-shared-8g-v3-disabled"'
sku_block=$(grep -m1 -A4 -F -- 'gpu-v100-shared-8g:' "${tmp_dir}/off.yaml")
if [[ "${sku_block}" != *'disabled: true'* || "${sku_block}" != *'selfService: false'* ]]; then
  echo "pilot GPU add-on must render disabled and operator-only" >&2
  exit 1
fi

helm template gpu-operator-only "${chart}" \
  --set-string 'billing.plans.migration.version=gpu-v100-shared-8g-v4-operator-only' \
  --set 'billing.plans.migration.availabilityOverrides.gpu-v100-shared-8g.disabled=false' \
  --set 'billing.plans.migration.availabilityOverrides.gpu-v100-shared-8g.selfService=false' \
  >"${tmp_dir}/operator-only.yaml"
require_present "${tmp_dir}/operator-only.yaml" 'billing.kube-dc.com/plan-seed-migration: "gpu-v100-shared-8g-v4-operator-only"'
operator_sku_block=$(grep -m1 -A20 -F -- 'gpu-v100-shared-8g:' "${tmp_dir}/operator-only.yaml")
if [[ "${operator_sku_block}" != *'disabled: false'* || "${operator_sku_block}" != *'selfService: false'* ]]; then
  echo "operator-only migration must make the pilot SKU assignable but never self-service" >&2
  exit 1
fi
expect_render_failure unsafe-availability-override \
  --set 'billing.plans.migration.availabilityOverrides.gpu-v100-shared-8g.disabled=false' \
  --set 'billing.plans.migration.availabilityOverrides.gpu-v100-shared-8g.selfService=true'
expect_render_failure incomplete-availability-override \
  --set 'billing.plans.migration.availabilityOverrides.gpu-v100-shared-8g.disabled=false'
expect_render_failure unlisted-availability-override \
  --set 'billing.plans.migration.availabilityOverrides.unknown-addon.disabled=true' \
  --set 'billing.plans.migration.availabilityOverrides.unknown-addon.selfService=false'

# GPU plan variants. A plan carrying GPU grants is not inert without a catalog:
# LoadPlanConfig rejects the ENTIRE plans.yaml, so the default render must carry
# no variant at all, and every way of enabling one on an unqualified cluster must
# fail the render rather than seed it.
require_absent "${tmp_dir}/off.yaml" 'dev-pool-gpu:'
require_absent "${tmp_dir}/off.yaml" 'pro-pool-gpu:'
require_absent "${tmp_dir}/off.yaml" 'scale-pool-gpu:'
helm template gpu-plan-variants "${chart}" -f "${fixture}" \
  --set 'gpu.profiles[0].billingEligible=true' \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=whmcs' \
  --set-string 'billing.plans.migration.version=gpu-plan-variants-contract' \
  --set 'billing.plans.migration.addMissingPlans[0]=dev-pool-gpu' \
  --set 'billing.plans.migration.addMissingPlans[1]=pro-pool-gpu' \
  --set 'billing.plans.migration.addMissingPlans[2]=scale-pool-gpu' \
  >"${tmp_dir}/plan-variants.yaml"
for plan_id in dev-pool pro-pool scale-pool; do
  require_present "${tmp_dir}/plan-variants.yaml" "      ${plan_id}-gpu:"
done
# Every variant needs its own eipQuota entry: plans.<id>.ipv4 is display-only,
# and CheckEIPQuota skips a plan that is missing from the eipQuota map.
require_present "${tmp_dir}/plan-variants.yaml" '      dev-pool-gpu: 1'
require_present "${tmp_dir}/plan-variants.yaml" '      pro-pool-gpu: 1'
require_present "${tmp_dir}/plan-variants.yaml" '      scale-pool-gpu: 3'
# The operator-only sections share the key and are REQUIRED by LoadPlanConfig;
# merging variants must not drop them (root-caused 2026-06-25).
require_present "${tmp_dir}/plan-variants.yaml" '    suspendedPlan:'
require_present "${tmp_dir}/plan-variants.yaml" '    systemOverhead:'
require_present "${tmp_dir}/plan-variants.yaml" '    addons:'
expect_render_failure plan-variants-without-gpu \
  --set 'gpu.enabled=false' \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=whmcs'
# GPU is a plan only on WHMCS, which cannot carry add-on quantities. Every other
# provider sells the GPU ADD-ON, so seeding these plans there would publish a
# second, unbillable way to buy the same entitlement.
expect_render_failure plan-variants-on-stripe \
  --set 'gpu.profiles[0].billingEligible=true' \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=stripe'
expect_render_failure plan-variants-without-a-provider \
  --set 'gpu.profiles[0].billingEligible=true' \
  --set 'billing.plans.gpuVariants.enabled=true'
expect_render_failure plan-variants-not-billing-eligible \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=whmcs'
expect_render_failure plan-variants-unknown-profile \
  --set 'gpu.profiles[0].billingEligible=true' \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=whmcs' \
  --set 'billing.plans.gpuVariants.plans.dev-pool-gpu.gpu.ghost-profile.shares=1'
expect_render_failure plan-variants-without-limit-range \
  --set 'gpu.profiles[0].billingEligible=true' \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=whmcs' \
  --set 'billing.plans.gpuVariants.plans.dev-pool-gpu.limitRange=null'
expect_render_failure plan-variants-without-eip-quota \
  --set 'gpu.profiles[0].billingEligible=true' \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=whmcs' \
  --set 'billing.plans.gpuVariants.eipQuota.dev-pool-gpu=null'
expect_render_failure plan-variants-colliding-id \
  --set 'gpu.profiles[0].billingEligible=true' \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=whmcs' \
  --set-json 'billing.plans.gpuVariants.plans={"dev-pool":{"requests":{"cpu":"4"},"limitRange":{},"servicesLB":1,"gpu":{"nvidia-v100-hami":{"shares":1}}}}'
# addMissingPlans is validated against the desired config BEFORE the lookup,
# because `lookup` is empty during render — otherwise a misspelled or gated-off
# plan id would pass CI and only fail as a live reconcile error.
expect_render_failure plan-migration-without-version \
  --set-string 'billing.plans.migration.version=' \
  --set 'billing.plans.migration.addMissingPlans[0]=dev-pool-gpu'
expect_render_failure plan-migration-gated-off \
  --set 'billing.plans.migration.addMissingPlans[0]=dev-pool-gpu'
expect_render_failure plan-migration-misspelled-id \
  --set 'gpu.profiles[0].billingEligible=true' \
  --set 'billing.plans.gpuVariants.enabled=true' \
  --set-string 'billing.provider=whmcs' \
  --set 'billing.plans.migration.addMissingPlans[0]=dev-pool-gpuu'
require_absent "${tmp_dir}/off.yaml" 'name: gpu-profiles'
require_absent "${tmp_dir}/off.yaml" 'name: kube-dc-hami-gpu-pods'
require_absent "${tmp_dir}/off.yaml" 'name: kube-dc-gpu-virtualmachines'
require_present "${tmp_dir}/off.yaml" 'name: gpu-off-kube-dc-manager-metrics'
require_present "${tmp_dir}/off.yaml" 'bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token'
require_present "${tmp_dir}/off.yaml" 'alert: KubeDcOrganizationQuotaConfigInvalid'
require_present "${tmp_dir}/off.yaml" 'expr: absent(kube_dc_organization_quota_config_valid)'
require_present "${tmp_dir}/off.yaml" 'alert: KubeDcOrganizationQuotaReconcileErrors'
require_present "${tmp_dir}/off.yaml" 'kube_dc_organization_quota_reconcile_total{result="error"}'
require_absent "${tmp_dir}/off.yaml" 'alert: KubeDcGPUDRADiscoveryFailed'
require_count "${tmp_dir}/off.yaml" 'service: kube-dc-control-plane' 3
require_count "${tmp_dir}/off.yaml" 'owner: control-plane' 3
require_count "${tmp_dir}/off.yaml" 'runbook_url: https://github.com/shalb/kube-dc/blob/main/docs/internal/shared-gpu-pilot-runbook.md' 3
require_absent "${tmp_dir}/off.yaml" 'nonResourceURLs:'

helm template metrics-off "${chart}" --set 'manager.metrics.enabled=false' >"${tmp_dir}/metrics-off.yaml"
require_absent "${tmp_dir}/metrics-off.yaml" 'name: metrics-off-kube-dc-manager-metrics'
require_absent "${tmp_dir}/metrics-off.yaml" 'containerPort: 8443'

helm template metrics-rbac "${chart}" \
  --set 'manager.metrics.rbac.create=true' \
  --set 'manager.metrics.rbac.serviceAccounts[0].namespace=monitoring' \
  --set 'manager.metrics.rbac.serviceAccounts[0].name=metrics-scraper' \
  >"${tmp_dir}/metrics-rbac.yaml"
require_present "${tmp_dir}/metrics-rbac.yaml" 'nonResourceURLs:'
require_present "${tmp_dir}/metrics-rbac.yaml" 'name: metrics-scraper'
require_present "${tmp_dir}/metrics-rbac.yaml" 'namespace: monitoring'

expect_render_failure metrics-rbac-without-subject \
  --set 'manager.metrics.rbac.create=true'
expect_render_failure invalid-metrics-port \
  --set 'manager.metrics.service.port=0'

helm template gpu-on "${chart}" -f "${fixture}" >"${tmp_dir}/on.yaml"
node "${script_dir}/check-vap-match-condition-maps.js" "${tmp_dir}/on.yaml"
helm template gpu-contract "${chart}" -f "${fixture}" \
  --show-only templates/gpu-profiles-configmap.yaml \
  | sed -e '/^---$/d' -e '/^# Source:/d' -e '/helm.sh\/chart:/d' \
  >"${tmp_dir}/gpu-profiles-configmap.yaml"
diff -u "${catalog_golden}" "${tmp_dir}/gpu-profiles-configmap.yaml"
require_present "${tmp_dir}/on.yaml" 'name: gpu-profiles'
require_present "${tmp_dir}/on.yaml" 'id: nvidia-v100-hami'
require_present "${tmp_dir}/on.yaml" 'billingEligible: false'
require_present "${tmp_dir}/on.yaml" 'displayName: Standard'
require_present "${tmp_dir}/on.yaml" 'allowCustom: true'
require_present "${tmp_dir}/on.yaml" 'name: kube-dc-hami-gpu-pods'
require_present "${tmp_dir}/on.yaml" 'name: kube-dc-gpu-virtualmachines'
require_present "${tmp_dir}/on.yaml" 'name: kube-dc-gpu-virtualmachineinstances'
require_present "${tmp_dir}/on.yaml" 'name: GPU_SHARED_CREATION_ENABLED'
require_present "${tmp_dir}/on.yaml" 'name: GPU_VM_CREATION_ENABLED'
require_env_value "${tmp_dir}/on.yaml" GPU_SHARED_CREATION_ENABLED false
require_env_value "${tmp_dir}/on.yaml" GPU_VM_CREATION_ENABLED false
require_env_value "${tmp_dir}/on.yaml" GPU_HAMI_SCHEDULER_NAME hami-scheduler
require_present "${tmp_dir}/on.yaml" 'request.userInfo.username == "system:serviceaccount:kubevirt:kubevirt-controller"'
require_present "${tmp_dir}/on.yaml" "kind == 'VirtualMachineInstance'"
require_present "${tmp_dir}/on.yaml" 'Ephemeral containers cannot be attached to GPU VM launcher Pods.'

helm template gpu-custom-scheduler "${chart}" -f "${fixture}" \
  --set-string 'gpu.hamiSchedulerName=hami-scheduler-v2' >"${tmp_dir}/custom-scheduler.yaml"
require_env_value "${tmp_dir}/custom-scheduler.yaml" GPU_HAMI_SCHEDULER_NAME hami-scheduler-v2
require_present "${tmp_dir}/custom-scheduler.yaml" 'object.spec.schedulerName == "hami-scheduler-v2"'

helm template gpu-dra-fixed "${chart}" -f "${dra_fixture}" >"${tmp_dir}/dra-fixed.yaml"
node "${script_dir}/check-vap-match-condition-maps.js" "${tmp_dir}/dra-fixed.yaml"
require_present "${tmp_dir}/dra-fixed.yaml" 'alert: KubeDcGPUDRADiscoveryFailed'
require_present "${tmp_dir}/dra-fixed.yaml" 'absent(kube_dc_gpu_dra_discovery_success)'
require_present "${tmp_dir}/dra-fixed.yaml" 'absent(kube_dc_gpu_dra_driver_discovery_success)'
require_present "${tmp_dir}/dra-fixed.yaml" 'alert: KubeDcGPUDRADriverUnavailable'
require_present "${tmp_dir}/dra-fixed.yaml" 'alert: KubeDcGPUDRAStaleDriverPod'
require_present "${tmp_dir}/dra-fixed.yaml" 'kube_dc_gpu_dra_unready_driver_pods > 0'
require_present "${tmp_dir}/dra-fixed.yaml" 'kube_dc_gpu_dra_driver_desired > 0'
require_present "${tmp_dir}/dra-fixed.yaml" 'ten minutes since the PodReady=False'
require_present "${tmp_dir}/dra-fixed.yaml" 'sum(kube_dc_gpu_dra_devices) == 0'
require_present "${tmp_dir}/dra-fixed.yaml" 'min(kube_dc_gpu_dra_discovery_success) == 1'
require_present "${tmp_dir}/dra-fixed.yaml" 'alert: KubeDcGPUDRAProfileUnavailable'
require_present "${tmp_dir}/dra-fixed.yaml" 'alert: KubeDcGPUDRADeviceNotShareable'
require_present "${tmp_dir}/dra-fixed.yaml" 'kube_dc_gpu_dra_claims{state="pending"}'
require_present "${tmp_dir}/dra-fixed.yaml" 'kind: DeviceClass'
require_present "${tmp_dir}/dra-fixed.yaml" 'name: kube-dc-nvidia-v100-shared-8g'
require_present "${tmp_dir}/dra-fixed.yaml" 'device.allowMultipleAllocations == true'
require_present "${tmp_dir}/dra-fixed.yaml" 'name: kube-dc-dra-gpu-claim-templates'
require_present "${tmp_dir}/dra-fixed.yaml" 'name: protect-kube-dc-dra-resourceclaims'
require_present "${tmp_dir}/dra-fixed.yaml" "'kube-dc.com/gpu-backend' in object.metadata.labels"
require_present "${tmp_dir}/dra-fixed.yaml" "'kube-dc.com/gpu-backend' in oldObject.metadata.labels"
require_present "${tmp_dir}/dra-fixed.yaml" "schedulerName in ['', 'default-scheduler']"
require_present "${tmp_dir}/dra-fixed.yaml" 'system:serviceaccount:kube-system:replicaset-controller'
require_present "${tmp_dir}/dra-fixed.yaml" 'system:serviceaccount:kube-system:resource-claim-controller'
require_present "${tmp_dir}/dra-fixed.yaml" "request.userInfo.username == 'system:kube-scheduler'"
require_present "${tmp_dir}/dra-fixed.yaml" "!has(request.userInfo.groups) || !('system:masters' in request.userInfo.groups)"
require_present "${tmp_dir}/dra-fixed.yaml" "object.spec.strategy.type == 'Recreate'"
require_present "${tmp_dir}/dra-fixed.yaml" '["nvidia.com/gpumem-percentage","nvidia.com/priority","nvidia.com/gpu"]'
require_env_value "${tmp_dir}/dra-fixed.yaml" GPU_SHARED_CREATION_ENABLED false

helm template gpu-dra-mapping-inactive "${chart}" -f "${dra_fixture}" \
  --set-string 'gpu.profiles[0].allocationBackend=' >"${tmp_dir}/dra-mapping-inactive.yaml"
require_absent "${tmp_dir}/dra-mapping-inactive.yaml" 'kind: DeviceClass'
require_absent "${tmp_dir}/dra-mapping-inactive.yaml" 'name: protect-kube-dc-dra-resourceclaims'

# A DRA Pod SKU may intentionally reuse the same native resource name as a
# KubeVirt passthrough SKU. The generic HAMi deny list must not then reject the
# virt-launcher Pod before the VM-specific admission checks can authorize it.
helm template gpu-dra-vm-shared-resource "${chart}" -f "${dra_fixture}" \
  --set 'gpu.profiles[1].id=nvidia-v100-passthrough' \
  --set 'gpu.profiles[1].displayName=NVIDIA V100 passthrough' \
  --set 'gpu.profiles[1].vendor=nvidia' \
  --set 'gpu.profiles[1].workload=vm' \
  --set 'gpu.profiles[1].allocation=passthrough' \
  --set 'gpu.profiles[1].allocationBackend=kubevirt-host-device' \
  --set 'gpu.profiles[1].resourceName=nvidia.com/gpu' \
  --set 'gpu.profiles[1].memoryMiB=32768' \
  --set 'gpu.profiles[1].isolation=device' \
  --set 'gpu.profiles[1].enabled=true' \
  --set 'gpu.profiles[1].billingEligible=false' \
  --set 'gpu.profiles[1].liveMigratable=false' \
  --set 'gpu.profiles[1].pciVendorSelector=10DE:1DB6' \
  >"${tmp_dir}/dra-vm-shared-resource.yaml"
require_present "${tmp_dir}/dra-vm-shared-resource.yaml" 'name: hasVMResources'
require_present "${tmp_dir}/dra-vm-shared-resource.yaml" '["nvidia.com/gpumem-percentage","nvidia.com/priority"]'
require_absent "${tmp_dir}/dra-vm-shared-resource.yaml" '["nvidia.com/gpumem-percentage","nvidia.com/priority","nvidia.com/gpu"]'

if helm template gpu-dra-custom "${chart}" -f "${dra_fixture}" \
  --set 'gpu.profiles[0].request.allowCustom=true' >"${tmp_dir}/dra-custom.out" 2>"${tmp_dir}/dra-custom.err"; then
  echo 'active DRA profile accepted custom tenant capacity' >&2
  exit 1
fi
if helm template gpu-dra-duplicate-class "${chart}" -f "${dra_fixture}" \
  --set 'gpu.profiles[1].id=nvidia-a100-shared-8g' \
  --set 'gpu.profiles[1].displayName=A100' \
  --set 'gpu.profiles[1].enabled=true' \
  --set 'gpu.profiles[1].allocation=hami' \
  --set 'gpu.profiles[1].allocationBackend=dra' \
  --set 'gpu.profiles[1].workload=pod' \
  --set 'gpu.profiles[1].memoryMiB=81920' \
  --set 'gpu.profiles[1].request.allowCustom=false' \
  --set 'gpu.profiles[1].request.presets[0].id=shared-8g' \
  --set 'gpu.profiles[1].request.presets[0].displayName=Shared-8G' \
  --set 'gpu.profiles[1].request.presets[0].memoryMiB=8192' \
  --set 'gpu.profiles[1].request.presets[0].corePercent=25' \
  --set 'gpu.profiles[1].request.presets[0].default=true' \
  --set 'gpu.profiles[1].dra.driver=hami-core-gpu.project-hami.io' \
  --set 'gpu.profiles[1].dra.deviceClassName=kube-dc-nvidia-v100-shared-8g' \
  --set 'gpu.profiles[1].dra.attributes.productName=NVIDIA-A100' \
  --set 'gpu.profiles[1].dra.memoryCapacity=memory' \
  --set 'gpu.profiles[1].dra.coreCapacity=cores' >"${tmp_dir}/dra-duplicate.out" 2>"${tmp_dir}/dra-duplicate.err"; then
  echo 'two DRA SKUs accepted one DeviceClass/quota identity' >&2
  exit 1
fi
if helm template gpu-dra-overlap "${chart}" -f "${dra_fixture}" \
  --set 'gpu.profiles[1].id=nvidia-a100-shared-8g' \
  --set 'gpu.profiles[1].displayName=A100' \
  --set 'gpu.profiles[1].enabled=true' \
  --set 'gpu.profiles[1].allocation=hami' \
  --set 'gpu.profiles[1].allocationBackend=dra' \
  --set 'gpu.profiles[1].workload=pod' \
  --set 'gpu.profiles[1].memoryMiB=81920' \
  --set 'gpu.profiles[1].request.allowCustom=false' \
  --set 'gpu.profiles[1].request.presets[0].id=shared-8g' \
  --set 'gpu.profiles[1].request.presets[0].displayName=Shared-8G' \
  --set 'gpu.profiles[1].request.presets[0].memoryMiB=8192' \
  --set 'gpu.profiles[1].request.presets[0].corePercent=25' \
  --set 'gpu.profiles[1].request.presets[0].default=true' \
  --set 'gpu.profiles[1].dra.driver=hami-core-gpu.project-hami.io' \
  --set 'gpu.profiles[1].dra.deviceClassName=kube-dc-nvidia-a100-shared-8g' \
  --set 'gpu.profiles[1].dra.attributes.productName=Tesla V100-PCIE-32GB' \
  --set 'gpu.profiles[1].dra.memoryCapacity=memory' \
  --set 'gpu.profiles[1].dra.coreCapacity=cores' >"${tmp_dir}/dra-overlap.out" 2>"${tmp_dir}/dra-overlap.err"; then
  echo 'two DRA SKUs accepted overlapping device selectors' >&2
  exit 1
fi

helm template gpu-hami-only "${chart}" -f "${fixture}" \
  --set 'gpu.profiles[1].enabled=false' \
  --set-string 'gpu.kubevirtControllerUsername=' >"${tmp_dir}/hami-only.yaml"
require_present "${tmp_dir}/hami-only.yaml" 'name: kube-dc-hami-gpu-pods'
require_absent "${tmp_dir}/hami-only.yaml" 'name: hasVMResources'
require_absent "${tmp_dir}/hami-only.yaml" 'Whole-GPU VM resources may only be requested by KubeVirt launcher Pods.'

helm template gpu-dra-only "${chart}" -f "${dra_fixture}" >"${tmp_dir}/dra-only.yaml"
require_present "${tmp_dir}/dra-only.yaml" 'name: kube-dc-hami-gpu-pods'
require_present "${tmp_dir}/dra-only.yaml" \
  'object.metadata.labels["kube-dc.com/gpu-profile"] in ["nvidia-v100-hami"]'
require_absent "${tmp_dir}/dra-only.yaml" \
  'Shared GPU requests must use equal requests/limits and the selected profile'

helm template gpu-shared-create "${chart}" -f "${fixture}" \
  --set 'gpu.sharedCreation.enabled=true' >"${tmp_dir}/shared-create.yaml"
require_env_value "${tmp_dir}/shared-create.yaml" GPU_SHARED_CREATION_ENABLED true
require_env_value "${tmp_dir}/shared-create.yaml" GPU_VM_CREATION_ENABLED false

helm template gpu-vm-create "${chart}" -f "${fixture}" \
  --set 'gpu.vmCreation.enabled=true' >"${tmp_dir}/vm-create.yaml"
require_env_value "${tmp_dir}/vm-create.yaml" GPU_SHARED_CREATION_ENABLED false
require_env_value "${tmp_dir}/vm-create.yaml" GPU_VM_CREATION_ENABLED true

expect_render_failure zero-memory-step \
  --set 'gpu.profiles[0].request.memoryStepMiB=0'
expect_render_failure duplicate-native-resource \
  --set 'gpu.profiles[0].request.memoryResource=nvidia.com/gpu'
expect_render_failure invalid-preset-step \
  --set 'gpu.profiles[0].request.presets[0].memoryMiB=8500'
expect_render_failure multiple-default-presets \
  --set 'gpu.profiles[0].request.presets[1].default=true'
expect_render_failure missing-kubevirt-controller \
  --set-string 'gpu.kubevirtControllerUsername='
expect_render_failure missing-profile \
  --set 'gpu.profiles=[]'

echo 'GPU chart contracts passed: disabled, independently gated, fixture-enabled, and invalid catalogs'
