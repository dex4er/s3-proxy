#!/usr/bin/env bash
set -euxo pipefail

cd $(dirname "$0")

../helm.sh upgrade --install istio-base --repo https://istio-release.storage.googleapis.com/charts base -n istio-system
../helm.sh upgrade --install istiod --repo https://istio-release.storage.googleapis.com/charts istiod -n istio-system --values istiod.values.yaml
../helm.sh upgrade --install istio-ingress --repo https://istio-release.storage.googleapis.com/charts gateway -n istio-system --values gateway.values.yaml
../kubectl.sh apply -k .
