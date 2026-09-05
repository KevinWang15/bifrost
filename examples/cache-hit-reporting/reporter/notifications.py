"""Pluggable notification renderers and transports; severity is supplied upstream."""

import base64
import hashlib
import hmac
import json
import os
import re
import smtplib
import ssl
from dataclasses import dataclass
from datetime import datetime
from email.message import EmailMessage
from email.utils import format_datetime
from pathlib import Path
from typing import Any, Callable, Protocol
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener

from models import Notification, parse_timestamp, utc_now


class DeliveryError(RuntimeError):
    def __init__(self, message: str, retryable: bool = True, retry_after: float = 0):
        super().__init__(message)
        self.retryable = retryable
        self.retry_after = retry_after


class NotificationIntegration(Protocol):
    id: str
    kind: str

    def render(self, notification: Notification) -> dict[str, Any]: ...
    def send(self, payload: dict[str, Any]) -> None: ...


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


def validate_url(value: str, name: str) -> None:
    parsed = urlparse(value)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password:
        raise ValueError(f"{name} must be an absolute HTTP(S) URL without credentials")
    if parsed.fragment or any(ord(character) < 33 for character in value):
        raise ValueError(f"{name} contains invalid URL characters")


def post_json(url: str, payload: dict[str, Any], headers: dict[str, str] | None = None) -> bytes:
    request = Request(url, data=json.dumps(payload, ensure_ascii=False).encode("utf-8"), method="POST",
                      headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with build_opener(NoRedirect()).open(request, timeout=15) as response:
            if not 200 <= response.status < 300:
                raise DeliveryError(f"HTTP {response.status}", retryable=response.status >= 500)
            body = response.read(65_537)
            if len(body) > 65_536:
                raise DeliveryError("notification response exceeded 64 KB", retryable=False)
            return body
    except HTTPError as error:
        retry_after = 0.0
        try:
            retry_after = max(0.0, min(3600.0, float(error.headers.get("Retry-After", "0"))))
        except ValueError:
            pass
        error.close()
        raise DeliveryError(f"HTTP {error.code}", retryable=error.code in {408, 429} or error.code >= 500,
                            retry_after=retry_after) from None
    except (URLError, TimeoutError, OSError):
        raise DeliveryError("notification connection failed or timed out") from None


@dataclass(frozen=True)
class EmailSettings:
    smtp_host: str
    sender: str
    recipients: tuple[str, ...]
    smtp_port: int = 25
    smtp_starttls: bool = False
    smtp_username: str = ""
    smtp_password: str = ""

    def __post_init__(self) -> None:
        strings = (self.smtp_host, self.sender, *self.recipients)
        if not self.recipients or not all(isinstance(value, str) and value.strip() and "\r" not in value and "\n" not in value for value in strings):
            raise ValueError("email requires a host, sender and nonempty recipients without line breaks")
        if type(self.smtp_port) is not int or not 1 <= self.smtp_port <= 65535:
            raise ValueError("smtp_port must be an integer between 1 and 65535")
        if type(self.smtp_starttls) is not bool:
            raise ValueError("smtp_starttls must be a boolean")


class EmailIntegration:
    kind = "email"

    def __init__(self, settings: EmailSettings, integration_id: str = "email"):
        self.id = integration_id
        self.settings = settings

    def render(self, notification: Notification) -> dict[str, Any]:
        rate = notification.report["overall"]["hit_rate_percent"]
        return {
            "subject": f"Bifrost cache report: [{notification.severity.upper()}] {rate:.2f}% hit rate",
            "from": self.settings.sender, "to": list(self.settings.recipients),
            "date": format_datetime(parse_timestamp(notification.created_at)),
            "message_id": f"<{notification.delivery_key(self.id)}@bifrost-reports.local>",
            "text": notification.body_text, "html": notification.body_html,
        }

    def send(self, payload: dict[str, Any]) -> None:
        message = EmailMessage()
        message["Subject"] = payload["subject"]
        message["From"] = payload["from"]
        message["To"] = ", ".join(payload["to"])
        message["Date"] = payload["date"]
        message["Message-ID"] = payload["message_id"]
        message.set_content(payload["text"])
        message.add_alternative(payload["html"], subtype="html")
        try:
            with smtplib.SMTP(self.settings.smtp_host, self.settings.smtp_port, timeout=15) as smtp:
                if self.settings.smtp_starttls:
                    smtp.starttls(context=ssl.create_default_context())
                if self.settings.smtp_username:
                    smtp.login(self.settings.smtp_username, self.settings.smtp_password)
                refused = smtp.send_message(message)
                if refused:
                    raise DeliveryError("SMTP partially refused recipients; accepted recipients may receive duplicates")
        except smtplib.SMTPResponseException as error:
            raise DeliveryError(f"SMTP rejected delivery ({error.smtp_code})", retryable=error.smtp_code < 500) from None
        except (smtplib.SMTPException, OSError):
            raise DeliveryError("SMTP delivery failed") from None


class WebhookIntegration:
    kind = "webhook"

    def __init__(self, url: str, integration_id: str = "webhook"):
        validate_url(url, "webhook URL")
        self.url, self.id = url, integration_id

    def render(self, notification: Notification) -> dict[str, Any]:
        return {
            "schema_version": 1, "report_id": notification.report_id,
            "severity": notification.severity, "report": notification.report,
            "text": notification.body_text, "detail_url": notification.detail_url,
            "idempotency_key": notification.delivery_key(self.id),
        }

    def send(self, payload: dict[str, Any]) -> None:
        post_json(self.url, payload, {"Idempotency-Key": payload["idempotency_key"]})


def feishu_signature(timestamp: str, secret: str) -> str:
    return base64.b64encode(hmac.new(f"{timestamp}\n{secret}".encode(), b"", hashlib.sha256).digest()).decode()


def card_text(value: str) -> str:
    value = " ".join(value.split())[:200]
    value = value.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    return re.sub(r"([\\*`_\[\]()#!])", r"\\\1", value)


class FeishuIntegration:
    kind = "feishu"
    colors = {"normal": "green", "warning": "orange", "critical": "red", "unknown": "grey"}

    def __init__(self, url: str, secret: str = "", integration_id: str = "feishu",
                 clock: Callable[[], datetime] = utc_now):
        validate_url(url, "Feishu webhook URL")
        self.url, self.secret, self.id, self.clock = url, secret, integration_id, clock

    def render(self, notification: Notification) -> dict[str, Any]:
        report = notification.report
        overall, assessment = report["overall"], report["assessment"]
        miss_rate = assessment["miss_rate_percent"]
        miss_label = f"{miss_rate:.2f}%" if miss_rate is not None else "N/A"
        hit_label = f"{overall['hit_rate_percent']:.2f}%" if overall["requests"] else "N/A"
        elements = [{
            "tag": "markdown",
            "content": (
                f"**Cache miss rate:** {miss_label}\n**Cache hit rate:** {hit_label}\n"
                f"**Requests:** {overall['requests']:,.2f} · **Misses:** {overall['misses']:,.2f}\n\n"
                f"{assessment['reason']}\n\n{report['period_start']} → {report['period_end']}"
            ),
        }]
        details = sorted(report["details"], key=lambda row: -(row["requests"] - row["hits"]))[:5]
        if details:
            lines = ["**Top miss contributors (among archived detail rows)**"]
            for row in details:
                identity = ", ".join(f"{name}={card_text(value or '(empty)')}" for name, value in row["labels"].items())
                rate = row["assessment"]["miss_rate_percent"]
                rate_label = f"{rate:.2f}%" if rate is not None else "N/A"
                lines.append(f"{identity[:600]}: {rate_label} miss · {max(0, row['requests'] - row['hits']):,.2f} misses")
            elements.append({"tag": "markdown", "content": "\n".join(lines)})
        if notification.detail_url:
            elements.append({
                "tag": "button", "text": {"tag": "plain_text", "content": "Open full report"},
                "type": "default",
                "behaviors": [{"type": "open_url", "default_url": notification.detail_url}],
            })
        elements.append({"tag": "markdown", "content": f"Report #{notification.report_id}"})
        payload = {
            "msg_type": "interactive",
            "card": {
                "schema": "2.0",
                "header": {"title": {"tag": "plain_text", "content": f"{notification.severity.upper()} · Bifrost cache performance"},
                           "template": self.colors[notification.severity]},
                "body": {"elements": elements},
            },
        }
        if len(json.dumps(payload, ensure_ascii=False).encode("utf-8")) > 19_000:
            raise DeliveryError("Feishu card exceeds payload limit", retryable=False)
        return payload

    def send(self, payload: dict[str, Any]) -> None:
        outgoing = dict(payload)
        if self.secret:
            timestamp = str(int(self.clock().timestamp()))
            outgoing.update(timestamp=timestamp, sign=feishu_signature(timestamp, self.secret))
        body = post_json(self.url, outgoing)
        try:
            response = json.loads(body)
        except (ValueError, UnicodeDecodeError):
            raise DeliveryError("Feishu returned invalid JSON") from None
        if not isinstance(response, dict) or type(response.get("code")) is not int:
            raise DeliveryError("Feishu response is missing an integer code")
        code = response["code"]
        if code != 0:
            raise DeliveryError(f"Feishu rejected delivery (code {code})",
                                retryable=code not in {9499, 19021, 19022, 19024})


def configured_integrations(path: str, public_base_url: str) -> list[NotificationIntegration]:
    def secret_env(options: dict[str, Any], name: str) -> str:
        env_name = options.get(name, "")
        if not isinstance(env_name, str) or (env_name and not re.fullmatch(r"[a-zA-Z_][a-zA-Z0-9_]*", env_name)):
            raise ValueError(f"{name} must be an environment variable name")
        value = os.getenv(env_name, "") if env_name else ""
        if env_name and not value:
            raise ValueError(f"notification secret environment variable {env_name} is empty")
        return value

    def email_integration(item: dict[str, Any]) -> EmailIntegration:
        recipients = item.get("recipients")
        if not isinstance(recipients, list):
            raise ValueError("recipients must be a list of email addresses")
        return EmailIntegration(EmailSettings(
            smtp_host=item.get("smtp_host", ""),
            smtp_port=item.get("smtp_port", 25),
            smtp_starttls=item.get("smtp_starttls", False),
            smtp_username=secret_env(item, "smtp_username_env"),
            smtp_password=secret_env(item, "smtp_password_env"),
            sender=item.get("sender", ""),
            recipients=tuple(recipients),
        ), item["id"])

    factories = {
        "email": ({"smtp_host", "smtp_port", "smtp_starttls", "smtp_username_env", "smtp_password_env", "sender", "recipients"},
                  email_integration),
        "webhook": ({"webhook_url_env"},
                    lambda item: WebhookIntegration(secret_env(item, "webhook_url_env"), item["id"])),
        "feishu": ({"webhook_url_env", "signing_secret_env"},
                   lambda item: FeishuIntegration(secret_env(item, "webhook_url_env"), secret_env(item, "signing_secret_env"), item["id"])),
    }
    entries = json.loads(Path(path).read_text())
    if not isinstance(entries, list):
        raise ValueError("notification configuration must be a JSON array")
    integrations = []
    for entry in entries:
        if not isinstance(entry, dict) or not isinstance(entry.get("type"), str) or entry["type"] not in factories:
            raise ValueError("unknown notification integration type")
        if not isinstance(entry.get("id"), str) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,64}", entry["id"]):
            raise ValueError("integration ID must be 1-64 letters, digits, underscores or hyphens")
        allowed_fields, factory = factories[entry["type"]]
        if set(entry) - allowed_fields - {"id", "type"}:
            raise ValueError(f"unknown fields for {entry['type']} integration")
        integrations.append(factory(entry))
    if not integrations or len({integration.id for integration in integrations}) != len(integrations):
        raise ValueError("configure at least one integration with unique IDs")
    if any(integration.kind == "feishu" for integration in integrations) and not public_base_url:
        raise ValueError("Feishu requires REPORT_PUBLIC_BASE_URL for the full report link")
    return integrations
