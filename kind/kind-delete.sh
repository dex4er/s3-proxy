#!/usr/bin/env bash
set -euo pipefail

config=${1:-$(dirname "$0")/kind-cluster.yaml}
name=$(yq .name "$config")

if [[ -z $name ]]; then
  echo "Cluster name not found in config"
  exit 1
fi

if [[ -d "$HOME/.kube/kind" ]]; then
  kubeconfig="$HOME/.kube/kind/$name"
  test -d "$kubeconfig" && exit 1
  rm -f "$kubeconfig"
else
  kubeconfig="$HOME/.kube/config"
fi

kind delete cluster --name "$name" --kubeconfig "$kubeconfig"

if [[ -f $kubeconfig && $kubeconfig != "$HOME/.kube/config" ]]; then
  rm -f "$kubeconfig"
fi
