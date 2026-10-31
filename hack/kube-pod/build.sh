#!/bin/bash

set -euo pipefail

# Build from THIS directory, whatever the caller's cwd.
#
# The build context and Dockerfile were `.`, so running the script from the repo root
# (the obvious thing to do — `bash hack/kube-pod/build.sh`) built the repo-root
# Dockerfile, which is the MANAGER. It then pushed that manager image under the
# cloud-shell tag: a "successfully built and pushed" message, a valid image, and no
# kubectl or kube-dc anywhere in it. Verified by exec'ing the pushed tag, which is the
# only check that would have caught it.
cd "$(dirname "${BASH_SOURCE[0]}")"
PUSH=1
if [[ ${1:-} == --no-push ]]; then PUSH=0; shift; fi
[[ $# == 0 ]] || { echo 'usage: build.sh [--no-push]' >&2; exit 1; }
REVISION=$(git rev-parse HEAD)
[[ -z $(git status --porcelain) ]] || {
    echo 'Commit cloud-shell source before building a release image' >&2; exit 1;
}
if [[ $PUSH == 1 ]]; then
    git fetch -q origin
    git merge-base --is-ancestor "$REVISION" origin/main || {
        echo 'Published cloud-shell source must be on origin/main' >&2; exit 1;
    }
fi

REGISTRY="shalb"
IMAGE_NAME="kube-dc-kubectl"
TAG=${KUBE_POD_TAG:?set KUBE_POD_TAG to an unused immutable image tag}
KUBE_DC_CLI_VERSION=${KUBE_DC_CLI_VERSION:?set KUBE_DC_CLI_VERSION to an immutable CLI release tag}

# Full image name
FULL_IMAGE_NAME="${REGISTRY}/${IMAGE_NAME}:${TAG}"
if [[ $PUSH == 1 ]]; then
    status=$(curl -sS -o /dev/null -w '%{http_code}' "https://hub.docker.com/v2/repositories/${REGISTRY}/${IMAGE_NAME}/tags/${TAG}")
    [[ $status == 404 ]] || {
        echo "Refusing existing or unverifiable image tag ${FULL_IMAGE_NAME} (HTTP ${status})" >&2; exit 1;
    }
fi

build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
./tools/build-go-tools.sh "$build_dir"
python3 ./tools/prepare-runtime.py "$build_dir" --cli-version "$KUBE_DC_CLI_VERSION"
[[ $(git rev-parse HEAD) == "$REVISION" && -z $(git status --porcelain) ]] || {
    echo 'Cloud-shell source changed during compilation' >&2; exit 1;
}

echo "Building image: ${FULL_IMAGE_NAME}"

# Build and push image
docker build \
    --platform linux/amd64 \
    --compress \
    --tag "${FULL_IMAGE_NAME}" \
    --label "org.opencontainers.image.revision=${REVISION}" \
    --label "org.opencontainers.image.source=https://github.com/shalb/kube-dc" \
    --label "io.kube-dc.cli.version=${KUBE_DC_CLI_VERSION}" \
    --build-arg BUILDKIT_INLINE_CACHE=1 \
    .

# Verify the image IS the cloud shell before publishing it. A wrong build context
# produces a perfectly valid image under this tag, and the backend hardcodes the tag —
# so an unverified push ships a cloud shell with no shell.
echo "Verifying ${FULL_IMAGE_NAME} contains the expected binaries..."
docker run --rm --network none --entrypoint /usr/local/bin/kubectl "${FULL_IMAGE_NAME}" version --client >/dev/null \
  || { echo "FAIL: no kubectl in ${FULL_IMAGE_NAME} — wrong build context?" >&2; exit 1; }
# `kube-dc version` prints "kube-dc CLI 0.5.15" — no leading v — so match it with the v
# optional, and assert the version is the one REQUESTED. Checking only that some binary
# responds would pass an image built against a stale CLI release.
built_cli="$(docker run --rm --network none --entrypoint /usr/local/bin/kube-dc "${FULL_IMAGE_NAME}" version 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ -n "$built_cli" ] \
  || { echo "FAIL: no kube-dc CLI in ${FULL_IMAGE_NAME}" >&2; exit 1; }
[ "${built_cli#v}" = "${KUBE_DC_CLI_VERSION#v}" ] \
  || { echo "FAIL: ${FULL_IMAGE_NAME} carries CLI ${built_cli}, requested ${KUBE_DC_CLI_VERSION}" >&2; exit 1; }
echo "  kubectl present; kube-dc reports ${built_cli} (requested ${KUBE_DC_CLI_VERSION})"

docker run --rm --network none --entrypoint /bin/bash "$FULL_IMAGE_NAME" -ec \
    'helm version --short; stern --version; k9s version; virtctl version --client; ttyd --version; bash -n /scripts/startup.sh'

if [[ $PUSH == 0 ]]; then
    echo "Built and smoke-tested ${FULL_IMAGE_NAME}; publication disabled"
    exit 0
fi
docker push "${FULL_IMAGE_NAME}"

echo "Successfully built and pushed: ${FULL_IMAGE_NAME}"
