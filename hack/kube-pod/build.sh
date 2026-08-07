#!/bin/bash

set -e

REGISTRY="shalb"
IMAGE_NAME="kube-dc-kubectl"
# Image tag and the CLI version baked inside are SEPARATE axes: the image
# line (v0.6.x) advances on every rebuild, the CLI line (v0.5.x) is what
# gets downloaded and checksum-verified in the Dockerfile. Conflating them
# (the old TAG=${KUBE_DC_VERSION} form) made it impossible to express
# "kube-pod v0.6.17 built against CLI v0.5.14" — the pairing release-set
# records under kube-pod.{tag,cli-version}.
TAG=${KUBE_POD_TAG:-"v0.6.5"}
CLI_VERSION=${KUBE_DC_VERSION:?set KUBE_DC_VERSION to the CLI release to bake in (e.g. v0.5.14)}

# Full image name
FULL_IMAGE_NAME="${REGISTRY}/${IMAGE_NAME}:${TAG}"

echo "Building image: ${FULL_IMAGE_NAME} (bundling kube-dc CLI ${CLI_VERSION})"

# Build and push image
docker build \
    --compress \
    --tag ${FULL_IMAGE_NAME} \
    --build-arg BUILDKIT_INLINE_CACHE=1 \
    --build-arg KUBE_DC_VERSION=${CLI_VERSION} \
    .

docker push ${FULL_IMAGE_NAME}

echo "Successfully built and pushed: ${FULL_IMAGE_NAME}"
