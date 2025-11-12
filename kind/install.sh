#!/usr/bin/env bash
set -euxo pipefail

cd $(dirname "$0")

./kubectl.sh version || { ./kind-create.sh && ./kubectl.sh version; }
./istio/install.sh
./minio/install.sh
./proxy/install.sh
