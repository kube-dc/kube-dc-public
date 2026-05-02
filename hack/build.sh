#!/bin/bash

set -x
set -e

if [ -z ${KUBE_DC_VERSION+x} ]; then 
  echo "KUBE_DC_VERSION is not set, getting last git tag"
  KUBE_DC_VERSION="$(git describe --tag --abbrev=0)" 
fi

export KUBE_DC_VERSION
export REGISTRY_URL=registry-1.docker.io
export REGISTRY_REPO=shalb

if [ -z ${KUBE_DC_VERSION+x} ]; then 
  echo "can't set KUBE_DC_VERSION automatically"
  exit 1
fi


path=$(dirname -- "$( readlink -f -- "$0"; )")

frontendPath=$(cd -- "${path}/../ui/frontend" &> /dev/null && pwd) 
backendPath=$(cd -- "${path}/../ui/backend" &> /dev/null && pwd)
chartPath=$(cd -- "${path}/../charts" &> /dev/null && pwd)
kubePodPath=$(cd -- "${path}/kube-pod" &> /dev/null && pwd)
rootPath=$(cd -- "${path}/../" &> /dev/null && pwd)

cd "${backendPath}"
docker build -t ${REGISTRY_REPO}/kube-dc-ui-backend:${KUBE_DC_VERSION} --push .

cd "${frontendPath}"
docker build -t ${REGISTRY_REPO}/kube-dc-ui-frontend:${KUBE_DC_VERSION} --push .

cd "${rootPath}"
docker build -t ${REGISTRY_REPO}/kube-dc-manager:${KUBE_DC_VERSION} --push .

cd "${kubePodPath}"
docker build -t ${REGISTRY_REPO}/kube-dc-kubectl:${KUBE_DC_VERSION} --push .

cd "${chartPath}"
# Chart.yaml + values.yaml are the source of truth — committed and
# edited directly. The earlier Chart-template.yaml + values-template.yaml
# + envsubst indirection only existed to inject ${KUBE_DC_VERSION} into
# the image tag on three Deployments; the deployment templates now handle
# this via `.Values.<comp>.image.tag | default .Chart.AppVersion`, and
# `helm package --version/--app-version` keep both in sync with the
# release tag.
helm package --version "${KUBE_DC_VERSION}" --app-version "${KUBE_DC_VERSION}" kube-dc
helm push kube-dc-"${KUBE_DC_VERSION}".tgz oci://"${REGISTRY_URL}"/"${REGISTRY_REPO}"



