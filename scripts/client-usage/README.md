# Client usage by virtual key

Python 3.9+ script, using only the standard library, that reads Bifrost's
`/api/logs/rankings/by-dimension` endpoint. No database access or changes to
Bifrost are required.

## Before running

You need:

1. Python 3.9 or later and system timezone data for the chosen timezone.
2. The Bifrost management base URL serving `/api/logs/...`, not a provider's
   `/v1` URL.
3. A Bifrost admin/dashboard session token with permission to read the desired
   logs. A GitHub token or inference virtual key cannot authenticate this API.
4. An inclusive date range and the timezone to use for calendar days.

Run from the repository root. Alternatively, copy `report.py` to your own
machine and invoke it by its local path; it has no dependency on other files
in this repository. The token file must exist on the machine running Python.

## Run with a token file

Put only the raw Bifrost session token in a local file, without quotes or a
`Bearer ` prefix. Keep the file outside the repository. Replace the URL and
file path below with your own values, and choose the reporting dates:

```bash
(
  export BIFROST_AUTHORIZATION="Bearer $(tr -d '\r\n' < /path/to/bifrost-token.txt)"
  python3 scripts/client-usage/report.py \
    --base-url https://your-bifrost-host \
    --start 2026-09-01 --end 2026-09-17 \
    --timezone Asia/Shanghai \
    --output-dir /tmp/client-usage
)
```

The subshell keeps the authorization variable scoped to this run. The command
does not display the token or put its literal value in shell history. Avoid
running it with shell tracing (`set -x`) enabled.

For a deployment that permits management requests without authentication:

```bash
python3 scripts/client-usage/report.py \
  --base-url https://your-bifrost-host \
  --start 2026-09-01 --end 2026-09-17 \
  --timezone Asia/Shanghai \
  --output-dir /tmp/client-usage
```

For an authenticated deployment, set `BIFROST_AUTHORIZATION` to your full
management API authorization value (for example, `Bearer <session-token>` or
`Basic <base64-user-colon-password>`), or set `BIFROST_COOKIE` to the management
session cookie header. These are the credentials used to access the dashboard;
an inference virtual key is not a substitute. The script reads these from the
environment and does not print them. `BIFROST_URL` can supply the base URL.
Use credentials with access to all colleagues' logs for an organization-wide
report; the API enforces the caller's access scope.

Both dates are inclusive, using the selected timezone's calendar days (UTC by
default). The script uses exact non-overlapping day boundaries, including DST
changes. It prints progress and detected app labels to stderr, and overall
counts to stdout.

## Results

- `daily.csv`: `date,client,active_vks,change_from_previous_day,cumulative_vks`.
  Each VK counts once per client per day. The first day's change is blank;
  subsequent days compare against the preceding calendar day, including zeros.
  Cumulative counts deduplicate VKs from the selected start date through that day.
- `overall.csv`: `client,unique_vks`. Each VK counts once per client across the
  entire date range. This is not the sum of daily counts.

For example, if VK A uses Claude Code 500 times and Codex 100 times on Monday,
both clients get +1 on Monday. If A uses Codex again on Tuesday, Tuesday's Codex
count is 1 and its overall count remains 1. No VK IDs or request bodies are
written to the CSVs. Existing CSVs at the destination are overwritten after
all API queries succeed.

An illustrative `overall.csv`:

```csv
client,unique_vks
claude-code,50
codex,30
```

An illustrative `daily.csv`:

```csv
date,client,active_vks,change_from_previous_day,cumulative_vks
2026-09-01,claude-code,40,,40
2026-09-01,codex,25,,25
2026-09-02,claude-code,45,5,50
2026-09-02,codex,20,-5,30
```

These are example counts, not measurements. Open the daily CSV in a spreadsheet
and plot `active_vks` against `date`, using `client` for the series. Use
`cumulative_vks` instead to show adoption since the selected start date.

## Client grouping

The script discovers all non-empty stored `app` labels in the selected period
(not the recent-only filter dropdown). It groups `Claude Code` as `claude-code`
and combines `Codex CLI` and `Codex Desktop` as `codex`. Other app labels become
lowercase names with spaces replaced by hyphens. App versions are already
normalized by Bifrost's User-Agent detection. Custom groups can override these:

```bash
python3 scripts/client-usage/report.py \
  --start 2026-09-01 --end 2026-09-17 \
  --group 'codex=Codex CLI,Codex Desktop,Our Custom Codex'
```

Labels on the right of `=` must match stored labels exactly. Other discovered
clients remain included. One API query per client group per day returns VK
rankings with `all=true`, avoiding the default 100-row cap. Sets of VK IDs
deduplicate across app variants and days. Requests without a VK (`Unassigned`)
are excluded.

## Coverage

This requires the version of Bifrost supporting `app`/`virtual_key` rankings,
`apps` filters, and `all=true`, as implemented in this checkout. The script
counts terminal LLM log records (success, error, or cancelled), so failed calls
also indicate client usage. Standalone MCP tool logs are not included.

Only retained, accessible logs with a populated `app` participate. Older rows
without app detection cannot be classified this way; unknown User-Agents may
appear as `other`. Apps absent throughout the selected period are omitted;
apps with activity somewhere in the period get zero rows on inactive days.
Today can be incomplete, and delayed log writes can change results on a rerun.
Counts measure VKs, not necessarily people: shared keys or multiple keys per
person affect that interpretation. Use completed days for daily comparisons.

## Side effects and performance

The script sends only GET requests to the rankings endpoint. It does not edit
VKs, configuration, or request-log records, and it does not make inference
requests. It creates the output directory and overwrites the two local CSVs.
HTTP redirects are rejected rather than forwarding credentials elsewhere.

Bifrost's own PostgreSQL query path can trigger materialized-view repair if a
view is missing or malformed. That is server-managed maintenance of derived
analytics views, not modification of the source request logs. Ordinary server
access logging can also record these API requests.

There are `1 + days × client groups` sequential API calls. A 30-day report with
10 groups makes 301 calls. The VK rankings use the raw logs table and also
calculate totals and previous-period trends that this report does not need.
Large ranges can therefore create substantial database load. Start with a short
range to assess runtime; `--timeout 120` changes the per-request timeout in
seconds. Timing out the client is not a guarantee that the database query has
stopped. The script does not cache results, so reruns repeat the queries.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Token file not found | Use an absolute path on the machine where the script is running. |
| HTTP 401 or 403 | Refresh the dashboard session token and verify log-read permissions. |
| HTTP 404, redirect, or invalid JSON | Use the management API host/path; verify the deployment supports the rankings endpoint. |
| No detected apps | Check date range, retained logs, populated app fields, and the credential's access scope. |
| Cannot reach the API | Check connectivity, TLS trust, and timeout; the script does not disable TLS verification. |
| Timezone not found | Install system timezone data or use an available timezone such as UTC. |

On an API failure the script exits nonzero before writing either CSV; CSVs
from a previous successful run remain in place. A filesystem failure during
output can still leave a partially written report, so check the exit status.

## Local verification

Run the local mock-API checks with:

```bash
python3 -m unittest discover -s scripts/client-usage -p 'test_*.py'
```
