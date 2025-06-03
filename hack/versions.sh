#!/bin/bash
set -e

NAMESPACE="kube-dc"

if ! command -v kubectl 2>&1 >/dev/null
then
    echo "Error: command 'kubectl' could not be found. Auto update is not possible" > /dev/stderr
    exit 1
fi

appList=("frontend" "backend" "manager")

# Loop through each string in the list
for item in "${appList[@]}"; do

  appVer=$(kubectl get deployment -n ${NAMESPACE} kube-dc-${item} -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $2}')
  if [ -z ${appVer+x} ]; then 
    echo "deployed release 'kube-dc-${item}' could not be found" > /dev/stderr
    continue
  fi
  echo -e "App \033[1;92m${item}\033[0m: version \033[1;92m${appVer}\033[0m"
done



