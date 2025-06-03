#!/bin/bash

set -e

REGISTRY="shalb"
IMAGE_NAME="kube-dc-kubectl"
TAG=${KUBE_DC_VERSION:-"v0.5.1"}

# Full image name
FULL_IMAGE_NAME="${REGISTRY}/${IMAGE_NAME}:${TAG}"

echo "Building image: ${FULL_IMAGE_NAME}"

# Build and push image
docker build \
    --compress \
    --tag ${FULL_IMAGE_NAME} \
    --build-arg BUILDKIT_INLINE_CACHE=1 \
    .

docker push ${FULL_IMAGE_NAME}

echo "Successfully built and pushed: ${FULL_IMAGE_NAME}"
