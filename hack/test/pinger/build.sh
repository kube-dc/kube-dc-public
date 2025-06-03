#!/bin/bash
set -e

# Get tag from argument or use "latest" as default
TAG=${1:-latest}

# Build directory path
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$DIR"

echo "Building shalb/go-pinger:${TAG}..."

# Build the Docker image
docker build -t shalb/go-pinger:${TAG} .

echo "Pushing shalb/go-pinger:${TAG}..."

# Push the Docker image
docker push shalb/go-pinger:${TAG}

echo "Successfully built and pushed shalb/go-pinger:${TAG}"
