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

output=$(helm diff --kube-context "kind-$name" --kubeconfig "$kubeconfig" "$@" --allow-unreleased)

if [[ -n $output ]]; then
  helm --kube-context "kind-$name" --kubeconfig "$kubeconfig" "$@" --create-namespace --wait
fi
