import json
import io
import tempfile
import threading
import unittest
from contextlib import closing, contextmanager
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch
from http.server import ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.request import urlopen

from reporter import Config, ReportStore, Reporter, ReporterHTTPHandler, configured_clock
from notifications import DeliveryError, EmailIntegration, EmailSettings, FeishuIntegration, WebhookIntegration, configured_integrations, post_json
from severity import SeverityPolicy


NOW = datetime(2026, 9, 4, 12, 0, tzinfo=timezone.utc)


class FakePrometheus:
    def scalar(self, expression, at):
        if "cache_hits" in expression:
            return 6.0
        return 10.0

    def query(self, expression, at):
        if "by (cache_type)" in expression:
            return [sample({"cache_type": "direct"}, 5), sample({"cache_type": "semantic"}, 1)]
        if "cache_type" in expression:
            return [
                sample({"user_id": "alice", "cache_type": "direct"}, 4),
                sample({"user_id": "alice", "cache_type": "semantic"}, 1),
                sample({"user_id": "bob", "cache_type": "direct"}, 1),
            ]
        if "cache_hits" not in expression:
            return [sample({"user_id": "alice"}, 7), sample({"user_id": "bob"}, 3)]
        raise AssertionError(expression)

    @staticmethod
    def sample_value(value):
        return float(value["value"][1])


def sample(labels, value):
    return {"metric": labels, "value": [NOW.timestamp(), str(value)]}


class RecordingNotifier:
    kind = "test"

    def __init__(self, integration_id="recording"):
        self.id = integration_id
        self.sent = []
        self.rendered = 0
        self.fail = False
        self.permanent = False
        self.attempts = 0

    def render(self, notification):
        self.rendered += 1
        return {"report_id": notification.report_id, "report": notification.report,
                "text": notification.body_text, "html": notification.body_html}

    def send(self, payload):
        self.attempts += 1
        if self.fail:
            raise DeliveryError("mail server unavailable", retryable=not self.permanent)
        self.sent.append((payload["report_id"], payload["report"], payload["text"], payload["html"]))


def config(database_path):
    return Config(
        prometheus_url="http://prometheus:9090",
        database_path=database_path,
        group_by=("user_id",),
        notifications_file="notifications.json",
        minimum_requests=1,
        public_base_url="http://localhost:8091",
    )


class ReporterTest(unittest.TestCase):
    def test_fixed_clock_from_test_environment(self):
        with patch.dict("os.environ", {"REPORT_TEST_NOW": "2026-09-04T12:00:00Z"}):
            self.assertEqual(configured_clock()(), NOW)

    def test_builds_overall_and_per_user_report(self):
        with tempfile.TemporaryDirectory() as directory:
            notifier = RecordingNotifier()
            app = Reporter(config(str(Path(directory) / "reports.db")), FakePrometheus(), ReportStore(str(Path(directory) / "reports.db")), [notifier], lambda: NOW)

            report_id = app.run_once()

            self.assertEqual(report_id, 1)
            report = notifier.sent[0][1]
            self.assertEqual(report["overall"]["hit_rate_percent"], 60.0)
            self.assertEqual(report["overall"]["hits_by_type"], {"direct": 5.0, "semantic": 1.0})
            self.assertEqual(report["details"][0]["labels"], {"user_id": "alice"})
            self.assertAlmostEqual(report["details"][0]["hit_rate_percent"], 100 * 5 / 7)
            html_body = notifier.sent[0][3]
            self.assertIn("Overall hit rate", html_body)
            self.assertIn("Cache type breakdown", html_body)
            self.assertIn("Performance by user_id", html_body)
            self.assertIn("Cache misses", html_body)
            self.assertIn('role="presentation"', html_body)

    def test_does_not_send_again_until_interval_elapsed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = str(Path(directory) / "reports.db")
            notifier = RecordingNotifier()
            clock = [NOW]
            app = Reporter(config(path), FakePrometheus(), ReportStore(path), [notifier], lambda: clock[0])

            self.assertEqual(app.run_once(), 1)
            clock[0] = NOW + timedelta(hours=23, minutes=59)
            self.assertIsNone(app.run_once())
            clock[0] = NOW + timedelta(hours=24)
            self.assertEqual(app.run_once(), 2)
            self.assertEqual(len(notifier.sent), 2)

    def test_failed_delivery_advances_generation_without_recording_a_send(self):
        with tempfile.TemporaryDirectory() as directory:
            path = str(Path(directory) / "reports.db")
            store = ReportStore(path)
            notifier = RecordingNotifier()
            notifier.fail = True
            app = Reporter(config(path), FakePrometheus(), store, [notifier], lambda: NOW)
            app.run_once()

            self.assertEqual(store.last_generated_at(), NOW)
            reports = store.list_reports()
            self.assertEqual(reports[0]["status"], "failed")
            delivery = reports[0]["deliveries"][0]
            self.assertIsNone(delivery["sent_at"])
            self.assertEqual(delivery["last_error"], "mail server unavailable")

    def test_html_report_escapes_metric_labels(self):
        with tempfile.TemporaryDirectory() as directory:
            path = str(Path(directory) / "reports.db")
            app = Reporter(config(path), FakePrometheus(), ReportStore(path), [RecordingNotifier()], lambda: NOW)
            report = app.build_report(NOW)
            app.policy.apply(report)
            report["details"][0]["labels"]["user_id"] = "<script>alert('x')</script>"
            report["overall"]["hits_by_type"] = {"<direct>": 6.0}

            html_body = app.renderer.render_html(report)

            self.assertNotIn("<script>", html_body)
            self.assertIn("&lt;script&gt;", html_body)
            self.assertIn("&lt;Direct&gt;", html_body)

    def test_persisted_report_json_is_inspectable(self):
        with tempfile.TemporaryDirectory() as directory:
            path = str(Path(directory) / "reports.db")
            store = ReportStore(path)
            app = Reporter(config(path), FakePrometheus(), store, [RecordingNotifier()], lambda: NOW)
            app.run_once()

            persisted = store.list_reports(1)[0]
            self.assertEqual(persisted["status"], "sent")
            self.assertEqual(persisted["report"]["overall"]["requests"], 10.0)
            json.dumps(persisted)


class NotificationTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = str(Path(self.directory.name) / "reports.db")
        self.clock = [NOW]
        self.email = RecordingNotifier("email")
        self.feishu = RecordingNotifier("feishu")
        self.app = self.make_app()

    def make_app(self):
        return Reporter(config(self.path), FakePrometheus(), ReportStore(self.path),
                        [self.email, self.feishu], lambda: self.clock[0])

    def test_database_has_one_source_of_delivery_state(self):
        with closing(self.app.store.connect()) as connection:
            tables = {row[0] for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'"
            )}
            report_columns = {row["name"] for row in connection.execute("PRAGMA table_info(reports)")}
            scheduler_columns = {row["name"] for row in connection.execute("PRAGMA table_info(scheduler_state)")}
        self.assertEqual(tables, {"reports", "report_deliveries", "scheduler_state"})
        self.assertEqual(report_columns, {
            "id", "period_start", "period_end", "report_json", "body_text", "body_html",
            "renderer_version", "detail_url", "created_at",
        })
        self.assertEqual(scheduler_columns, {"singleton", "last_generated_at"})

    def test_superseding_failed_report_keeps_error_on_delivery_only(self):
        self.email.fail = self.feishu.fail = True
        self.app.run_once()
        self.clock[0] += timedelta(days=1)
        self.email.fail = self.feishu.fail = False
        self.app.run_once()
        report = self.app.store.get_report(1)
        self.assertEqual(report["status"], "superseded")
        self.assertNotIn("delivery_error", report)
        self.assertNotIn("sent_at", report)
        self.assertTrue(all(delivery["last_error"] for delivery in report["deliveries"]))

    def test_severity_boundaries_and_small_samples(self):
        policy = SeverityPolicy(minimum_requests=100)
        for hits, expected in [(701, "normal"), (700, "warning"), (400, "warning"), (399, "critical")]:
            with self.subTest(hits=hits):
                self.assertEqual(policy.assess(1000, hits)["level"], expected)
        for requests, hits in [(0, 0), (99, 0), (100, 101), (-1, 0), (float("nan"), 0)]:
            with self.subTest(requests=requests):
                self.assertEqual(policy.assess(requests, hits)["level"], "unknown")
        self.assertIsNone(policy.assess(0, 0)["miss_rate_percent"])
        for warning, critical in [(60, 30), (30, 30), (-1, 30), (30, 101), (float("nan"), 60)]:
            with self.assertRaises(ValueError):
                SeverityPolicy(warning, critical)

    def test_partial_success_retries_only_failed_integration_after_restart(self):
        self.feishu.fail = True
        self.assertEqual(self.app.run_once(), 1)
        self.assertEqual(self.app.store.get_report(1)["status"], "partial")
        self.assertEqual(len(self.email.sent), 1)
        self.assertEqual(self.feishu.rendered, 1)
        self.clock[0] += timedelta(seconds=30)
        self.assertIsNone(self.app.run_once())
        self.assertEqual(self.feishu.attempts, 1)
        self.clock[0] += timedelta(seconds=40)
        self.feishu.fail = False
        self.app = self.make_app()
        self.assertIsNone(self.app.run_once())
        self.assertEqual(len(self.email.sent), 1)
        self.assertEqual(len(self.feishu.sent), 1)
        self.assertEqual(self.feishu.rendered, 1)
        self.assertEqual(self.app.store.get_report(1)["status"], "sent")
        self.assertEqual(len(self.app.store.list_reports()), 1)

    def test_three_day_pause_generates_one_current_report_without_backlog(self):
        self.app.run_once()
        self.clock[0] += timedelta(days=3)
        self.assertEqual(self.app.run_once(), 2)
        self.assertIsNone(self.app.run_once())
        report = self.app.store.get_report(2)["report"]
        self.assertEqual(report["period_start"], "2026-09-06T12:00:00Z")
        self.assertEqual(len(self.email.sent), 2)

    def test_latest_report_supersedes_undelivered_old_report(self):
        self.feishu.fail = True
        self.app.run_once()
        self.clock[0] += timedelta(days=1)
        self.feishu.fail = False
        self.app.run_once()
        self.assertEqual([row[0] for row in self.feishu.sent], [2])
        old = self.app.store.get_report(1)
        self.assertEqual(old["deliveries"][1]["status"], "superseded")
        self.assertEqual(old["deliveries"][0]["status"], "sent")

    def test_permanent_error_is_not_retried_until_new_report(self):
        self.feishu.fail = self.feishu.permanent = True
        self.app.run_once()
        self.clock[0] += timedelta(hours=1)
        self.app.run_once()
        self.assertEqual(self.feishu.attempts, 1)
        self.assertIsNone(self.app.store.get_report(1)["deliveries"][1]["next_attempt_at"])

    def test_disabled_integration_does_not_block_other_channels(self):
        self.feishu.fail = True
        self.app.run_once()
        self.clock[0] += timedelta(minutes=2)
        self.app = Reporter(config(self.path), FakePrometheus(), ReportStore(self.path), [self.email], lambda: self.clock[0])
        self.app.run_once()
        self.assertEqual(self.app.store.get_report(1)["deliveries"][1]["status"], "superseded")
        self.assertEqual(len(self.email.sent), 1)

    def test_interrupted_send_is_recovered(self):
        self.app.run_once()
        with closing(self.app.store.connect()) as connection, connection:
            connection.execute("UPDATE report_deliveries SET status = 'sending', sent_at = NULL WHERE integration_id = 'feishu'")
        self.app = self.make_app()
        self.app.run_once()
        self.assertEqual(len(self.feishu.sent), 2)
        self.assertEqual(len(self.email.sent), 1)

    def test_retries_do_not_require_prometheus(self):
        self.feishu.fail = True
        self.app.run_once()
        self.feishu.fail = False
        self.clock[0] += timedelta(days=1)
        with patch.object(self.app, "build_report", side_effect=RuntimeError("Prometheus unavailable")):
            with self.assertRaisesRegex(RuntimeError, "Prometheus unavailable"):
                self.app.run_once()
        self.assertEqual(len(self.email.sent), 1)
        self.assertEqual(len(self.feishu.sent), 1)

    def test_renderer_artifact_is_shared_and_does_not_change(self):
        self.app.run_once()
        notification = self.app.store.notification(1)
        payload = EmailIntegration(EmailSettings("mailpit", "reports@example.test", ("owner@example.test",))).render(notification)
        self.assertEqual(payload["html"], notification.body_html)
        self.assertIn("http://localhost:8091/reports/1/view", payload["html"])
        with patch.object(self.app.renderer, "render_html", side_effect=AssertionError("must not render")):
            with self.http_server() as base_url:
                with urlopen(base_url + "/reports/1/view") as response:
                    self.assertEqual(response.read().decode(), payload["html"])
                    self.assertIn("default-src 'none'", response.headers["Content-Security-Policy"])
                with urlopen(base_url + "/reports/latest/view") as response:
                    self.assertTrue(response.url.endswith("/reports/1/view"))
                with urlopen(base_url + "/reports/1") as response:
                    self.assertEqual(json.load(response)["report"]["assessment"]["level"], "warning")
                with self.assertRaises(HTTPError) as caught:
                    urlopen(base_url + "/reports/999/view")
                self.assertEqual(caught.exception.code, 404)
                caught.exception.close()
        self.clock[0] += timedelta(days=1)
        self.app.run_once()
        self.assertEqual(self.app.store.notification(1), notification)

    @contextmanager
    def http_server(self):
        handler = type("TestHandler", (ReporterHTTPHandler,), {"reporter": self.app})
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}"
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_feishu_uses_assessment_and_current_signature(self):
        self.app.run_once()
        notification = self.app.store.notification(1)
        integration = FeishuIntegration("http://mock-feishu/hooks/test", "secret", clock=lambda: NOW)
        payload = integration.render(notification)
        self.assertEqual(payload["card"]["header"]["template"], "orange")
        self.assertIn(notification.detail_url, json.dumps(payload))
        with patch("notifications.post_json", return_value=b'{"code":0}') as post:
            integration.send(payload)
            outgoing = post.call_args.args[1]
            self.assertEqual(outgoing["timestamp"], str(int(NOW.timestamp())))
            self.assertIn("sign", outgoing)
        self.assertNotIn("sign", payload)
        self.assertNotIn("secret", json.dumps(payload))
        for body, retryable in [(b'{"code":11232}', True), (b'{"code":19021}', False), (b'{"code":9499}', False),
                                (b'{"code":true}', True), (b'{"msg":"success"}', True), (b"not json", True)]:
            with self.subTest(body=body), patch("notifications.post_json", return_value=body):
                with self.assertRaises(DeliveryError) as caught:
                    integration.send(payload)
                self.assertEqual(caught.exception.retryable, retryable)

    def test_feishu_card_bounds_and_escaping(self):
        report = self.app.build_report(NOW)
        report["details"][0]["labels"]["user_id"] = '<at id=all>everyone</at> [bad](https://bad.test)' + '坏' * 10000
        self.app.policy.apply(report)
        self.app.store.create(report, self.app.renderer, [("test", "test")], NOW, 86400, "http://localhost:8091")
        payload = FeishuIntegration("http://mock-feishu/hooks/test").render(self.app.store.notification(1))
        encoded = json.dumps(payload, ensure_ascii=False).encode()
        self.assertLess(len(encoded), 19000)
        self.assertNotIn(b"<at", encoded)
        self.assertIn(b"&lt;at", encoded)

    def test_config_validation_and_multiple_instances(self):
        with self.assertRaises(ValueError):
            replace(config(self.path), public_base_url="javascript:alert(1)").validate()
        with self.assertRaises(ValueError):
            replace(config(self.path), notifications_file="").validate()
        entries = [{"id": "ops", "type": "feishu", "webhook_url_env": "TEST_FEISHU_URL"},
                   {"id": "dev", "type": "feishu", "webhook_url_env": "TEST_FEISHU_URL"}]
        with patch("notifications.Path.read_text", return_value=json.dumps(entries)), patch.dict("os.environ", {"TEST_FEISHU_URL": "http://feishu/hooks/mock"}):
            with self.assertRaisesRegex(ValueError, "REPORT_PUBLIC_BASE_URL"):
                configured_integrations("mock.json", "")
            integrations = configured_integrations("mock.json", "https://reports.example.test")
            self.assertEqual([integration.id for integration in integrations], ["ops", "dev"])

    def test_reopen_preserves_report_and_schedule(self):
        self.app.run_once()
        report = self.app.store.notification(1)
        self.app = self.make_app()
        self.assertEqual(self.app.store.last_generated_at(), NOW)
        self.assertEqual(self.app.store.notification(1), report)
        self.assertIsNone(self.app.run_once())
        self.assertEqual(len(self.email.sent), 1)
        self.assertEqual(len(self.feishu.sent), 1)

    def test_email_settings_belong_to_each_integration(self):
        entries = [
            {"id": "ops", "type": "email", "smtp_host": "ops-smtp", "smtp_port": 587, "smtp_starttls": True,
             "sender": "ops@example.test", "recipients": ["ops@example.test"], "smtp_password_env": "OPS_PASSWORD"},
            {"id": "dev", "type": "email", "smtp_host": "mailpit", "smtp_port": 1025,
             "sender": "dev@example.test", "recipients": ["dev@example.test"]},
        ]
        with patch("notifications.Path.read_text", return_value=json.dumps(entries)), patch.dict("os.environ", {"OPS_PASSWORD": "secret"}):
            integrations = configured_integrations("mock.json", "")
        self.assertEqual([integration.settings.smtp_host for integration in integrations], ["ops-smtp", "mailpit"])
        self.assertEqual(integrations[0].settings.smtp_password, "secret")
        self.assertTrue(integrations[0].settings.smtp_starttls)
        self.app.run_once()
        self.assertNotIn("secret", json.dumps(integrations[0].render(self.app.store.notification(1))))

    def test_invalid_integration_configuration_is_rejected(self):
        valid = {"id": "email", "type": "email", "smtp_host": "mailpit",
                 "sender": "reports@example.test", "recipients": ["owner@example.test"]}
        invalid_entries = [
            [], {}, [valid, valid], [{**valid, "type": []}], [{**valid, "unused": "value"}],
            [{**valid, "recipients": []}], [{**valid, "recipients": "owner@example.test"}],
            [{**valid, "smtp_port": True}], [{**valid, "smtp_starttls": "false"}],
            [{**valid, "smtp_password_env": ["invalid"]}],
            [{**valid, "sender": "sender\r\nInjected: header"}],
        ]
        for entries in invalid_entries:
            with self.subTest(entries=entries), patch("notifications.Path.read_text", return_value=json.dumps(entries)):
                with self.assertRaises(ValueError):
                    configured_integrations("mock.json", "")

    def test_atomic_generation_and_render_failure_rollback(self):
        report = self.app.build_report(NOW)
        self.app.policy.apply(report)
        with patch.object(self.app.renderer, "render_html", side_effect=RuntimeError("template failed")):
            with self.assertRaisesRegex(RuntimeError, "template failed"):
                self.app.store.create(report, self.app.renderer, [("email", "test")], NOW, 86400, "")
        self.assertIsNone(self.app.store.last_generated_at())
        self.assertEqual(self.app.store.list_reports(), [])
        self.app.run_once()
        self.assertIsNone(self.app.store.create(report, self.app.renderer, [("email", "test")], NOW, 86400, ""))
        self.assertEqual(len(self.app.store.list_reports()), 1)

    def test_webhook_contains_shared_report_and_stable_idempotency_key(self):
        self.app.run_once()
        notification = self.app.store.notification(1)
        integration = WebhookIntegration("https://hooks.example.test/private-secret", "ops")
        payload = integration.render(notification)
        self.assertEqual(payload["severity"], notification.severity)
        self.assertEqual(payload["detail_url"], notification.detail_url)
        self.assertEqual(payload["text"], notification.body_text)
        self.assertNotIn("private-secret", json.dumps(payload))
        with patch("notifications.post_json", return_value=b"") as post:
            integration.send(payload)
            self.assertEqual(post.call_args.args[2]["Idempotency-Key"], payload["idempotency_key"])

    def test_http_failures_are_sanitized_and_classified(self):
        for status, retryable in [(429, True), (500, True), (400, False), (302, False)]:
            with self.subTest(status=status):
                error = HTTPError("https://hooks.test/private-secret", status, "private-secret",
                                  {"Retry-After": "90"}, io.BytesIO(b"private-secret"))
                with patch("notifications.build_opener") as opener:
                    opener.return_value.open.side_effect = error
                    with self.assertRaises(DeliveryError) as caught:
                        post_json("https://hooks.test/private-secret", {})
                self.assertEqual(caught.exception.retryable, retryable)
                self.assertEqual(caught.exception.retry_after, 90)
                self.assertNotIn("private-secret", str(caught.exception))
                error.close()

    def test_each_feishu_color_comes_from_central_assessment(self):
        for hits, level, color in [(9, "normal", "green"), (6, "warning", "orange"), (1, "critical", "red")]:
            with self.subTest(level=level):
                report = self.app.build_report(NOW)
                report["overall"].update(hits=hits, hit_rate_percent=hits * 10, misses=10-hits)
                self.app.policy.apply(report)
                report_id = self.app.store.create(report, self.app.renderer, [("test", "test")], NOW, 86400, "", force=True)
                notification = self.app.store.notification(report_id)
                self.assertEqual(notification.severity, level)
                self.assertEqual(FeishuIntegration("http://mock").render(notification)["card"]["header"]["template"], color)

    def test_other_channel_success_preserves_delivery_error(self):
        self.email.fail = True
        self.app.run_once()
        report = self.app.store.get_report(1)
        self.assertEqual(report["status"], "partial")
        deliveries = {delivery["integration_id"]: delivery for delivery in report["deliveries"]}
        self.assertEqual(deliveries["email"]["last_error"], "mail server unavailable")
        self.assertIsNone(deliveries["email"]["sent_at"])
        self.assertIsNotNone(deliveries["feishu"]["sent_at"])


if __name__ == "__main__":
    unittest.main()
