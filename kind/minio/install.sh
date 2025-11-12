#!/usr/bin/env bash
set -euxo pipefail

cd $(dirname "$0")

../kubectl.sh apply -f persistentvolume.yaml
../helm.sh upgrade --install minio --repo https://charts.min.io/ minio -n minio --values values.yaml
../kubectl.sh apply -k .
