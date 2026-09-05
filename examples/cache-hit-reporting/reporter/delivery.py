"""Delivery coordination and retry policy, independent of report generation."""

import json
import random
from datetime import datetime, timedelta
from typing import Callable

from notifications import DeliveryError, NotificationIntegration
from storage import ReportStore


class DeliveryCoordinator:
    def __init__(self, store: ReportStore, integrations: list[NotificationIntegration],
                 clock: Callable[[], datetime], retry_base_seconds: int = 60, retry_max_seconds: int = 3600):
        self.store = store
        self.integrations = {integration.id: integration for integration in integrations}
        if len(self.integrations) != len(integrations) or not integrations:
            raise ValueError("integration IDs must be unique and nonempty")
        self.clock = clock
        self.retry_base_seconds = retry_base_seconds
        self.retry_max_seconds = retry_max_seconds
        self.store.recover_interrupted()

    def dispatch(self) -> list[str]:
        errors = []
        for delivery in self.store.due_deliveries(self.clock()):
            report_id, integration_id = delivery["report_id"], delivery["integration_id"]
            integration = self.integrations.get(integration_id)
            if integration is None or integration.kind != delivery["integration_type"]:
                self.store.finish_attempt(report_id, integration_id, self.clock(), superseded=True)
                continue
            self.store.begin_attempt(report_id, integration_id, self.clock())
            try:
                if delivery["rendered_payload"] is None:
                    payload = integration.render(self.store.notification(report_id))
                    self.store.save_payload(report_id, integration_id, payload)
                else:
                    payload = json.loads(delivery["rendered_payload"])
                integration.send(payload)
            except Exception as error:
                safe_error = str(error) if isinstance(error, DeliveryError) else f"integration failed ({type(error).__name__})"
                retryable = not isinstance(error, DeliveryError) or error.retryable
                retry_after = error.retry_after if isinstance(error, DeliveryError) else 0
                delay = min(self.retry_max_seconds, self.retry_base_seconds * 2 ** min(delivery["attempts"], 16))
                delay = max(retry_after, min(self.retry_max_seconds, delay * random.uniform(1, 1.1)))
                self.store.finish_attempt(
                    report_id, integration_id, self.clock(), error=safe_error,
                    retry_at=self.clock() + timedelta(seconds=delay) if retryable else None,
                )
                errors.append(f"{integration_id}: {safe_error}")
                continue
            self.store.finish_attempt(report_id, integration_id, self.clock())
        return errors
