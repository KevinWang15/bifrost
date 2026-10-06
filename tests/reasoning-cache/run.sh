#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
case "${1:-}" in
  ""|--harness) ;;
  *) printf 'Usage: %s [--harness]\n' "$0" >&2; exit 2 ;;
esac
mkdir -p tmp/reasoning-cache
# Build with local modules. The workspace and UI placeholder are ignored artifacts.
docker run --rm --network host \
  -e HTTPS_PROXY -e HTTP_PROXY -e ALL_PROXY -e NO_PROXY \
  -v "$PWD:/workspace" \
  -v bifrost-routing-gomod:/go/pkg/mod \
  -v bifrost-routing-gocache:/root/.cache/go-build \
  -w /workspace golang:1.27.0-bookworm bash -eu -c '
    if [ ! -f go.work ]; then
      go work init ./core ./framework ./transports
      for module in plugins/*; do
        if [ -f "$module/go.mod" ]; then go work use "$module"; fi
      done
    fi
    if [ ! -d transports/bifrost-http/ui ]; then
      mkdir -p transports/bifrost-http/ui
      printf "<html>Local integration test</html>" > transports/bifrost-http/ui/index.html
    fi
    cd transports
    go build -o ../tmp/reasoning-cache/bifrost ./bifrost-http
  '
compose=(docker compose -p bifrost-reasoning-cache -f tests/reasoning-cache/compose.yml)
trap '"${compose[@]}" down --remove-orphans' EXIT
"${compose[@]}" up -d --force-recreate providers gateway
"${compose[@]}" run --rm verify
if [ "${1:-}" = --harness ]; then
  docker run --rm --network host \
    -e HTTPS_PROXY -e HTTP_PROXY -e ALL_PROXY -e NO_PROXY \
    -v "$PWD:/workspace" -w /workspace node:22-alpine \
    npm install --prefix tmp/reasoning-cache/harness-tools newman@6.2.1 --no-audit --no-fund
  docker run --rm -v "$PWD:/workspace" -w /workspace node:22-alpine \
    node tests/e2e/api/runners/augment-provider-harness.mjs \
    --source tests/e2e/api/collections/provider-harness.json --out tmp/reasoning-cache/harness-augmented.json
  docker run --rm -v "$PWD:/workspace" -w /workspace node:22-alpine \
    node tests/e2e/api/runners/filter-collection.mjs \
    --source tmp/reasoning-cache/harness-augmented.json --out tmp/reasoning-cache/harness-filtered.json \
    --provider openai --feature 'reasoning rejection cache'
  docker run --rm --network bifrost-reasoning-cache_default \
    -v "$PWD:/workspace" -w /workspace node:22-alpine \
    /workspace/tmp/reasoning-cache/harness-tools/node_modules/.bin/newman run \
    tmp/reasoning-cache/harness-filtered.json \
    --env-var baseUrl=http://gateway:8080 --env-var reasoningCacheProviderURL=http://providers:9000 \
    --env-var setupToken=local-reasoning-cache-setup \
    --env-var include_preview=1 --reporters cli,json \
    --reporter-json-export tmp/reasoning-cache/newman-report.json
fi
