# Experiment results — 2026-09-28

**Bifrost works with Switchyard through its existing custom-provider configuration.**
No Go changes were needed. Builds, servers, mock backends, HTTP probes, and live
inference experiments all ran in Docker.

## Versions and running services

- Bifrost checkout: `6a622ad5212d1968c0cdc128bcc6daecc70bc485`.
- Switchyard checkout: `64def5647b711c3c3ce2840ed4feb42c85cea6db`.
- Bifrost API: `http://127.0.0.1:18473/v1`, model `switchyard/smart`.
- Switchyard: `http://127.0.0.1:14000`.
- OpenRouter judge: `qwen/qwen3-30b-a3b-instruct-2507`.
- Smaller answer model: `qwen/qwen3.5-9b`.
- Larger answer model: `qwen/qwen3.5-397b-a17b`.

The temporary judge recorder has been stopped. The restored configuration sends
judge requests directly to OpenRouter. The API key is an external Docker secret;
an exact-value scan found no copy in the experiment files or saved results.

## Integration checks

All **15 deterministic checks passed**. They cover both model tiers, malformed
verdict recovery, Chat Completions and Responses streaming, buffered Responses,
direct and prefixed session headers, tool calls, tool-loop affinity, upgrades and
downgrades on later user turns, classifier HTTP failures/timeouts, and Bifrost
fallback recovery for both failures. See the committed
[evidence snapshot](evidence/2026-09-28.json). The complete local output is in
the ignored `results/mock.json` file.

The first attempt failed because Bifrost blocks private provider addresses by
default. Setting `network_config.allow_private_network = true` for the Switchyard
provider resolved it. Forwarding `x-switchyard-session-id` through the header
allowlist preserved session identity without a plugin.

Invalid classifier JSON selected the larger model. An injected classifier HTTP
500 returned 500, and a classifier exceeding its 2-second test deadline returned
504. Adding `fallbacks: ["switchyard/test-big"]` recovered both through a fixed
larger-model route. The corresponding live route is `switchyard/big`.

The session mode checks the last message's role; replaying a request ending in
user text triggers another classification. A tool continuation reuses the tier.
This is session routing, not deduplication of repeated client requests.

## Live inference

There were **16 successful answer requests and 12 routing-judge requests**, plus
one earlier structured-JSON preflight call. Live checks included SSE, Responses,
fixed-model baselines, and a real tool loop. That loop made one judge call and two
answer calls to the same model; the final answer was exactly the tool value `READY`.

The first six prompts used the packaged capability card, with `base_threshold =
0.75`, `threshold_step = 0.10`, and a six-message recent window:

| Prompt | Selected model | Total request time |
| --- | --- | ---: |
| Extract name and age | 9B | 12.504 s |
| Translate a greeting into Chinese | 397B | 11.382 s |
| Palindrome function with explicit examples | 9B | 8.088 s |
| Explain Python lambda late binding | 9B | 9.034 s |
| Atomic MPMC queue design | 9B | 9.625 s |
| All-pairs shortest paths with negative cycles | 9B | 7.666 s |

Across eight judged requests in the first batch, the judge alone averaged
**5.236 seconds**, with a 2.242–10.125-second range. This measures this OpenRouter
model/provider sample; it is not Switchyard's local processing overhead or a
prediction of local inference latency.

After allowing account usage to update, the observed increase was about
**$0.00573**, excluding the earlier tiny preflight charge. Account usage can lag
and can include unrelated requests using the same key; this is not a billing audit.

Selected measurements, synthetic prompts and answers, and recorded verdicts are
preserved in [evidence/2026-09-28.json](evidence/2026-09-28.json). Complete local
outputs remain ignored: `results/live.json`, `results/verdicts.json`,
`results/live-tools.json`, and `results/cost-summary.json`.

## Routing quality findings

The integration succeeds, but the default classifier is not validated for this
model pair. In repeated requests with the actual verdict recorded:

- The concurrency task received a valid `supported` verdict with `p_solve = 0.85`,
  selecting 9B. This was not a JSON parsing failure.
- The graph task received a valid `supported` verdict with `p_solve = 0.95`,
  selecting 9B. The qualitative card does not establish a measured 95% success rate.
- The translation selected 9B on the repeat, with `p_solve = 1.0`. Its first verdict
  was not recorded, so the original larger-model choice cannot be attributed
  conclusively to a probability estimate versus a fallback.

There were observable answer-quality problems. The initial extraction answer
wrapped the JSON in Markdown despite the JSON-only instruction. The initial graph
answer gave an incorrect negative-cycle propagation rule: it marked a pair as
negative infinity if the destination was reachable from a negative cycle and from
the source, without requiring the source to reach that cycle. Counterexample:
`u → v (5)`, `c → c (-1)`, `c → v (0)`. The correct `u → v` distance is 5.
The fixed larger-model baseline included the required reachability conditions.

The first concurrency answer hit the 512-token output cap. A repeat with 1024
tokens completed, but these small samples do not establish a general model ranking
or an accuracy/cost frontier. This experiment proves integration and identifies
policy weaknesses; it does not establish production routing quality.

## Follow-up measurements and tuning

Three lightweight authenticated OpenRouter API requests from the Docker probe
container took 576.61, 480.44, and 459.52 ms. Fresh DNS/TCP/TLS connections took
320.93, 308.27, and 293.97 ms of those totals. These requests made no inference
calls. They include API processing and exclude the onward inference-provider
path, so they do not isolate or explain the network share of the earlier 5.236 s
classifier average.

An offline replay used `base_threshold = 0.97` and `threshold_step = 0.01`:

| Recorded verdict | Score | Original policy | Candidate policy |
| --- | ---: | --- | --- |
| Translation, supported | 1.00 | Small | Small |
| Concurrency, supported | 0.85 | Small | Big |
| Graph algorithm, supported | 0.95 | Small | Big |

The [candidate configuration](evidence/routes.threshold-candidate.toml) passed
Switchyard's `--dry-run` inside Docker. It was not applied to the running service
and was not tested with fresh classifier calls. This is a policy replay, not an
accuracy benchmark or calibration of the judge's probabilities. The active
`routes.toml` remains at 0.75/0.10.

No experiment has yet measured a local judge, a 150k-token classifier request,
prefix-cache reuse, concurrent long sessions, or end-to-end quality on company
agent workloads. [DISCUSSION.md](DISCUSSION.md) records the proposed evaluation.

## Validation scope

This change adds an isolated example, probe scripts, and documentation; it changes
no Bifrost runtime implementation under `core/`, `framework/`, `transports/`, or
`plugins/`. It is exempt from adding a Bifrost provider-harness regression case.
The example has its own Docker-only deterministic integration checks. The live
calls reported above are historical observations, not required commit-time tests.

## Recommendation

Use this configuration as the integration starting point. Before rollout, evaluate
a model-specific capability card or trained classifier on representative tasks,
calibrate thresholds, and set an acceptable routing latency budget. Keep an
explicit larger-model fallback for classifier outages.

For production accounting, Bifrost currently sees `switchyard/smart` as the routing
destination. The returned model and captured selected-model header reveal the
answering model, but the judge call is outside Bifrost's accounting path. A future
decision-service integration using Switchyard's `/v1/decision` could keep final
provider execution inside Bifrost; that integration was not implemented here.

NVIDIA currently labels the standalone server as a demo/evaluation component.
The pilot has no HA, gateway authentication, load testing, or native Codex
WebSocket validation. Local model serving remains to be supplied: these live
experiments deliberately used OpenRouter.
