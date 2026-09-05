# Bifrost cache-hit reporting stack

This development stack demonstrates daily cache-hit reporting without paid Grafana features. It runs the current Bifrost checkout, Bifrost's embedded Chromem cache, an OpenAI-compatible mock upstream (chat and embeddings), Prometheus, a provisioned Grafana dashboard, a small SQLite-backed reporter, Mailpit as the local email inbox, and a signed mock Feishu webhook. Chromem keeps the example self-contained; use a shared production vector store when running multiple Bifrost replicas.

## Why a small reporter?

Prometheus stores and queries the counters, and Grafana OSS visualizes them. Prometheus/Alertmanager is intended for alert state transitions rather than scheduled analytical reports. Grafana's native scheduled PDF/CSV email reporting is an Enterprise/Cloud feature. The reporter therefore owns only the missing pieces:

- query the previous 24 hours with PromQL `increase()` so Bifrost restarts are handled;
- compute the aggregate hit rate and a per-user breakdown;
- assess cache-miss severity centrally, using configurable thresholds and minimum traffic;
- render HTML/text once and archive those exact artifacts with the report in SQLite;
- generate a report only when `last_generated_at` is absent or at least 24 hours old;
- deliver independently through pluggable email, Feishu, and JSON webhook integrations.

References: [Prometheus range/instant query API](https://prometheus.io/docs/prometheus/latest/querying/api/), [Alertmanager notification behavior](https://prometheus.io/docs/alerting/latest/configuration/), and [Grafana scheduled reporting requirements](https://grafana.com/docs/grafana/latest/visualizations/dashboards/create-reports/).

Bifrost already exposes the necessary counters:

- `bifrost_cache_hits_total{cache_type="direct|semantic", ...}` is the numerator;
- `bifrost_upstream_requests_total{...}` is the denominator. Despite its historical name, the telemetry hook increments it once for every completed LLM request, including Bifrost-local cache hits;
- built-in labels include virtual key, team, customer, business unit, project, provider, and model.

The demo also configures the custom `user_id` label. Traffic supplies it as `x-bf-dim-user_id`, which is an observability dimension and must not be treated as an authentication identity. In a production OSS deployment, a trusted proxy should set or overwrite that header. If virtual keys represent users, use `REPORT_GROUP_BY=virtual_key_id` and remove the custom label instead. Enterprise-authenticated user IDs are deliberately not a default Prometheus label because unbounded user cardinality can be expensive; use the log-store stats API when exact trusted per-user attribution is required.

## Start it

From this directory:

```bash
docker compose up -d --build
docker compose ps
```

The first local build compiles Bifrost and its UI from the checkout and can take several minutes. Startup then creates deterministic demo traffic: Alice has 3/4 measured hits, Bob has 2/4, and the aggregate measured rate is approximately 62.5%. The reporter waits for that traffic to be scraped before sending its initial report.

Open:

- Bifrost: <http://localhost:8080>
- Prometheus: <http://localhost:9090>
- Grafana: <http://localhost:3000> (`admin` / `admin`), dashboard **Bifrost / Bifrost cache hit reporting**
- Mailpit inbox: <http://localhost:8025>
- Reporter archive: <http://localhost:8091/reports>
- Latest HTML report: <http://localhost:8091/reports/latest/view>
- Mock Feishu messages: <http://localhost:8092/messages/development>

Inspect the latest persisted report:

```bash
curl -s http://localhost:8091/reports/latest | python -m json.tool
```

Force an additional report without waiting 24 hours:

```bash
docker compose stop reporter
docker compose run --rm --no-deps reporter --once --force
docker compose start reporter
```

Run the reporter's unit tests:

```bash
docker run --rm -v "$PWD/reporter:/app" -w /app python:3.14.7-alpine3.24 \
  python -m unittest -v test_reporter.py
```

Run the full stack test after the services are up:

```bash
python e2e/test_daily_reporting.py
```

The test uses an isolated temporary Docker volume for SQLite, a unique mock Feishu hook, and the real Prometheus and Mailpit services. It verifies signed Feishu cards, application-level and HTTP failures, restart-safe retries without duplicate email, and that email HTML exactly matches the persisted detail page. Mocked time covers retry backoff, the `23:59:59`/`24:00:00` boundary, and a three-day pause with no backlog. Only the test's temporary volume and server are removed; existing reports and inbox messages are preserved.

`REPORT_TEST_NOW` is a test-only report/scheduler clock override; leave it unset for normal operation. Feishu signatures always use current wall-clock time, even when a report's time is mocked.

Stop the stack while preserving its volumes:

```bash
docker compose down
```

Delete the development data as well:

```bash
docker compose down -v
```

## Production configuration

The reporter's relevant environment variables are:

| Variable | Default | Purpose |
|---|---:|---|
| `PROMETHEUS_URL` | `http://prometheus:9090` | Prometheus base URL |
| `REPORT_DATABASE_PATH` | `/data/reports.db` | SQLite archive and delivery state |
| `REPORT_WINDOW` | `24h` | PromQL range selector |
| `REPORT_INTERVAL_SECONDS` | `86400` | Minimum elapsed time between report generation |
| `CHECK_INTERVAL_SECONDS` | `60` | Due-check frequency |
| `REPORT_GROUP_BY` | `user_id` | Comma-separated Prometheus labels for detail rows |
| `REPORT_TOP_LIMIT` | `50` | Maximum detail rows |
| `REPORT_PUBLIC_BASE_URL` | unset | Trusted external base URL for permanent report links; required by Feishu |
| `REPORT_WARNING_MISS_PERCENT` | `30` | Warning at or above this miss percentage |
| `REPORT_CRITICAL_MISS_PERCENT` | `60` | Critical strictly above this miss percentage |
| `REPORT_MINIMUM_REQUESTS` | `100` | Below this volume, severity is unknown; Compose uses `1` for demo traffic |
| `REPORT_RETRY_BASE_SECONDS`, `REPORT_RETRY_MAX_SECONDS` | `60`, `3600` | Exponential retry delay with bounded jitter |
| `REPORT_NOTIFICATIONS_FILE` | required | JSON array of integration instances; Compose sets `/app/notifications.json` |

## Report and delivery lifecycle

`reporter.py` queries aggregate Prometheus values. `severity.py` evaluates overall and per-user rates using the same policy; the overall assessment controls message severity. No traffic, low volume, or inconsistent counters are `unknown`, never automatically critical. Default boundaries are explicit: miss below 30% is normal, 30–60% inclusive is warning, above 60% is critical. Daily reports are sent at every severity; this is not an immediate-alert trigger.

`rendering.py` creates the canonical HTML/text once. SQLite keeps the aggregate JSON, assessment and policy parameters, renderer version, exact HTML/text, and stable detail URL. Email embeds this HTML; Feishu links to `GET /reports/{id}/view`, which serves the same stored bytes. `GET /reports/{id}` returns structured JSON and per-integration status. `/reports/latest/view` redirects to the latest ID; notification links always use a specific ID. Historical artifacts never change when templates or thresholds change.

A transaction saves each report, creates its delivery rows, and advances `scheduler_state.last_generated_at`. `delivery.py` independently renders/persists each channel payload and attempts delivery through the integrations in `notifications.py`. A failed Feishu send cannot cause an already-successful email to be retried. Retries reuse the persisted report and channel payload, without querying Prometheus again.

SQLite has three application tables: immutable `reports` artifacts, `report_deliveries`, and `scheduler_state` containing only the generation clock. The outbox is the sole source of delivery status, attempts, next retry, last attempt, sent timestamp, and sanitized error per integration. Report API status (`pending`, `partial`, `sent`, `failed`, or `superseded`) is derived from these delivery rows, never stored separately. Successful timestamps and delivery errors appear only on individual deliveries.

- Stopping the reporter for three days produces one current 24-hour report on restart, not three catch-up reports.
- If just one destination is down, the others continue receiving daily reports. When a newer report is created, older unsent deliveries become `superseded`; recovery sends only the latest pending report.
- Transient network errors, HTTP 408/429/5xx, and Feishu rate-limit errors retry with exponential backoff. Known invalid-payload/authentication failures stop retrying that delivery and remain visible in the archive.
- A retry can be delivered close to the next scheduled report. The 24-hour gate limits **generation**, not every successful network send. `--force` explicitly bypasses it.
- Delivery remains at-least-once: a crash after remote acceptance but before SQLite commits can duplicate that channel. SMTP also cannot guarantee recipient-level deduplication after partial acceptance. Stable email Message-IDs and webhook idempotency keys help identify duplicates; generic webhook receivers should deduplicate.
- Run one reporter process per SQLite database; do not run a one-shot command concurrently with the resident reporter. Stop the resident process first if using `--once --force`.

This is unreleased software: schema changes require a fresh report database, not automatic migrations or historical-format fallbacks. Stop the reporter, retain the previous database as a backup, and set `REPORT_DATABASE_PATH` to a new file when changing schemas. Restarting with an unchanged schema preserves reports, artifacts, delivery outcomes, and the generation clock.

Only aggregate report snapshots, rendered artifacts, and delivery metadata are stored in SQLite; raw time series remain in Prometheus. No broker, external database, or Python package dependency is required.

## Configure Feishu and integration instances

Add a custom bot to the target Feishu group, enable signature verification, and set `FEISHU_WEBHOOK_URL`, `FEISHU_SIGNING_SECRET`, and `REPORT_PUBLIC_BASE_URL`. The default Compose destination is a local mock, not a real Feishu group. The mock validates signatures and accepts the card shape but cannot validate Feishu's actual client rendering; perform a smoke test with a real bot before production rollout.

Feishu cards show the shared severity, overall hit/miss rates, request counts, up to five miss contributors among archived detail rows, and an **Open full report** link. Cards are bounded below Feishu's 20 KB limit, use restrained severity colors, and do not mention group members. Each attempt refreshes its signature; signatures, credentials, and destination webhook URLs are never stored in the report/outbox or emitted in delivery errors. HTTP redirects are rejected.

See the [official custom-bot guide](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot) for group setup, security settings, card payloads, and limits.

All integrations are configured in one required JSON file. Compose mounts `notifications.json` at `/app/notifications.json` and sets `REPORT_NOTIFICATIONS_FILE`. Each entry needs a unique, stable `id` and `type` (`email`, `feishu`, or `webhook`). Missing files, empty integration lists, duplicate IDs, and unknown fields are configuration errors. Environment variables never implicitly enable an integration.

| Integration | Configuration fields |
|---|---|
| `email` | Required `smtp_host`, `sender`, and nonempty `recipients` array; optional `smtp_port` (25), `smtp_starttls` (false), `smtp_username_env`, `smtp_password_env` |
| `feishu` | Required `webhook_url_env`; optional `signing_secret_env` |
| `webhook` | Required `webhook_url_env`; sends JSON with an `Idempotency-Key` header |

SMTP settings belong to each email instance, so different audiences can use different transports. Webhook URLs, signing secrets and SMTP credentials are referenced by environment-variable names, not embedded secrets. The bundled manifest references `FEISHU_WEBHOOK_URL` and `FEISHU_SIGNING_SECRET`. Compose also exposes `SMTP_USERNAME`, `SMTP_PASSWORD` and `REPORT_WEBHOOK_URL` for optional entries; they have no effect unless referenced in the file.

Removing an instance cancels its next pending retry. Changing destinations should use a new integration ID, so a queued report cannot be redirected to a different audience. Existing reports are not retroactively sent to newly added IDs. Adding a transport requires implementing `NotificationIntegration.render/send` and registering its factory; it should not change the severity policy or scheduler.

If instances reference additional secret environment-variable names, also pass those variables through `reporter.environment` in Compose or your deployment's secret manager. A Compose `.env` file alone does not automatically inject arbitrary variables into the container.

Prometheus retention must be at least as long as `REPORT_WINDOW`. For long-term reporting, add remote-write storage or increase retention. Keep detailed labels bounded: grouping by raw end-user IDs can create one time series per user, model, provider, and governance-label combination. The archive and HTML endpoints contain user-level data, have no built-in authentication, and are bound to loopback in this development Compose file. Put both behind your normal authentication/SSO layer before exposing them, configure a reachable HTTPS `REPORT_PUBLIC_BASE_URL`, and keep the fault-injection mock local. Report URLs are never derived from an incoming Host header.
