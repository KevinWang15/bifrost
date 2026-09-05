"""SQLite report archive and durable per-integration notification outbox."""

import json
import sqlite3
from contextlib import closing
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

from models import Notification, isoformat, parse_timestamp
from rendering import ReportRenderer


class ReportStore:
    def __init__(self, path: str):
        self.path = path
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        self._initialize()

    def connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(self.path, timeout=10)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 10000")
        connection.execute("PRAGMA journal_mode = WAL")
        connection.execute("PRAGMA foreign_keys = ON")
        return connection

    def _initialize(self) -> None:
        with closing(self.connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            connection.execute("""
                CREATE TABLE IF NOT EXISTS reports (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    period_start TEXT NOT NULL, period_end TEXT NOT NULL,
                    report_json TEXT NOT NULL, body_text TEXT NOT NULL, body_html TEXT NOT NULL,
                    renderer_version TEXT NOT NULL, detail_url TEXT NOT NULL,
                    created_at TEXT NOT NULL
                )
            """)
            connection.execute("""
                CREATE TABLE IF NOT EXISTS scheduler_state (
                    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                    last_generated_at TEXT
                )
            """)
            connection.execute("INSERT OR IGNORE INTO scheduler_state(singleton) VALUES (1)")
            connection.execute("""
                CREATE TABLE IF NOT EXISTS report_deliveries (
                    report_id INTEGER NOT NULL REFERENCES reports(id),
                    integration_id TEXT NOT NULL, integration_type TEXT NOT NULL,
                    status TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'superseded')),
                    attempts INTEGER NOT NULL DEFAULT 0,
                    next_attempt_at TEXT, last_attempt_at TEXT, sent_at TEXT,
                    last_error TEXT, rendered_payload TEXT,
                    PRIMARY KEY (report_id, integration_id)
                )
            """)
            connection.execute("""
                CREATE INDEX IF NOT EXISTS idx_deliveries_due ON report_deliveries(status, next_attempt_at)
            """)

    def last_generated_at(self) -> datetime | None:
        with closing(self.connect()) as connection:
            value = connection.execute("SELECT last_generated_at FROM scheduler_state WHERE singleton = 1").fetchone()[0]
        return parse_timestamp(value) if value else None

    def create(
        self, report: dict[str, Any], renderer: ReportRenderer,
        integrations: list[tuple[str, str]], now: datetime, interval_seconds: int,
        public_base_url: str, force: bool = False,
    ) -> int | None:
        timestamp = isoformat(now)
        with closing(self.connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            last = connection.execute("SELECT last_generated_at FROM scheduler_state WHERE singleton = 1").fetchone()[0]
            if not force and last and now - parse_timestamp(last) < timedelta(seconds=interval_seconds):
                return None
            cursor = connection.execute("""
                INSERT INTO reports(period_start, period_end, report_json, body_text, body_html, renderer_version, detail_url, created_at)
                VALUES (?, ?, ?, '', '', ?, '', ?)
            """, (report["period_start"], report["period_end"], json.dumps(report, sort_keys=True, allow_nan=False),
                  renderer.version, timestamp))
            report_id = int(cursor.lastrowid)
            detail_url = f"{public_base_url}/reports/{report_id}/view" if public_base_url else ""
            connection.execute("""
                UPDATE reports SET body_text = ?, body_html = ?, detail_url = ? WHERE id = ?
            """, (renderer.render_text(report, detail_url), renderer.render_html(report, detail_url),
                  detail_url, report_id))
            connection.execute("""
                UPDATE report_deliveries SET status = 'superseded', next_attempt_at = NULL
                WHERE status IN ('pending', 'sending', 'failed')
            """)
            connection.executemany("""
                INSERT INTO report_deliveries(report_id, integration_id, integration_type, status, next_attempt_at)
                VALUES (?, ?, ?, 'pending', ?)
            """, [(report_id, name, kind, timestamp) for name, kind in integrations])
            connection.execute("""
                UPDATE scheduler_state SET last_generated_at = ? WHERE singleton = 1
            """, (timestamp,))
            return report_id

    def recover_interrupted(self) -> None:
        with closing(self.connect()) as connection, connection:
            connection.execute("""
                UPDATE report_deliveries SET status = 'failed', next_attempt_at = last_attempt_at,
                last_error = 'process interrupted during delivery; remote acceptance unknown'
                WHERE status = 'sending'
            """)

    def due_deliveries(self, now: datetime) -> list[dict[str, Any]]:
        with closing(self.connect()) as connection:
            return [dict(row) for row in connection.execute("""
                SELECT * FROM report_deliveries
                WHERE status IN ('pending', 'failed') AND next_attempt_at <= ?
                ORDER BY report_id, integration_id
            """, (isoformat(now),))]

    def begin_attempt(self, report_id: int, integration_id: str, now: datetime) -> None:
        with closing(self.connect()) as connection, connection:
            connection.execute("""
                UPDATE report_deliveries SET status = 'sending', attempts = attempts + 1, last_attempt_at = ?
                WHERE report_id = ? AND integration_id = ?
            """, (isoformat(now), report_id, integration_id))

    def save_payload(self, report_id: int, integration_id: str, payload: dict[str, Any]) -> None:
        with closing(self.connect()) as connection, connection:
            connection.execute("""
                UPDATE report_deliveries SET rendered_payload = ? WHERE report_id = ? AND integration_id = ?
            """, (json.dumps(payload, ensure_ascii=False, allow_nan=False), report_id, integration_id))

    def finish_attempt(
        self, report_id: int, integration_id: str, now: datetime,
        error: str | None = None, retry_at: datetime | None = None, superseded: bool = False,
    ) -> None:
        status = "superseded" if superseded else "failed" if error else "sent"
        with closing(self.connect()) as connection, connection:
            connection.execute("""
                UPDATE report_deliveries SET status = ?, sent_at = ?, last_error = ?, next_attempt_at = ?
                WHERE report_id = ? AND integration_id = ?
            """, (status, isoformat(now) if status == "sent" else None, error,
                  isoformat(retry_at) if retry_at else None, report_id, integration_id))

    def notification(self, report_id: int) -> Notification:
        with closing(self.connect()) as connection:
            row = connection.execute("""
                SELECT id, report_json, body_text, body_html, created_at, detail_url FROM reports WHERE id = ?
            """, (report_id,)).fetchone()
        if row is None:
            raise LookupError("report not found")
        return Notification(*tuple(row))

    @staticmethod
    def _report(connection: sqlite3.Connection, row: sqlite3.Row) -> dict[str, Any]:
        result = dict(row)
        result["report"] = json.loads(result.pop("report_json"))
        del result["body_text"]
        del result["body_html"]
        result["deliveries"] = [dict(delivery) for delivery in connection.execute("""
            SELECT integration_id, integration_type, status, attempts, next_attempt_at,
                   last_attempt_at, sent_at, last_error
            FROM report_deliveries WHERE report_id = ? ORDER BY integration_id
        """, (row["id"],))]
        states = {delivery["status"] for delivery in result["deliveries"]}
        result["status"] = (
            "sent" if states == {"sent"} else "superseded" if states == {"superseded"}
            else "partial" if "sent" in states else "failed" if "failed" in states else "pending"
        )
        return result

    def get_report(self, report_id: int) -> dict[str, Any] | None:
        with closing(self.connect()) as connection:
            row = connection.execute("SELECT * FROM reports WHERE id = ?", (report_id,)).fetchone()
            return self._report(connection, row) if row else None

    def list_reports(self, limit: int = 50) -> list[dict[str, Any]]:
        with closing(self.connect()) as connection:
            rows = connection.execute("SELECT * FROM reports ORDER BY id DESC LIMIT ?", (max(1, min(limit, 500)),)).fetchall()
            return [self._report(connection, row) for row in rows]
