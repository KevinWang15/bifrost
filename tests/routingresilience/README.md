# OSS routing resilience container tests

Run from the repository root:

```bash
./tests/routingresilience/run.sh
```

Requires Docker Compose, Python 3 on the host and access to the Go 1.27.0 and
Python 3.12 container images. No local Go installation or provider credentials
are needed. The runner uses local Go modules and persistent Docker build caches.
A minimal embedded page is used if the UI has not been built; this suite verifies
API behavior, not the web UI.

The dedicated `bifrost-routing-test` project binds `127.0.0.1:28080` for Bifrost
and `127.0.0.1:29090` for the fake service. Do not run two copies concurrently.
The runner creates an isolated temporary configuration/database directory and
removes its containers and temporary data on exit. It does not manage other
container projects.

The fake service provides independent `a` and `b` upstreams, OpenAI-compatible
JSON and SSE replies, synthetic prefix-cache usage, request records and a control
endpoint for errors/delays. It never contacts a real LLM. Provider configurations
explicitly permit the private Docker network. Synthetic cache reuse is a routing
assertion, not a claim about actual provider cache hit rates.

## Coverage

- Codex dashed/underscore, Claude Code and explicit Bifrost session IDs.
- Same-session provider/model stability across repeated requests.
- Simulated cache reuse on the stable target.
- Cross-model fallback from one fake provider to the other.
- Subsequent sessions skip the failed route without another business attempt.
- Background probes restore the route before its 600-second cooldown.
- Successful fallback remains the session target after primary recovery.
- Streaming and non-streaming chat share the same target; an SSE fallback updates the binding.
- Responses JSON/SSE wire format, stable sessions, cross-model fallback and probe recovery.
- Request, authentication and rate-limit errors do not quarantine a model route.
- All-down requests fail fast, followed by automatic recovery.

The test config uses a 2-second probe interval to shorten the test; the production
default is 30 seconds. It uses a failure threshold of one rather than the default
three to make the initial failure deterministic.

## Standard provider harness

To run its cases before the runner tears down the stack (also requires Node/npm):

```bash
RUN_PROVIDER_HARNESS=1 ./tests/routingresilience/run.sh
```

The added `Routing resilience (fake OpenAI-compatible upstreams)` folder is opt-in.
With a running stack, set these Newman environment variables:

```text
baseUrl=http://127.0.0.1:28080
routingResilienceTests=true
```

Filter it with `--feature 'routing resilience'` using the existing
`tests/e2e/api/runners/filter-collection.mjs` script. The four requests check
initial binding, the next request's target, streaming consistency and expiry
through `x-bf-session-ttl`. The TTL case controls the fake upstream using
`fakeLLMUrl` (default `http://127.0.0.1:29090`). Without the
opt-in variable they skip and do not affect normal live-provider sweeps. The
Python suite additionally controls outages and asserts upstream call counts and
background recovery.

## Go regressions

With a Go workspace containing the local core, framework, transport and plugin
modules:

```bash
go test -race ./core/routingstrategy
go test ./core -run 'RoutingResilience|StreamFallback|StreamRetry|ExecuteRequestWithRetries'
go test ./plugins/routing/... ./plugins/governance/...
go test ./transports/bifrost-http/lib ./transports/bifrost-http/handlers -run 'RoutingResilience|SessionIDResolution|HarnessSession|ExplicitSessionID'
```

The outage regression was run before implementation: it failed because the dead
primary received two requests instead of one. The same test passes with the
policy enabled. WebSocket harness header recognition likewise failed before its
context-construction fix and passes afterward.
