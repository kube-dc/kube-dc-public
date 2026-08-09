#!/usr/bin/env bash

# Read-only GPU runtime image provenance audit. It rejects implicit/latest
# desired references and requires every started container to report a resolved
# sha256 image ID. Feed a captured PodList for CI or query an approved cluster.

set -euo pipefail

pod_json=
namespaces=(gpu-operator hami-system)

usage() {
  echo "usage: $0 [--pod-json FILE] [--namespace NAME ...]" >&2
}

while (($# > 0)); do
  case "$1" in
    --pod-json)
      (($# >= 2)) || { usage; exit 2; }
      pod_json=$2
      shift 2
      ;;
    --namespace)
      (($# >= 2)) || { usage; exit 2; }
      if [[ ${namespaces_explicit:-false} != true ]]; then
        namespaces=()
        namespaces_explicit=true
      fi
      namespaces+=("$2")
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

for command in jq; do
  command -v "${command}" >/dev/null 2>&1 || {
    echo "required command is missing: ${command}" >&2
    exit 2
  }
done

if [[ -n "${pod_json}" ]]; then
  [[ -r "${pod_json}" ]] || { echo "PodList is not readable: ${pod_json}" >&2; exit 2; }
  inventory=$(jq -c '.' "${pod_json}")
else
  command -v kubectl >/dev/null 2>&1 || { echo "required command is missing: kubectl" >&2; exit 2; }
  : "${KUBECONFIG:?set KUBECONFIG to the approved cluster kubeconfig or use --pod-json}"
  inventory=$(
    for namespace in "${namespaces[@]}"; do
      kubectl --kubeconfig "${KUBECONFIG}" -n "${namespace}" get pods -o json
    done | jq -sc '{apiVersion:"v1",kind:"PodList",items:[.[].items[]]}'
  )
fi

jq -e 'type == "object" and (.items | type == "array")' <<<"${inventory}" >/dev/null || {
  echo "input is not a Kubernetes PodList" >&2
  exit 2
}

records=$(jq -c '
  def records($pod; $spec; $statuses; $kind):
    ($spec // [])[] as $container |
    (($statuses // []) | map(select(.name == $container.name)) | first // {}) as $status |
    {
      namespace: ($pod.metadata.namespace // "default"),
      pod: $pod.metadata.name,
      kind: $kind,
      container: $container.name,
      declared: $container.image,
      resolved: ($status.imageID // "")
    };
  [.items[] as $pod |
    records($pod; $pod.spec.initContainers; $pod.status.initContainerStatuses; "init"),
    records($pod; $pod.spec.containers; $pod.status.containerStatuses; "container"),
    records($pod; $pod.spec.ephemeralContainers; $pod.status.ephemeralContainerStatuses; "ephemeral")
  ]
' <<<"${inventory}")

count=$(jq 'length' <<<"${records}")
((count > 0)) || { echo "no GPU runtime containers found" >&2; exit 1; }

failures=0
printf 'NAMESPACE\tPOD\tKIND\tCONTAINER\tDECLARED\tRESOLVED\n'
while IFS=$'\t' read -r namespace pod kind container declared resolved; do
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "${namespace}" "${pod}" "${kind}" "${container}" "${declared}" "${resolved}"
  leaf=${declared##*/}
  if [[ -z "${declared}" || "${declared}" =~ :latest(@sha256:[0-9a-f]{64})?$ ]]; then
    echo "floating latest image is forbidden: ${namespace}/${pod}/${container}: ${declared}" >&2
    failures=$((failures + 1))
  elif [[ "${declared}" != *@sha256:* && "${leaf}" != *:* ]]; then
    echo "implicit latest image is forbidden: ${namespace}/${pod}/${container}: ${declared}" >&2
    failures=$((failures + 1))
  fi
  if [[ ! "${resolved}" =~ (@sha256:[0-9a-f]{64}|^(containerd|docker)://sha256:[0-9a-f]{64})$ ]]; then
    echo "runtime image is not resolved to sha256: ${namespace}/${pod}/${container}: ${resolved:-missing}" >&2
    failures=$((failures + 1))
  fi
done < <(jq -r '.[] | [.namespace,.pod,.kind,.container,.declared,.resolved] | @tsv' <<<"${records}")

((failures == 0)) || exit 1
printf 'PASS GPU runtime image provenance: containers=%s all desired references versioned and runtime IDs sha256-resolved\n' "${count}"
