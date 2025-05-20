#!/bin/bash

set -e


helm upgrade --install demo oci://registry-1.docker.io/bitnamicharts/wordpress --namespace "shalb-dev" --values values.yaml