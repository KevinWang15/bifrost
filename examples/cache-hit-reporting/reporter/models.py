"""Shared notification data and UTC time helpers."""

import json
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any


UTC = timezone.utc


def utc_now() -> datetime:
    return datetime.now(UTC)


def isoformat(value: datetime) -> str:
    return value.astimezone(UTC).isoformat(timespec="seconds").replace("+00:00", "Z")


def parse_timestamp(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00")).astimezone(UTC)


@dataclass(frozen=True)
class Notification:
    report_id: int
    report_json: str
    body_text: str
    body_html: str
    created_at: str
    detail_url: str

    @property
    def report(self) -> dict[str, Any]:
        return json.loads(self.report_json)

    @property
    def severity(self) -> str:
        return self.report["assessment"]["level"]

    def delivery_key(self, integration_id: str) -> str:
        return f"bifrost-cache-report-{self.report_id}-{integration_id}"
