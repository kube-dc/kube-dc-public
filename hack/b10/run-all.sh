#!/usr/bin/env bash
#
# Run the B-10 acceptance pass in order, stopping at the first failure.
#
# If it fails: fix ONLY the observed blocker, cut RC2, and rerun the WHOLE pass.
# Not the failed step — the point of an acceptance run is that the bundle passed
# together, and a partial rerun does not establish that.

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

STEPS=(00-preflight.sh 10-absent.sh 20-declare.sh 30-pod.sh 40-vm.sh 50-breach.sh 70-teardown.sh)
[ "${B10_INCLUDE_RESILIENCE:-no}" = "yes" ] && STEPS+=(60-resilience.sh)

{
  echo "# B-10 acceptance run"
  echo
  echo "- started: $(date -u +%FT%TZ)"
  echo "- cluster: $(kubectl config current-context 2>/dev/null)"
  echo "- manager: $(kubectl get deploy kube-dc-manager -n kube-dc -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)"
  echo "- backend: $(kubectl get deploy kube-dc-backend -n kube-dc -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)"
  echo "- commit:  $(git -C ../.. rev-parse --short HEAD 2>/dev/null)"
  echo
  echo "| time | result | check |"
  echo "|---|---|---|"
} > RESULTS.md

for s in "${STEPS[@]}"; do
  echo
  echo "════════ $s ════════"
  if ! bash "$s"; then
    echo
    echo "STOPPED at $s — see RESULTS.md"
    echo "Run 99-cleanup.sh before retrying."
    exit 1
  fi
done

echo
echo "════════ B-10 PASSED ════════"
echo "Results in $(pwd)/RESULTS.md"
