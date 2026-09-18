#!/usr/bin/env python3
"""Report distinct virtual keys per client using Bifrost's read-only rankings API."""

import argparse
import csv
import json
import os
import sys
from datetime import date, datetime, time, timedelta, timezone
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError


class ReportError(Exception):
    pass


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Never forward management credentials to a redirect target.
        return None


def window_params(start, end, tz):
    """Inclusive local dates; Bifrost's end_time is inclusive to nanoseconds."""
    lower = datetime.combine(start, time.min, tz).astimezone(timezone.utc)
    upper = datetime.combine(end + timedelta(days=1), time.min, tz)
    last_second = upper.astimezone(timezone.utc) - timedelta(seconds=1)
    return {
        "start_time": lower.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "end_time": last_second.strftime("%Y-%m-%dT%H:%M:%S") + ".999999999Z",
    }


class BifrostAPI:
    def __init__(self, base_url, timeout=60):
        parsed = urlsplit(base_url)
        if (parsed.scheme not in ("http", "https") or not parsed.hostname
                or parsed.username is not None or parsed.password is not None
                or parsed.query or parsed.fragment):
            raise ReportError("base URL must be HTTP(S), without credentials, query or fragment")
        self.base = base_url.rstrip("/")
        if not self.base.endswith("/api"):
            self.base += "/api"
        self.timeout = timeout
        self.opener = build_opener(NoRedirects())
        self.headers = {"Accept": "application/json"}
        for env, header in (("BIFROST_AUTHORIZATION", "Authorization"),
                            ("BIFROST_COOKIE", "Cookie")):
            if os.environ.get(env):
                self.headers[header] = os.environ[env]

    def rankings(self, dimension, window, apps=None):
        params = {**window, "dimension": dimension, "all": "true"}
        if apps is not None:
            if not apps:
                raise ReportError("refusing an empty app filter")
            params["apps"] = json.dumps(apps)
        request = Request(
            self.base + "/logs/rankings/by-dimension?" + urlencode(params),
            headers=self.headers,
        )
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                payload = json.load(response)
        except HTTPError as exc:
            # Do not echo response bodies, which may contain sensitive data.
            raise ReportError(
                f"rankings API returned HTTP {exc.code}; check URL, management "
                "authentication, log access, and endpoint availability"
            ) from None
        except (URLError, TimeoutError, OSError):
            raise ReportError("cannot reach rankings API; check connection/TLS or --timeout") from None
        except (ValueError, UnicodeError):
            raise ReportError("rankings API returned invalid JSON; check URL/authentication") from None
        if (not isinstance(payload, dict) or payload.get("dimension") != dimension
                or not isinstance(payload.get("rankings"), list)):
            raise ReportError("unexpected rankings response; this Bifrost version may be unsupported")
        for row in payload["rankings"]:
            if (not isinstance(row, dict) or not isinstance(row.get("id"), str)
                    or type(row.get("total_requests")) is not int
                    or row["total_requests"] < 0):
                raise ReportError("invalid ranking row; refusing to produce incomplete counts")
        return payload["rankings"]


def app_groups(apps, overrides):
    """Keep exact API labels in filters; normalize only report category names."""
    mapping = {"Claude Code": "claude-code", "Codex CLI": "codex", "Codex Desktop": "codex"}
    for spec in overrides:
        name, sep, labels = spec.partition("=")
        labels = [label.strip() for label in labels.split(",")]
        if not sep or not name.strip() or not all(labels):
            raise ReportError('--group must look like \'codex=Codex CLI,Codex Desktop\'')
        for label in labels:
            mapping[label] = name.strip()
    groups = {}
    for app in sorted(set(apps)):
        if not app.strip():
            continue
        name = mapping.get(app, "-".join(app.lower().split()))
        groups.setdefault(name, []).append(app)
    return dict(sorted(groups.items()))


def collect_report(api, start, end, tz, overrides=()):
    discovered = api.rankings("app", window_params(start, end, tz))
    groups = app_groups([row["id"] for row in discovered if row["total_requests"] > 0], overrides)
    if not groups:
        raise ReportError("no detected apps in this period; check retained logs and access scope")
    for name, labels in groups.items():
        print(f"{name}: {', '.join(labels)}", file=sys.stderr)
    cumulative = {name: set() for name in groups}
    previous = {}
    daily = []
    day = start
    while day <= end:
        print(f"Querying {day} ({tz.key})", file=sys.stderr)
        for name, labels in groups.items():
            rows = api.rankings("virtual_key", window_params(day, day, tz), labels)
            keys = {row["id"] for row in rows
                    if row["id"] not in ("", "unassigned") and row["total_requests"] > 0}
            count = len(keys)
            cumulative[name].update(keys)
            daily.append({
                "date": day.isoformat(), "client": name, "active_vks": count,
                "change_from_previous_day": count - previous[name] if name in previous else "",
                "cumulative_vks": len(cumulative[name]),
            })
            previous[name] = count
        day += timedelta(days=1)
    overall = [{"client": name, "unique_vks": len(keys)} for name, keys in cumulative.items()]
    return daily, overall


def write_csv(path, rows):
    with path.open("w", newline="", encoding="utf-8") as output:
        writer = csv.DictWriter(output, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=os.getenv("BIFROST_URL", "http://localhost:8080"))
    parser.add_argument("--start", type=date.fromisoformat, required=True, help="first date, YYYY-MM-DD (inclusive)")
    parser.add_argument("--end", type=date.fromisoformat, required=True, help="last date, YYYY-MM-DD (inclusive)")
    parser.add_argument("--timezone", default="UTC", help="calendar-day timezone, e.g. Asia/Shanghai (default: UTC)")
    parser.add_argument("--group", action="append", default=[], metavar="NAME=APP,APP",
                        help="merge exact stored app labels; repeatable; other apps remain included")
    parser.add_argument("--output-dir", type=Path, default=Path("client-usage-report"))
    parser.add_argument("--timeout", type=float, default=60, help="seconds per API request (default: 60)")
    args = parser.parse_args()
    if args.end < args.start:
        parser.error("--end must be on or after --start")
    if not 0 < args.timeout < float("inf"):
        parser.error("--timeout must be finite and positive")
    try:
        tz = ZoneInfo(args.timezone)
        app_groups([], args.group)  # Validate before any requests.
        api = BifrostAPI(args.base_url, args.timeout)
        daily, overall = collect_report(api, args.start, args.end, tz, args.group)
        args.output_dir.mkdir(parents=True, exist_ok=True)
        write_csv(args.output_dir / "daily.csv", daily)
        write_csv(args.output_dir / "overall.csv", overall)
    except (ReportError, OSError, ValueError, OverflowError, ZoneInfoNotFoundError) as exc:
        parser.exit(1, f"Error: {exc}\n")
    print(f"Unique VKs per client, {args.start} to {args.end} ({tz.key}):")
    for row in overall:
        print(f"  {row['client']}: {row['unique_vks']}")
    print(f"CSVs saved to {args.output_dir.resolve()}")


if __name__ == "__main__":
    main()
