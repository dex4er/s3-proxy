#!/usr/bin/env bash
set -euxo pipefail

cd $(dirname "$0")

../kubectl.sh apply -f persistentvolume.yaml
if ../kubectl.sh get pv minio --no-headers | grep -qs "Released"; then
  ../kubectl.sh patch pv minio -p '{"spec":{"claimRef": null}}'
fi
../helm.sh upgrade --install minio --repo https://charts.min.io/ minio -n minio --values values.yaml
../kubectl.sh apply -k .
