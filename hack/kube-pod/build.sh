#!/bin/bash

set -e

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

docker push ${FULL_IMAGE_NAME}

echo "Successfully built and pushed: ${FULL_IMAGE_NAME}"
