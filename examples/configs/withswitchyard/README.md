# Bifrost + Switchyard experiment

This example runs the current checkout's Bifrost API and a pinned Switchyard server
in Docker. Bifrost uses an OpenAI-compatible custom provider named `switchyard`.
No changes to Bifrost's Go request path are needed.

Read [RESULTS.md](RESULTS.md) for the measured integration results and
[DISCUSSION.md](DISCUSSION.md) for the routing design discussion, threshold tuning,
latency evidence, and proposed self-hosted classifier with full-context caching.
The [evidence snapshot](evidence/2026-09-28.json) preserves selected measurements
without credentials, account records, or request identifiers.

```text
Client → Bifrost :18473 → Switchyard :14000 → OpenRouter
                               ├─ judge: Qwen3 30B A3B Instruct
                               ├─ small: Qwen3.5 9B
                               └─ big: Qwen3.5 397B A17B
```

Switchyard source is pinned to `64def5647b711c3c3ce2840ed4feb42c85cea6db`.
The Bifrost Dockerfile builds the local Go modules, with a placeholder HTML page
instead of the full UI. Both published ports bind to localhost.

## Start

Save an OpenRouter API key in a file **outside the repository**, readable by uid
1000. Set `OPENROUTER_KEY_FILE` to that absolute path, either in the shell or in
an untracked `.env` beside this file. `.env.example` contains only the path setting.
The key is mounted as a Docker secret and is not copied into either image.

From this directory:

```bash
docker compose --profile test up -d --build bifrost mock
docker compose run --rm --no-deps -T probe probe.py
```

If dependency downloads require the host's existing HTTP proxy, build from the
repository root with the proxy as Docker build arguments, then use `--no-build`:

```bash
docker build --network host --build-arg HTTP_PROXY=http://127.0.0.1:7890 --build-arg HTTPS_PROXY=http://127.0.0.1:7890 -f examples/configs/withswitchyard/Dockerfile.switchyard -t bifrost-switchyard:64def5647b71 examples/configs/withswitchyard
docker build --network host --build-arg HTTP_PROXY=http://127.0.0.1:7890 --build-arg HTTPS_PROXY=http://127.0.0.1:7890 -f examples/configs/withswitchyard/Dockerfile.bifrost -t bifrost:switchyard-experiment .
```

`probe.py` only calls the local mock. It checks routing, Chat Completions SSE,
Responses, tool calls, session retention, upgrades/downgrades, invalid verdicts,
classifier failures, and Bifrost fallback recovery.

Run the small **paid** OpenRouter experiments explicitly:

```bash
docker compose run --rm --no-deps -T probe live.py
docker compose run --rm --no-deps -T probe live_tools.py
```

Results are written to the ignored `results/` directory. `live.py` is an integration
smoke test, not a quality benchmark; inspect finish reasons and actual answers.
The displayed account-usage difference can include unrelated traffic on the same key.

## Client configuration

Use `http://127.0.0.1:18473/v1` as the API base and `switchyard/smart` as the model.
The isolated experiment does not enforce client authentication. Send a unique
`x-switchyard-session-id` for each conversation, and resend the relevant history.

An OpenAI Chat Completions request body can include:

```json
{
  "model": "switchyard/smart",
  "messages": [{"role": "user", "content": "Explain this function."}],
  "fallbacks": ["switchyard/big"],
  "max_tokens": 512
}
```

The fallback invokes the fixed larger-model route if the classifier fails. It still
uses Switchyard's proxy; it does not cover Switchyard being unavailable. For that
case, configure a separate direct Bifrost provider as the fallback.

`switchyard/small` and `switchyard/big` bypass classification for baseline checks.
`switchyard/noop` checks connectivity without inference. The `test` and `test-big`
routes use the local mock and should be omitted from a deployed model pool.

Two Bifrost settings matter:

- `network_config.allow_private_network: true` permits the Switchyard container's
  private address for this provider.
- `client.header_filter_config.allowlist` forwards `x-switchyard-session-id`.
  The prefixed `x-bf-eh-x-switchyard-session-id` form also works. A plain
  `x-bf-session-id` is not automatically translated into Switchyard's header.

The routing policy explicitly uses `llm_classifier`, `mode = "capability"`, and
`classify_trigger = "user_turn"`. It keeps the model through a tool loop and judges
again on a new user message. Switchyard's `auto` route is a different, heuristic policy.
The 0.75 base threshold and 0.10 boundary increments are experimental settings,
not calibrated probabilities for these Qwen models.

## Inspect actual judge verdicts

The optional observer forwards buffered judge requests to OpenRouter and records
only verdicts, token usage, and elapsed time. It does not log credentials.

```bash
docker compose run --rm --no-deps -T probe -c 'from pathlib import Path; p=Path("routes.toml").read_text(); Path("results").mkdir(exist_ok=True); Path("results/routes.observe.toml").write_text(p.replace("https://openrouter.ai/api/v1", "http://judge-recorder:9001/v1", 1))'
docker compose -f compose.yaml -f compose.observe.yaml --profile test up -d --no-build switchyard judge-recorder
docker compose run --rm --no-deps -T probe inspect_routing.py
```

Run `live.py` first: the inspection script reuses three of its saved prompts and
adds a fixed larger-model baseline. Restore the direct path afterwards:

```bash
docker compose up -d --no-build switchyard
docker compose --profile test stop judge-recorder
```

## Self-hosted inference

Replace the judge and answer clients' `base_url` values with local OpenAI-compatible
servers, and set the target IDs to their served model names. Configure the judge
to return JSON in normal assistant content. Adjust `response_format_type` if its
server supports JSON Object mode instead of JSON Schema. Remove the OpenRouter
secret/entrypoint wrapper when it is no longer needed.

Bifrost loads empty local catalogs in this experiment and disables remote catalog
sync. This avoids catalog network dependencies, but also means the example does not
provide pricing estimates. Supply local catalogs for a real deployment. No local
model weights are installed here; the live experiment uses OpenRouter inference.

## Deployment limits

- NVIDIA labels the standalone Switchyard server as a demo/evaluation component.
  This Compose stack is a pilot, with in-memory session state and no HA testing.
- The packaged classifier prompt has a generic capability card. It is not trained
  on this smaller model's successes and failures. Routing quality needs evaluation.
- Invalid verdicts select the larger model. Judge HTTP errors and timeouts fail
  the request unless a Bifrost fallback is supplied.
- The response `model` and Bifrost's captured `X-Model-Router-Selected-Model` header
  identify the serving model. Bifrost's routing metadata still names provider
  `switchyard` and model `smart`. The judge is an extra inference call visible in
  Switchyard stats, outside Bifrost's per-model accounting/governance path.
- Native Responses continuations by ID can pin a serving model. Full-history
  requests are the appropriate starting point for routing between turns.
- Tool affinity does not make model-specific reasoning state portable. This pilot
  does not validate Codex WebSocket sessions or every client protocol extension.

Stop only this experiment with `docker compose --profile test down`.

Upstream references: [Switchyard status](https://github.com/NVIDIA-NeMo/Switchyard),
[classifier behavior](https://github.com/NVIDIA-NeMo/Switchyard/blob/64def5647b711c3c3ce2840ed4feb42c85cea6db/docs/routing_algorithms/llm_classifier_routing.md).
