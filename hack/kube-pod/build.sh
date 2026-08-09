#!/bin/bash

set -e

# Build from THIS directory, whatever the caller's cwd.
#
# The build context and Dockerfile were `.`, so running the script from the repo root
# (the obvious thing to do — `bash hack/kube-pod/build.sh`) built the repo-root
# Dockerfile, which is the MANAGER. It then pushed that manager image under the
# cloud-shell tag: a "successfully built and pushed" message, a valid image, and no
# kubectl or kube-dc anywhere in it. Verified by exec'ing the pushed tag, which is the
# only check that would have caught it.
cd "$(dirname "${BASH_SOURCE[0]}")"

REGISTRY="shalb"
IMAGE_NAME="kube-dc-kubectl"
TAG=${KUBE_POD_TAG:-"v0.6.8"}
KUBE_DC_CLI_VERSION=${KUBE_DC_CLI_VERSION:?set KUBE_DC_CLI_VERSION to an immutable CLI release tag}

# Full image name
FULL_IMAGE_NAME="${REGISTRY}/${IMAGE_NAME}:${TAG}"

echo "Building image: ${FULL_IMAGE_NAME}"

# Build and push image
docker build \
    --compress \
    --tag "${FULL_IMAGE_NAME}" \
    --build-arg KUBE_DC_VERSION="${KUBE_DC_CLI_VERSION}" \
    --build-arg BUILDKIT_INLINE_CACHE=1 \
    .

# Verify the image IS the cloud shell before publishing it. A wrong build context
# produces a perfectly valid image under this tag, and the backend hardcodes the tag —
# so an unverified push ships a cloud shell with no shell.
echo "Verifying ${FULL_IMAGE_NAME} contains the expected binaries..."
docker run --rm --entrypoint /usr/local/bin/kubectl "${FULL_IMAGE_NAME}" version --client >/dev/null \
  || { echo "FAIL: no kubectl in ${FULL_IMAGE_NAME} — wrong build context?" >&2; exit 1; }
# `kube-dc version` prints "kube-dc CLI 0.5.15" — no leading v — so match it with the v
# optional, and assert the version is the one REQUESTED. Checking only that some binary
# responds would pass an image built against a stale CLI release.
built_cli="$(docker run --rm --entrypoint /usr/local/bin/kube-dc "${FULL_IMAGE_NAME}" version 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ -n "$built_cli" ] \
  || { echo "FAIL: no kube-dc CLI in ${FULL_IMAGE_NAME}" >&2; exit 1; }
[ "${built_cli#v}" = "${KUBE_DC_CLI_VERSION#v}" ] \
  || { echo "FAIL: ${FULL_IMAGE_NAME} carries CLI ${built_cli}, requested ${KUBE_DC_CLI_VERSION}" >&2; exit 1; }
echo "  kubectl present; kube-dc reports ${built_cli} (requested ${KUBE_DC_CLI_VERSION})"

docker push ${FULL_IMAGE_NAME}

echo "Successfully built and pushed: ${FULL_IMAGE_NAME}"
