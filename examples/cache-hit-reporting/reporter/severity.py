"""Central, transport-independent cache-miss severity policy."""

import math
from dataclasses import asdict, dataclass
from typing import Any


@dataclass(frozen=True)
class SeverityPolicy:
    warning_miss_percent: float = 30.0
    critical_miss_percent: float = 60.0
    minimum_requests: int = 100
    version: str = "cache-miss-v1"

    def __post_init__(self) -> None:
        if not 0 <= self.warning_miss_percent < self.critical_miss_percent <= 100:
            raise ValueError("severity thresholds must satisfy 0 <= warning < critical <= 100")
        if self.minimum_requests < 1:
            raise ValueError("severity minimum requests must be positive")

    def assess(self, requests: float, hits: float) -> dict[str, Any]:
        valid = all(math.isfinite(value) and value >= 0 for value in (requests, hits)) and hits <= requests
        rate = 100.0 * (requests - hits) / requests if valid and requests > 0 else None
        level, threshold = "unknown", None
        if not valid:
            reason = "Inconsistent request and cache-hit counters; check telemetry coverage."
        elif requests == 0:
            reason = "No measured requests; cache performance cannot be assessed."
        elif requests < self.minimum_requests:
            reason = f"Insufficient traffic: {requests:,.2f} requests; at least {self.minimum_requests:,} required."
        elif rate > self.critical_miss_percent:
            level, threshold = "critical", self.critical_miss_percent
            reason = f"Miss rate {rate:.2f}% exceeded the {threshold:g}% critical threshold."
        elif rate >= self.warning_miss_percent:
            level, threshold = "warning", self.warning_miss_percent
            reason = f"Miss rate {rate:.2f}% reached the {threshold:g}% warning threshold."
        else:
            level = "normal"
            reason = f"Miss rate {rate:.2f}% is below the {self.warning_miss_percent:g}% warning threshold."
        return {
            "level": level,
            "miss_rate_percent": rate,
            "request_count": requests,
            "reason": reason,
            "threshold_percent": threshold,
            "policy": asdict(self),
        }

    def apply(self, report: dict[str, Any]) -> None:
        report["assessment"] = self.assess(report["overall"]["requests"], report["overall"]["hits"])
        for row in report["details"]:
            row["assessment"] = self.assess(row["requests"], row["hits"])
