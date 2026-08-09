#!/usr/bin/env bash

# Read-only acceptance check for the operator-only Shared GPU Wave 0. It proves
# release readiness and zero tenant entitlement without creating or mutating a
# workload. Set MIN_READY_AGE_SECONDS=86400 for the G10-T02 exit check.

set -euo pipefail

for command in date jq kubectl yq; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

target=${GPU_PILOT_TARGET:?set GPU_PILOT_TARGET to the expected cluster name}
expected_chart=${GPU_PILOT_CHART:-v0.4.23}
expected_app_version=${GPU_PILOT_APP_VERSION:-v0.4.9}
expected_manager_tag=${GPU_PILOT_MANAGER_TAG:-gpu-pilot-e7f1df9b}
expected_backend_tag=${GPU_PILOT_BACKEND_TAG:-v0.4.9-1784121455}
expected_frontend_tag=${GPU_PILOT_FRONTEND_TAG:-v0.4.9-1784121455}
expected_admin_tag=${GPU_PILOT_ADMIN_TAG:-gpu-pilot-e501a729}
pilot_addon=${GPU_PILOT_ADDON:-gpu-v100-shared-8g}
min_ready_age=${MIN_READY_AGE_SECONDS:-0}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

pass() {
  echo "PASS: $*"
}

[[ "${min_ready_age}" =~ ^[0-9]+$ ]] || fail 'MIN_READY_AGE_SECONDS must be an integer'

cluster_name=$(kubectl -n flux-system get configmap cluster-config -o jsonpath='{.data.CLUSTER_NAME}')
[[ "${cluster_name}" == "${target}" ]] || fail "connected to ${cluster_name}, expected ${target}"
pass "target cluster is ${cluster_name}"

release=$(kubectl -n kube-dc get helmrelease kube-dc -o json)
ready=$(jq -r '.status.conditions[] | select(.type == "Ready") | .status' <<<"${release}")
chart=$(jq -r '.status.history[0].chartVersion // ""' <<<"${release}")
app_version=$(jq -r '.status.history[0].appVersion // ""' <<<"${release}")
ready_since=$(jq -r '.status.conditions[] | select(.type == "Ready") | .lastTransitionTime' <<<"${release}")
[[ "${ready}" == 'True' ]] || fail 'kube-dc HelmRelease is not Ready'
[[ "${chart}" == "${expected_chart}" ]] || fail "chart is ${chart}, expected ${expected_chart}"
[[ "${app_version}" == "${expected_app_version}" ]] || fail "appVersion is ${app_version}, expected ${expected_app_version}"
ready_age=$(( $(date -u +%s) - $(date -u -d "${ready_since}" +%s) ))
(( ready_age >= min_ready_age )) || fail "release Ready age ${ready_age}s is below ${min_ready_age}s"
pass "HelmRelease ${chart}/${app_version} has been Ready for ${ready_age}s"

declare -A repositories=(
  [kube-dc-manager]='shalb/kube-dc-manager'
  [kube-dc-backend]='shalb/kube-dc-ui-backend'
  [kube-dc-frontend]='shalb/kube-dc-ui-frontend'
  [kube-dc-admin-frontend]='shalb/kube-dc-admin-frontend'
)

declare -A tags=(
  [kube-dc-manager]="${expected_manager_tag}"
  [kube-dc-backend]="${expected_backend_tag}"
  [kube-dc-frontend]="${expected_frontend_tag}"
  [kube-dc-admin-frontend]="${expected_admin_tag}"
)

for deployment in "${!repositories[@]}"; do
  object=$(kubectl -n kube-dc get deployment "${deployment}" -o json)
  desired=$(jq -r '.spec.replicas' <<<"${object}")
  ready_replicas=$(jq -r '.status.readyReplicas // 0' <<<"${object}")
  image=$(jq -r '.spec.template.spec.containers[0].image' <<<"${object}")
  [[ "${ready_replicas}" == "${desired}" ]] || fail "${deployment} is ${ready_replicas}/${desired} Ready"
  [[ "${image}" == "${repositories[${deployment}]}:${tags[${deployment}]}" ]] || fail "${deployment} runs ${image}"
done
pass 'all four pilot Deployments are fully Ready on their expected immutable tags'

gpu_values=$(jq -c '.spec.values.gpu // {}' <<<"${release}")
jq -e '.enabled == true' <<<"${gpu_values}" >/dev/null || fail 'GPU catalog is not enabled'
jq -e '[.profiles[] | select(.enabled == true) | .billingEligible] | length > 0 and all(. == false)' <<<"${gpu_values}" >/dev/null \
  || fail 'an enabled GPU profile is billing eligible'
jq -e '.sharedCreation.enabled == false and .vmCreation.enabled == false' <<<"${gpu_values}" >/dev/null \
  || fail 'a GPU workload-creation gate is enabled'
pass 'catalog is discovery-only and both creation gates are false'

plans=$(kubectl -n kube-dc get configmap billing-plans -o jsonpath='{.data.plans\.yaml}')
addon_path=".addons.\"${pilot_addon}\""
yq -e "${addon_path}.selfService == false" <<<"${plans}" >/dev/null \
  || fail "${pilot_addon} is not explicitly operator-only"
yq -e "${addon_path}.gpu.\"nvidia-v100-hami\".shares == 1 and ${addon_path}.gpu.\"nvidia-v100-hami\".memoryMiB == 8192 and ${addon_path}.gpu.\"nvidia-v100-hami\".corePercent == 25" <<<"${plans}" >/dev/null \
  || fail "${pilot_addon} does not have the explicit pilot budget"

addons_response=$(kubectl -n kube-dc exec deployment/kube-dc-backend -- node -e \
  "fetch('http://127.0.0.1:3333/api/billing/addons').then(async r => { if (!r.ok) process.exit(1); process.stdout.write(await r.text()) })")
