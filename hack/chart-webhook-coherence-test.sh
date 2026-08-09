#!/usr/bin/env bash
# chart-webhook-coherence-test.sh — a chart that ships an admission webhook must
# default to a manager image that can actually serve it.
#
# WHY: the ValidatingWebhookConfiguration and the handler that answers it ship in
# two different artifacts. The chart renders the configuration; the manager image
# contains the code. Bump one without the other and the cluster ends up with a
# webhook pointed at a manager that has no such endpoint.
#
# For the #147 Service webhook that failure is SILENT by construction: it is
# registered failurePolicy: Ignore (deliberately — it intercepts a core resource
# and must not block tenant Services when the manager is down), so an
# unanswerable webhook does not error. It just lets every annotation change
# through, which is precisely the behaviour the release claims to prevent. The
# chart would look correct, the manager would look healthy, and the guarantee
# would be absent.
#
# So this asserts the two are a coherent pair:
#   1. the manager image tag actually rendered by the chart, and
#   2. that tag being at least the first manager release containing the handler.
#
# Run: hack/chart-webhook-coherence-test.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="$REPO_ROOT/charts/kube-dc"

# The first manager release that contains ServiceNetworkTypeWebhook. Raise this
# when a future handler needs a newer manager; never lower it.
MIN_MANAGER_WITH_SERVICE_WEBHOOK="v0.5.15"

fail() { echo "FAIL: $*"; exit 1; }
command -v helm >/dev/null || fail "helm not found"

rendered="$(helm template "$CHART" --set manager.webhook.enabled=true)" \
  || fail "chart does not render"

# Does this chart ship the Service network-type webhook at all?
if ! grep -q 'vservicenetworktype.kube-dc.com' <<<"$rendered"; then
  echo "SKIP: chart does not ship the Service network-type webhook"
  exit 0
fi

# The tag the chart ACTUALLY renders — values.manager.image.tag when set,
# otherwise .Chart.AppVersion. Read it from the rendered Deployment rather than
# re-deriving it, so the test cannot disagree with what gets installed.
manifest="$(mktemp)"
trap 'rm -f "$manifest"' EXIT
printf '%s' "$rendered" > "$manifest"
# Select by IMAGE REPOSITORY, not container name: this chart renders several
# deployments whose container is called "manager" (k8-manager, db-manager), and
# only shalb/kube-dc-manager serves this webhook. Matching on the name picked
# k8-manager and read its unrelated "latest" tag.
tag="$(python3 -c '
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") != "Deployment":
        continue
    for c in d["spec"]["template"]["spec"]["containers"]:
        repo, _, t = c["image"].rpartition(":")
        if repo.rsplit("/", 1)[-1] == "kube-dc-manager":
            print(t)
            raise SystemExit
' "$manifest")"
[ -n "$tag" ] || fail "could not read the rendered manager image tag"

# A dev suffix (v0.5.6-dev3) sorts before its base version under -V, but it is
# built from a superset of it, so compare on the base.
base="${tag%%-*}"

lowest="$(printf '%s\n%s\n' "$base" "$MIN_MANAGER_WITH_SERVICE_WEBHOOK" | sort -V | head -1)"
if [ "$lowest" != "$MIN_MANAGER_WITH_SERVICE_WEBHOOK" ] && [ "$base" != "$MIN_MANAGER_WITH_SERVICE_WEBHOOK" ]; then
  fail "the chart ships the #147 Service webhook but defaults the manager to ${tag}, which predates the handler (first release with it: ${MIN_MANAGER_WITH_SERVICE_WEBHOOK}).
Because that webhook is registered failurePolicy: Ignore, this does NOT fail loudly at runtime — every external-network-type change would silently pass, which is the exact bug the webhook exists to prevent.
Fix: bump appVersion in charts/kube-dc/Chart.yaml (or set manager.image.tag) to ${MIN_MANAGER_WITH_SERVICE_WEBHOOK} or later, and make sure that image is published."
fi

echo "ok: chart ships the Service network-type webhook and defaults the manager to ${tag} (>= ${MIN_MANAGER_WITH_SERVICE_WEBHOOK})"
echo "PASS: chart and manager image are a coherent pair"
