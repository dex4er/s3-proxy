#!/usr/bin/env bash
set -euxo pipefail

cd $(dirname "$0")

../helm.sh upgrade --install istio-base --repo https://istio-release.storage.googleapis.com/charts base -n istio-system --create-namespace --wait
../helm.sh upgrade --install istiod --repo https://istio-release.storage.googleapis.com/charts istiod -n istio-system --wait
../helm.sh upgrade --install istio-ingress --repo https://istio-release.storage.googleapis.com/charts gateway -n istio-system --wait --values gateway.values.yaml
../kubectl.sh apply -k .
