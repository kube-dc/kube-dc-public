#!/bin/bash
set -e

increment_version() {
  input_version=$1
  if [[ $input_version =~ ^(v[0-9]+\.[0-9]+\.[0-9]+)(-dev([0-9]+))?$ ]]; then
    base_version=${BASH_REMATCH[1]}
    dev_suffix=${BASH_REMATCH[3]}

    if [[ -n $dev_suffix ]]; then
      # If a dev suffix exists, increment the number
      new_dev_suffix=$((dev_suffix + 1))
      echo "${base_version}-dev${new_dev_suffix}"
    else
      # If no dev suffix, add "-dev1"
      echo "${base_version}-dev1"
    fi
  else
    echo "Invalid version format" > /dev/stderr
    exit 1
  fi
}

NAMESPACE="kube-dc"

if ! command -v kubectl 2>&1 >/dev/null
then
    echo "Error: command 'kubectl' could not be found. Auto update is not possible" > /dev/stderr
    exit 1
fi

KUBE_DC_VERSION=$(kubectl get deployment -n ${NAMESPACE} kube-dc-backend -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $2}')
# KUBE_DC_VERSION=$(helm get metadata -n ${NAMESPACE} kube-dc | grep APP_VERSION | awk '{print $2}')

if [ -z ${KUBE_DC_VERSION+x} ]; then 
  echo "deployed release 'kube-dc' could not be found. Auto update is not possible" > /dev/stderr
  exit 1 
fi

export KUBE_DC_VERSION
export REGISTRY_URL=registry-1.docker.io
export REGISTRY_REPO=shalb

if [ -z ${KUBE_DC_VERSION+x} ]; then 
  echo "can't set KUBE_DC_VERSION automatically" > /dev/stderr
  exit 1
fi

new_version=$(increment_version "${KUBE_DC_VERSION}")


# Print the generated version and prompt for confirmation
echo -e "New version tag: \033[1;92m${new_version}\033[0m"
read -p "Do you want to build and update with this version? (y/n): " user_input

# Act based on user input
if [[ ! ${user_input} == "y" && ! ${user_input} == "Y" ]]; then
  echo "Operation canceled."
  exit 0
fi
echo "Proceeding with version: ${new_version}"

path=$(dirname -- "$( readlink -f -- "$0"; )")

frontendPath=$(cd -- "${path}/../ui/frontend" &> /dev/null && pwd) 
backendPath=$(cd -- "${path}/../ui/backend" &> /dev/null && pwd)

cd "${backendPath}"
docker build -t ${REGISTRY_REPO}/kube-dc-ui-backend:${new_version} .
docker push ${REGISTRY_REPO}/kube-dc-ui-backend:${new_version}
kubectl set image -n ${NAMESPACE} deployment/kube-dc-backend kube-dc=${REGISTRY_REPO}/kube-dc-ui-backend:${new_version}

cd "${frontendPath}"
docker build -t ${REGISTRY_REPO}/kube-dc-ui-frontend:${new_version} .
docker push ${REGISTRY_REPO}/kube-dc-ui-frontend:${new_version}
kubectl set image -n ${NAMESPACE} deployment/kube-dc-frontend kube-dc=${REGISTRY_REPO}/kube-dc-ui-frontend:${new_version}




