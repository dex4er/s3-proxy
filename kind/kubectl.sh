#!/usr/bin/env bash
set -euo pipefail

config="$(dirname "$0")/kind-cluster.yaml"
name=$(yq .name "$config")

if [[ -z $name ]]; then
  echo "Cluster name not found in config"
  exit 1
fi

if [[ -d "$HOME/.kube/kind" ]]; then
  kubeconfig="$HOME/.kube/kind/$name"
else
  kubeconfig="$HOME/.kube/config"
fi

kubectl --context "kind-$name" --kubeconfig "$kubeconfig" "$@"
