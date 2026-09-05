#!/usr/bin/env python3
"""End-to-end test for Bifrost daily cache reporting.

The development stack must already be running. The test creates an isolated
SQLite volume, exercises the real Prometheus and Mailpit services, advances the
reporter's clock without sleeping, and removes its temporary volume afterward.
"""

from __future__ import annotations

import base64
import json
import os
import socket
import subprocess
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import urlencode, urlparse
from urllib.request import ProxyHandler, Request, build_opener


UTC = timezone.utc
EXAMPLE_DIR = Path(__file__).resolve().parents[1]
PROJECT = "bifrost-cache-reporting"
NETWORK = f"{PROJECT}_default"
REPORTER_IMAGE = f"{PROJECT}-reporter:latest"
HTTP = build_opener(ProxyHandler({}))


def run(command: list[str], expected_code: int = 0) -> str:
    result = subprocess.run(
        command,
        cwd=EXAMPLE_DIR,
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != expected_code:
        raise AssertionError(
            f"command failed ({result.returncode}): {' '.join(command)}\n"
            f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
        )
    return result.stdout


def get_json(url: str, headers: dict[str, str] | None = None) -> object:
    request = Request(url, headers=headers or {})
    with HTTP.open(request, timeout=10) as response:
        return json.load(response)


def wait_for(description: str, predicate, timeout: float = 15) -> None:
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            if predicate():
                return
        except Exception as error:  # Services can briefly race during startup.
            last_error = error
        time.sleep(0.25)
    raise AssertionError(f"timed out waiting for {description}: {last_error or 'condition not met'}")


def isoformat(value: datetime) -> str:
    return value.astimezone(UTC).isoformat(timespec="seconds").replace("+00:00", "Z")


def prometheus_query(expression: str) -> list[dict]:
    payload = get_json(f"http://127.0.0.1:9090/api/v1/query?{urlencode({'query': expression})}")
    if payload.get("status") != "success":  # type: ignore[union-attr]
        raise AssertionError(f"Prometheus query failed: {payload}")
    return payload["data"]["result"]  # type: ignore[index]


def mail_snapshot() -> tuple[int, dict[str, dict]]:
    payload = get_json("http://127.0.0.1:8025/api/v1/messages")
    messages = {message["ID"]: message for message in payload["messages"]}  # type: ignore[index]
    return int(payload["total"]), messages  # type: ignore[index]


def reporter_command(volume: str, now: datetime, hook: str, public_url: str) -> list[str]:
    return [
            "docker",
            "run",
            "--rm",
            "--network",
            NETWORK,
            "--volume",
            f"{volume}:/data",
            "--volume",
            f"{EXAMPLE_DIR / 'e2e' / 'notifications.json'}:/config/notifications.json:ro",
            "--env",
            "REPORT_NOTIFICATIONS_FILE=/config/notifications.json",
            "--env",
            "PROMETHEUS_URL=http://prometheus:9090",
            "--env",
            "REPORT_DATABASE_PATH=/data/reports.db",
            "--env",
            "REPORT_WINDOW=24h",
            "--env",
            "REPORT_INTERVAL_SECONDS=86400",
            "--env",
            "REPORT_GROUP_BY=user_id",
            "--env",
            "REPORT_MINIMUM_REQUESTS=1",
            "--env",
            f"REPORT_PUBLIC_BASE_URL={public_url}",
            "--env",
            f"FEISHU_WEBHOOK_URL=http://mock-feishu:8092/hooks/{hook}",
            "--env",
            "FEISHU_SIGNING_SECRET=local-feishu-secret",
            "--env",
            f"REPORT_TEST_NOW={isoformat(now)}",
            REPORTER_IMAGE,
        ]


def run_reporter(volume: str, now: datetime, hook: str, public_url: str, expected_code: int = 0) -> str:
    return run(reporter_command(volume, now, hook, public_url) + ["--once"], expected_code)


def control_feishu(hook: str, failures: int, http_status: int = 200, code: int = 11232) -> None:
    request = Request(f"http://127.0.0.1:8092/control/{hook}",
                      data=json.dumps({"remaining_failures": failures, "http_status": http_status, "code": code}).encode(),
                      headers={"Content-Type": "application/json"}, method="POST")
    with HTTP.open(request, timeout=10) as response:
        assert response.status == 200


def feishu_state(hook: str) -> dict:
    return get_json(f"http://127.0.0.1:8092/messages/{hook}")


def database_state(volume: str) -> dict:
    program = """
import json
from storage import ReportStore
from models import isoformat

store = ReportStore('/data/reports.db')
reports = list(reversed(store.list_reports()))
for report in reports:
    notification = store.notification(report['id'])
    report['body_html'] = notification.body_html
    report['body_text'] = notification.body_text
print(json.dumps({
    'reports': reports,
    'last_generated_at': isoformat(store.last_generated_at()) if store.last_generated_at() else None,
}))
"""
    output = run(
        [
            "docker",
            "run",
            "--rm",
            "--volume",
            f"{volume}:/data",
            "--entrypoint",
            "python",
            REPORTER_IMAGE,
            "-c",
            program,
        ]
    )
    return json.loads(output)


def verify_stack() -> None:
    health = get_json("http://127.0.0.1:8080/health")
    assert health["status"] == "ok"  # type: ignore[index]

    targets = prometheus_query('up{job="bifrost"}')
    assert targets and float(targets[0]["value"][1]) == 1

    users = {
        sample["metric"].get("user_id")
        for sample in prometheus_query("sum by (user_id) (bifrost_cache_hits_total)")
    }
    assert {"alice", "bob"}.issubset(users), f"missing seeded user metrics: {users}"

    credentials = base64.b64encode(b"admin:admin").decode()
    dashboards = get_json(
        "http://127.0.0.1:3000/api/search?query=Bifrost%20cache%20hit%20reporting",
        {"Authorization": f"Basic {credentials}"},
    )
    assert any(item.get("uid") == "bifrost-cache-reporting" for item in dashboards)  # type: ignore[union-attr]


def main() -> None:
    verify_stack()
    initial_mail_count, initial_messages = mail_snapshot()
    volume = f"{PROJECT}-e2e-{os.getpid()}-{time.time_ns()}".lower()
    hook = volume
    server_name = f"{volume}-http"
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        http_port = listener.getsockname()[1]
    public_url = f"http://127.0.0.1:{http_port}"
    server_started = False
    run(["docker", "volume", "create", volume])

    try:
        start = datetime.now(UTC).replace(microsecond=0)

        control_feishu(hook, failures=1)
        first_output = run_reporter(volume, start, hook, public_url, expected_code=1)
        assert "persisted; deliveries attempted" in first_output
        assert "code 11232" in first_output
        wait_for("first E2E email", lambda: mail_snapshot()[0] == initial_mail_count + 1)

        _, messages_after_first = mail_snapshot()
        first_message_ids = set(messages_after_first) - set(initial_messages)
        assert len(first_message_ids) == 1
        first_message = messages_after_first[first_message_ids.pop()]
        assert first_message["From"]["Address"] == "bifrost-e2e@example.test"
        assert first_message["Subject"].startswith("Bifrost cache report:")
        delivered = get_json(f"http://127.0.0.1:8025/api/v1/message/{first_message['ID']}")
        assert "user_id=alice" in delivered["Text"]  # type: ignore[index]
        assert "user_id=bob" in delivered["Text"]  # type: ignore[index]

        first_state = database_state(volume)
        assert len(first_state["reports"]) == 1
        assert first_state["reports"][0]["status"] == "partial"
        assert first_state["last_generated_at"] == isoformat(start)
        first_deliveries = {delivery["integration_id"]: delivery for delivery in first_state["reports"][0]["deliveries"]}
        assert first_deliveries["email"]["sent_at"] == isoformat(start)
        assert first_deliveries["feishu"]["sent_at"] is None
        assert feishu_state(hook)["attempts"] == 1
        assert feishu_state(hook)["messages"] == []
        first_report = first_state["reports"][0]["report"]
        assert first_report["overall"]["requests"] > 0
        report_users = {row["labels"].get("user_id") for row in first_report["details"]}
        assert {"alice", "bob"}.issubset(report_users)
        assert first_state["reports"][0]["body_html"].replace("\r\n", "\n").rstrip() == delivered["HTML"].replace("\r\n", "\n").rstrip()

        early_retry = run_reporter(volume, start + timedelta(seconds=30), hook, public_url)
        assert "report not due" in early_retry
        assert feishu_state(hook)["attempts"] == 1
        control_feishu(hook, failures=1, http_status=500)
        failed_retry = run_reporter(volume, start + timedelta(seconds=70), hook, public_url, expected_code=1)
        assert "HTTP 500" in failed_retry
        assert feishu_state(hook)["attempts"] == 2
        assert mail_snapshot()[0] == initial_mail_count + 1
        retried = run_reporter(volume, start + timedelta(minutes=5), hook, public_url)
        assert "report not due" in retried
        assert mail_snapshot()[0] == initial_mail_count + 1
        assert len(database_state(volume)["reports"]) == 1
        retried_report = database_state(volume)["reports"][0]
        assert retried_report["status"] == "sent"
        retried_deliveries = {delivery["integration_id"]: delivery for delivery in retried_report["deliveries"]}
        assert retried_deliveries["email"]["sent_at"] == isoformat(start)
        assert retried_deliveries["feishu"]["sent_at"] == isoformat(start + timedelta(minutes=5))
        card = feishu_state(hook)["messages"][0]
        expected_color = {"normal": "green", "warning": "orange", "critical": "red", "unknown": "grey"}
        assert card["header"]["template"] == expected_color[first_report["assessment"]["level"]]
        detail_url = next(element["behaviors"][0]["default_url"] for element in card["body"]["elements"] if element["tag"] == "button")
        assert detail_url == public_url + "/reports/1/view"

        server_command = reporter_command(volume, start + timedelta(minutes=5), hook, public_url)
        server_command[2:2] = ["--detach", "--name", server_name, "--publish", f"127.0.0.1:{http_port}:8091"]
        run(server_command)
        server_started = True
        wait_for("isolated report HTML server", lambda: get_json(public_url + "/healthz")["status"] == "ok")
        with HTTP.open(detail_url) as response:
            assert response.read().decode() == first_state["reports"][0]["body_html"]
        with HTTP.open(public_url + "/reports/latest/view") as response:
            assert urlparse(response.url).path == "/reports/1/view"
        run(["docker", "stop", server_name])
        server_started = False

        early_output = run_reporter(volume, start + timedelta(hours=23, minutes=59, seconds=59), hook, public_url)
        assert "report not due" in early_output
        assert len(database_state(volume)["reports"]) == 1
        assert mail_snapshot()[0] == initial_mail_count + 1

        due_at = start + timedelta(hours=24)
        due_output = run_reporter(volume, due_at, hook, public_url)
        assert "persisted; deliveries attempted" in due_output
        wait_for("second E2E email", lambda: mail_snapshot()[0] == initial_mail_count + 2)

        final_state = database_state(volume)
        assert len(final_state["reports"]) == 2
        assert all(report["status"] == "sent" for report in final_state["reports"])
        assert len(feishu_state(hook)["messages"]) == 2
        assert all(delivery["sent_at"] == isoformat(due_at) for delivery in final_state["reports"][1]["deliveries"])
        assert final_state["reports"][0]["body_html"] == first_state["reports"][0]["body_html"]

        paused_at = due_at + timedelta(days=3)
        run_reporter(volume, paused_at, hook, public_url)
        run_reporter(volume, paused_at, hook, public_url)
        assert len(database_state(volume)["reports"]) == 3
        assert mail_snapshot()[0] == initial_mail_count + 3
        assert len(feishu_state(hook)["messages"]) == 3
        assert database_state(volume)["reports"][2]["report"]["assessment"]["level"] == "unknown"
        assert feishu_state(hook)["messages"][2]["header"]["template"] == "grey"
        print("PASS: real Bifrost/Prometheus, SMTP + signed Feishu fan-out, partial retries, shared HTML, 24h gate, and 3-day pause")
    finally:
        if server_started:
            run(["docker", "stop", server_name])
        run(["docker", "volume", "rm", volume])


if __name__ == "__main__":
    main()