jq -e --arg addon "${pilot_addon}" '.success == true and ([.data[].id] | index($addon) == null)' <<<"${addons_response}" >/dev/null \
  || fail "${pilot_addon} is exposed by the tenant add-on API"
pass "${pilot_addon} is operator-only and hidden from the tenant API"

hrqs=$(kubectl get hierarchicalresourcequotas.hnc.x-k8s.io -A -o json)
jq -e '
  (.items | length) > 0 and
  all(.items[];
    .spec.hard["requests.nvidia.com/gpu"] == "0" and
    .spec.hard["requests.nvidia.com/gpumem"] == "0" and
    .spec.hard["requests.nvidia.com/gpucores"] == "0")
' <<<"${hrqs}" >/dev/null || fail 'an organization HRQ has missing or non-zero GPU quota'
pass "all $(jq -r '.items | length' <<<"${hrqs}") organization HRQs are GPU 0/0/0"

organizations=$(kubectl get organizations.kube-dc.com -o json)
jq -e --arg addon "${pilot_addon}" '
  all(.items[];
    (((.metadata.annotations // {}) | tostring | contains($addon)) | not) and
    (((.spec // {}) | tostring | contains($addon)) | not))
' <<<"${organizations}" >/dev/null || fail 'an organization has the pilot add-on assignment'

pods=$(kubectl get pods -A -o json)
active_gpu_pods=$(jq -r '[
  .items[] |
  select(.status.phase != "Succeeded" and .status.phase != "Failed") |
  select((
    .metadata.namespace == "gpu-operator" and (.metadata.name | startswith("hami-device-plugin-canary-"))
  ) | not) |
  select([.spec.containers[], (.spec.initContainers[]?)] | any(
    ((.resources.requests // {}) + (.resources.limits // {})) |
    keys[]? | test("^(requests\\.nvidia\\.com/|nvidia\\.com/gpu)")
  ))
] | length' <<<"${pods}")
(( active_gpu_pods == 0 )) || fail "${active_gpu_pods} active Pod(s) request GPU resources"

gpu_vms=$(kubectl get virtualmachines.kubevirt.io,virtualmachineinstances.kubevirt.io -A -o json | jq -r '[
  .items[] | select((.spec | tostring) | test("nvidia-v100|requests\\.nvidia\\.com|nvidia\\.com/gpu"))
] | length')
(( gpu_vms == 0 )) || fail "${gpu_vms} VM/VMI object(s) reference GPU resources"
pass 'zero organization assignments and zero active tenant GPU holders'

expected_alerts=(
  KubeDcHamiDevicePluginMetricsUnavailable
  KubeDcHamiSchedulerUnavailable
  KubeDcHamiAllocationCanaryStale
  KubeDcHamiAllocationCanaryStuck
  KubeDcGPUNodeWrongWorkloadMode
  KubeDcConflictingNvidiaDevicePlugins
  KubeDcGPUDcgmExporterUnavailable
  KubeDcGPUMandatoryTelemetryUnavailable
  KubeDcGPUCapacityTelemetryUnavailable
  KubeDcGPUXidError
  KubeDcGPUUncorrectableEccError
  KubeDcGPUOverTemperature
  KubeDcGPUThrottlingViolation
  KubeDcOrganizationQuotaConfigInvalid
  KubeDcOrganizationQuotaConfigUnobserved
  KubeDcOrganizationQuotaReconcileErrors
)

prometheus_proxy='/api/v1/namespaces/monitoring/services/prom-operator-prometheus:9090/proxy'
rules=$(kubectl get --raw "${prometheus_proxy}/api/v1/rules")
for alert_name in "${expected_alerts[@]}"; do
  jq -e --arg name "${alert_name}" '
    [.data.groups[].rules[] | select(.type == "alerting" and .name == $name)] as $matches |
    ($matches | length) == 1 and
    all($matches[];
      ((.labels.severity // "") | length) > 0 and
      ((.labels.service // "") | length) > 0 and
      ((.labels.owner // "") | length) > 0 and
      ((.annotations.runbook_url // "") | startswith("https://")))
  ' <<<"${rules}" >/dev/null || fail "${alert_name} is missing, duplicated, unowned, or has no resolvable runbook URL"
done
pass "all ${#expected_alerts[@]} required GPU/quota alert rules are loaded and owned"

gpu_record_rule_count=$(jq -r '[
  .data.groups[].rules[] |
  select(.type == "recording" and (.name | startswith("kube_dc:gpu_")))
] | length' <<<"${rules}")
(( gpu_record_rule_count == 12 )) || fail "loaded ${gpu_record_rule_count}/12 GPU recording rules"

quota_record_rule_count=$(jq -r '[
  .data.groups[].rules[] |
  select(.type == "recording" and (.name | startswith("kube_dc:organization_quota_reconcile_")))
] | length' <<<"${rules}")
(( quota_record_rule_count == 2 )) || fail "loaded ${quota_record_rule_count}/2 quota reconciliation recording rules"
pass 'all GPU and quota reconciliation recording rules are loaded'

alerts=$(kubectl get --raw "${prometheus_proxy}/api/v1/alerts")
active_kube_dc_alerts=$(jq -r '[.data.alerts[] | select(.labels.alertname | startswith("KubeDc"))] | length' <<<"${alerts}")
(( active_kube_dc_alerts == 0 )) || {
  jq -r '.data.alerts[] | select(.labels.alertname | startswith("KubeDc")) | [.labels.alertname,.state] | @tsv' <<<"${alerts}" >&2
  fail "${active_kube_dc_alerts} kube-dc alert(s) are active"
}
pass 'all kube-dc alerts are inactive'

echo "Wave 0 observation passed at $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
