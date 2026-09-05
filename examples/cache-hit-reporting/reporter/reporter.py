#!/usr/bin/env python3
"""Daily cache-hit reports backed by Prometheus and SQLite.

The service intentionally uses only the Python standard library so the example has
no package-manager or lock-file surface.  It can run continuously (the default) or
once from a scheduler with ``--once``.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import threading
from dataclasses import dataclass
from datetime import datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlencode, urlparse
from urllib.request import Request, urlopen

from delivery import DeliveryCoordinator
from models import isoformat, parse_timestamp, utc_now
from notifications import NotificationIntegration, configured_integrations, validate_url
from rendering import ReportRenderer
from severity import SeverityPolicy
from storage import ReportStore


LABEL_RE = re.compile(r"^[a-zA-Z_][a-zA-Z0-9_]*$")
PROM_DURATION_RE = re.compile(r"^[1-9][0-9]*(?:ms|s|m|h|d|w|y)$")
PROM_DURATION_MULTIPLIERS = {
    "ms": 0.001,
    "s": 1,
    "m": 60,
    "h": 3_600,
    "d": 86_400,
    "w": 604_800,
    "y": 31_536_000,
}


def configured_clock() -> Callable[[], datetime]:
    """Return the real clock, or a fixed clock used by the stack E2E test."""
    value = os.getenv("REPORT_TEST_NOW", "").strip()
    if not value:
        return utc_now
    fixed_now = parse_timestamp(value)
    return lambda: fixed_now


def split_csv(value: str) -> tuple[str, ...]:
    return tuple(part.strip() for part in value.split(",") if part.strip())


def prometheus_duration_seconds(value: str) -> float:
    match = re.fullmatch(r"([1-9][0-9]*)(ms|s|m|h|d|w|y)", value)
    if match is None:
        raise ValueError(f"invalid Prometheus duration: {value}")
    return int(match.group(1)) * PROM_DURATION_MULTIPLIERS[match.group(2)]


@dataclass(frozen=True)
class Config:
    prometheus_url: str
    database_path: str
    report_window: str = "24h"
    report_interval_seconds: int = 86_400
    check_interval_seconds: int = 60
    query_timeout_seconds: int = 10
    group_by: tuple[str, ...] = ("user_id",)
    top_limit: int = 50
    http_host: str = "0.0.0.0"
    http_port: int = 8091
    public_base_url: str = ""
    notifications_file: str = ""
    warning_miss_percent: float = 30.0
    critical_miss_percent: float = 60.0
    minimum_requests: int = 100
    retry_base_seconds: int = 60
    retry_max_seconds: int = 3600

    @classmethod
    def from_env(cls) -> "Config":
        config = cls(
            prometheus_url=os.getenv("PROMETHEUS_URL", "http://prometheus:9090").rstrip("/"),
            database_path=os.getenv("REPORT_DATABASE_PATH", "/data/reports.db"),
            report_window=os.getenv("REPORT_WINDOW", "24h").strip(),
            report_interval_seconds=int(os.getenv("REPORT_INTERVAL_SECONDS", "86400")),
            check_interval_seconds=int(os.getenv("CHECK_INTERVAL_SECONDS", "60")),
            query_timeout_seconds=int(os.getenv("PROMETHEUS_QUERY_TIMEOUT_SECONDS", "10")),
            group_by=split_csv(os.getenv("REPORT_GROUP_BY", "user_id")),
            top_limit=int(os.getenv("REPORT_TOP_LIMIT", "50")),
            http_host=os.getenv("REPORT_HTTP_HOST", "0.0.0.0"),
            http_port=int(os.getenv("REPORT_HTTP_PORT", "8091")),
            public_base_url=os.getenv("REPORT_PUBLIC_BASE_URL", "").rstrip("/"),
            notifications_file=os.getenv("REPORT_NOTIFICATIONS_FILE", ""),
            warning_miss_percent=float(os.getenv("REPORT_WARNING_MISS_PERCENT", "30")),
            critical_miss_percent=float(os.getenv("REPORT_CRITICAL_MISS_PERCENT", "60")),
            minimum_requests=int(os.getenv("REPORT_MINIMUM_REQUESTS", "100")),
            retry_base_seconds=int(os.getenv("REPORT_RETRY_BASE_SECONDS", "60")),
            retry_max_seconds=int(os.getenv("REPORT_RETRY_MAX_SECONDS", "3600")),
        )
        config.validate()
        return config

    def validate(self) -> None:
        if not self.prometheus_url.startswith(("http://", "https://")):
            raise ValueError("PROMETHEUS_URL must use http:// or https://")
        if not PROM_DURATION_RE.fullmatch(self.report_window):
            raise ValueError("REPORT_WINDOW must be a positive Prometheus duration such as 24h")
        if self.report_interval_seconds <= 0 or self.check_interval_seconds <= 0:
            raise ValueError("report and check intervals must be positive")
        if self.query_timeout_seconds <= 0 or self.top_limit <= 0:
            raise ValueError("query timeout and top limit must be positive")
        invalid = [label for label in self.group_by if not LABEL_RE.fullmatch(label)]
        if invalid:
            raise ValueError(f"invalid Prometheus label(s): {', '.join(invalid)}")
        if not self.notifications_file:
            raise ValueError("REPORT_NOTIFICATIONS_FILE is required")
        if self.public_base_url:
            validate_url(self.public_base_url, "REPORT_PUBLIC_BASE_URL")
            if urlparse(self.public_base_url).query:
                raise ValueError("REPORT_PUBLIC_BASE_URL cannot contain a query string")
        if not 0 < self.retry_base_seconds <= self.retry_max_seconds:
            raise ValueError("retry intervals must satisfy 0 < base <= max")
        SeverityPolicy(self.warning_miss_percent, self.critical_miss_percent, self.minimum_requests)


class PrometheusClient:
    def __init__(self, base_url: str, timeout_seconds: int = 10):
        self.base_url = base_url.rstrip("/")
        self.timeout_seconds = timeout_seconds

    def query(self, expression: str, at: datetime) -> list[dict[str, Any]]:
        params = urlencode({"query": expression, "time": isoformat(at)})
        request = Request(f"{self.base_url}/api/v1/query?{params}", headers={"Accept": "application/json"})
        try:
            with urlopen(request, timeout=self.timeout_seconds) as response:
                payload = json.load(response)
        except (HTTPError, URLError, TimeoutError, json.JSONDecodeError) as error:
            raise RuntimeError(f"Prometheus query failed: {error}") from error
        if payload.get("status") != "success":
            raise RuntimeError(f"Prometheus query failed: {payload.get('error', payload)}")
        data = payload.get("data", {})
        if data.get("resultType") != "vector":
            raise RuntimeError(f"expected vector result, got {data.get('resultType')!r}")
        return data.get("result", [])

    @staticmethod
    def sample_value(sample: dict[str, Any]) -> float:
        try:
            value = float(sample["value"][1])
        except (KeyError, IndexError, TypeError, ValueError) as error:
            raise RuntimeError(f"invalid Prometheus sample: {sample!r}") from error
        if not math.isfinite(value):
            return 0.0
        # Tiny negative floating-point artifacts can appear around counter resets.
        return max(0.0, value)

    def scalar(self, expression: str, at: datetime) -> float:
        result = self.query(expression, at)
        return sum(self.sample_value(sample) for sample in result)


class Reporter:
    def __init__(
        self,
        config: Config,
        prometheus: PrometheusClient,
        store: ReportStore,
        integrations: list[NotificationIntegration],
        clock: Callable[[], datetime] = utc_now,
    ):
        self.config = config
        self.prometheus = prometheus
        self.store = store
        self.clock = clock
        self.renderer = ReportRenderer()
        self.policy = SeverityPolicy(config.warning_miss_percent, config.critical_miss_percent, config.minimum_requests)
        self.delivery = DeliveryCoordinator(store, integrations, clock, config.retry_base_seconds, config.retry_max_seconds)
        self.run_lock = threading.Lock()
        self.last_error = ""

    def is_due(self, now: datetime) -> bool:
        last_generated = self.store.last_generated_at()
        return last_generated is None or now - last_generated >= timedelta(seconds=self.config.report_interval_seconds)

    def _increase(self, metric: str) -> str:
        return f"increase({metric}[{self.config.report_window}])"

    def build_report(self, now: datetime) -> dict[str, Any]:
        period_start = now - timedelta(seconds=prometheus_duration_seconds(self.config.report_window))
        requests_expr = self._increase("bifrost_upstream_requests_total")
        hits_expr = self._increase("bifrost_cache_hits_total")

        total_requests = self.prometheus.scalar(f"sum({requests_expr})", now)
        total_hits = self.prometheus.scalar(f"sum({hits_expr})", now)

        hits_by_type: dict[str, float] = {}
        for sample in self.prometheus.query(f"sum by (cache_type) ({hits_expr})", now):
            cache_type = sample.get("metric", {}).get("cache_type") or "unknown"
            hits_by_type[cache_type] = hits_by_type.get(cache_type, 0.0) + self.prometheus.sample_value(sample)

        details: dict[tuple[str, ...], dict[str, Any]] = {}
        if self.config.group_by:
            labels = ", ".join(self.config.group_by)
            for sample in self.prometheus.query(f"sum by ({labels}) ({requests_expr})", now):
                metric_labels = sample.get("metric", {})
                key = tuple(metric_labels.get(label, "") for label in self.config.group_by)
                details[key] = {
                    "labels": {label: metric_labels.get(label, "") for label in self.config.group_by},
                    "requests": self.prometheus.sample_value(sample),
                    "hits": 0.0,
                    "hits_by_type": {},
                }
            cache_labels = f"{labels}, cache_type"
            for sample in self.prometheus.query(f"sum by ({cache_labels}) ({hits_expr})", now):
                metric_labels = sample.get("metric", {})
                key = tuple(metric_labels.get(label, "") for label in self.config.group_by)
                row = details.setdefault(
                    key,
                    {
                        "labels": {label: metric_labels.get(label, "") for label in self.config.group_by},
                        "requests": 0.0,
                        "hits": 0.0,
                        "hits_by_type": {},
                    },
                )
                cache_type = metric_labels.get("cache_type") or "unknown"
                value = self.prometheus.sample_value(sample)
                row["hits"] += value
                row["hits_by_type"][cache_type] = row["hits_by_type"].get(cache_type, 0.0) + value

        detail_rows = list(details.values())
        for row in detail_rows:
            row["hit_rate_percent"] = 100.0 * row["hits"] / row["requests"] if row["requests"] else 0.0
        detail_rows.sort(key=lambda row: (-row["requests"], tuple(row["labels"].values())))
        detail_rows = detail_rows[: self.config.top_limit]

        return {
            "period_start": isoformat(period_start),
            "period_end": isoformat(now),
            "window": self.config.report_window,
            "group_by": list(self.config.group_by),
            "overall": {
                "requests": total_requests,
                "hits": total_hits,
                "misses": max(0.0, total_requests - total_hits),
                "hit_rate_percent": 100.0 * total_hits / total_requests if total_requests else 0.0,
                "hits_by_type": hits_by_type,
            },
            "details": detail_rows,
        }

    def run_once(self, force: bool = False) -> int | None:
        if not self.run_lock.acquire(blocking=False):
            raise RuntimeError("a report run is already in progress")
        try:
            now = self.clock()
            report_id = None
            generation_error = None
            try:
                if force or self.is_due(now):
                    report = self.build_report(now)
                    self.policy.apply(report)
                    report_id = self.store.create(
                        report, self.renderer,
                        [(integration.id, integration.kind) for integration in self.delivery.integrations.values()],
                        now, self.config.report_interval_seconds, self.config.public_base_url, force,
                    )
            except Exception as error:
                generation_error = error
            errors = self.delivery.dispatch()
            if generation_error:
                raise generation_error
            self.last_error = "; ".join(errors)
            if errors:
                print(f"notification delivery failed: {self.last_error}", flush=True)
            return report_id
        except Exception as error:
            self.last_error = str(error)
            raise
        finally:
            self.run_lock.release()


class ReporterHTTPHandler(BaseHTTPRequestHandler):
    reporter: Reporter

    def _json(self, status: int, payload: Any) -> None:
        body = json.dumps(payload, indent=2).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "private, no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        parsed = urlparse(self.path)
        if parsed.path == "/healthz":
            self._json(200, {"status": "ok", "last_error": self.reporter.last_error or None})
            return
        if parsed.path == "/reports":
            query = parse_qs(parsed.query)
            try:
                limit = int(query.get("limit", ["50"])[0])
            except ValueError:
                self._json(400, {"error": "limit must be an integer"})
                return
            self._json(200, {"reports": self.reporter.store.list_reports(limit)})
            return
        if parsed.path == "/reports/latest":
            reports = self.reporter.store.list_reports(1)
            self._json(200 if reports else 404, reports[0] if reports else {"error": "no reports"})
            return
        if parsed.path == "/reports/latest/view":
            reports = self.reporter.store.list_reports(1)
            if not reports:
                self._json(404, {"error": "no reports"})
                return
            self.send_response(302)
            self.send_header("Location", f"../{reports[0]['id']}/view")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        match = re.fullmatch(r"/reports/([1-9][0-9]{0,17})(/view)?", parsed.path)
        if match:
            report_id = int(match.group(1))
            report = self.reporter.store.get_report(report_id)
            if report is None:
                self._json(404, {"error": "report not found"})
            elif match.group(2):
                body = self.reporter.store.notification(report_id).body_html.encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(body)))
                self.send_header("Cache-Control", "private, no-store")
                self.send_header("X-Content-Type-Options", "nosniff")
                self.send_header("Referrer-Policy", "no-referrer")
                self.send_header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
                self.end_headers()
                self.wfile.write(body)
            else:
                self._json(200, report)
            return
        self._json(404, {"error": "not found"})

    def log_message(self, message_format: str, *args: Any) -> None:
        print(f"reporter-http: {message_format % args}", flush=True)


def scheduler(reporter: Reporter, stop: threading.Event) -> None:
    while not stop.is_set():
        try:
            reporter.run_once()
        except Exception as error:
            print(f"report run failed: {error}", flush=True)
        stop.wait(reporter.config.check_interval_seconds)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--once", action="store_true", help="run one due check and exit")
    parser.add_argument("--force", action="store_true", help="generate even when the 24-hour interval has not elapsed")
    args = parser.parse_args()

    config = Config.from_env()
    reporter = Reporter(
        config,
        PrometheusClient(config.prometheus_url, config.query_timeout_seconds),
        ReportStore(config.database_path),
        configured_integrations(config.notifications_file, config.public_base_url),
        configured_clock(),
    )

    if args.once:
        report_id = reporter.run_once(force=args.force)
        print("report not due; pending deliveries checked" if report_id is None else f"report {report_id} persisted; deliveries attempted", flush=True)
        return 1 if reporter.last_error else 0

    handler = type("ConfiguredReporterHTTPHandler", (ReporterHTTPHandler,), {"reporter": reporter})
    server = ThreadingHTTPServer((config.http_host, config.http_port), handler)
    stop = threading.Event()
    worker = threading.Thread(target=scheduler, args=(reporter, stop), name="report-scheduler", daemon=True)
    worker.start()
    print(f"cache reporter listening on {config.http_host}:{config.http_port}", flush=True)
    try:
        server.serve_forever(poll_interval=0.5)
    except KeyboardInterrupt:
        pass
    finally:
        stop.set()
        server.server_close()
        worker.join(timeout=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
