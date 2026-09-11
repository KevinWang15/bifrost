#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
repo_dir="$PWD"
# Only this stack's containers and generated data are managed here.
mkdir -p "$repo_dir/tmp"
export BIFROST_ROUTING_TEST_DIR="$(mktemp -d "$repo_dir/tmp/routing-test.XXXXXX")"
cleanup() {
  docker compose -p bifrost-routing-test -f tests/routingresilience/compose.yml down
  rm -rf "$BIFROST_ROUTING_TEST_DIR"
}
trap cleanup EXIT
mkdir -p "$BIFROST_ROUTING_TEST_DIR/data"
cp tests/routingresilience/config.json "$BIFROST_ROUTING_TEST_DIR/data/config.json"
# Build with local modules. The temporary Go workspace does not alter go.mod files.
# A minimal embedded page suffices for these API tests when no UI build exists.
docker run --rm --network host -e HTTP_PROXY -e HTTPS_PROXY -e ALL_PROXY \
  -v "$repo_dir:/src" -v "$BIFROST_ROUTING_TEST_DIR:/result" \
  -v bifrost-routing-gomod:/go/pkg/mod -v bifrost-routing-gocache:/root/.cache/go-build \
  golang:1.27.0-bookworm bash -c '
    cd /tmp
    go work init /src/core /src/framework /src/transports /src/plugins/*
    export GOWORK=/tmp/go.work
    cd /src
    if [ ! -d transports/bifrost-http/ui ]; then
      mkdir -p transports/bifrost-http/ui
      echo "Routing API test server" > transports/bifrost-http/ui/index.html
      trap "rm -rf /src/transports/bifrost-http/ui" EXIT
    fi
    go build -o /result/bifrost ./transports/bifrost-http
  '
docker compose -p bifrost-routing-test -f tests/routingresilience/compose.yml up -d --wait
python3 tests/routingresilience/check.py
if [ "${RUN_PROVIDER_HARNESS:-0}" = 1 ]; then
  node tests/e2e/api/runners/augment-provider-harness.mjs \
    --source tests/e2e/api/collections/provider-harness.json --out "$BIFROST_ROUTING_TEST_DIR/augmented.json"
  node tests/e2e/api/runners/filter-collection.mjs \
    --source "$BIFROST_ROUTING_TEST_DIR/augmented.json" --out "$BIFROST_ROUTING_TEST_DIR/harness.json" --feature 'routing resilience'
  npx --yes newman@6.2.1 run "$BIFROST_ROUTING_TEST_DIR/harness.json" \
    --env-var baseUrl=http://127.0.0.1:28080 --env-var routingResilienceTests=true --reporters cli
fi
