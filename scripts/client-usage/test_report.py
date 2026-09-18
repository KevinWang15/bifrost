import csv
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from datetime import date
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit
from zoneinfo import ZoneInfo

import report


class ReportTests(unittest.TestCase):
    def setUp(self):
        self.calls = []
        self.status = 200
        self.malformed = False
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                url = urlsplit(self.path)
                params = {key: values[0] for key, values in parse_qs(url.query).items()}
                owner.calls.append((url.path, params, self.headers.get("Authorization")))
                self.send_response(owner.status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                if owner.status != 200:
                    self.wfile.write(b'{"error":"private server detail"}')
                    return
                dimension = params["dimension"]
                if dimension == "app":
                    ids = ["Claude Code", "Codex CLI", "Codex Desktop", "Cursor"]
                else:
                    apps = json.loads(params["apps"])
                    first_day = params["start_time"] == "2026-09-14T16:00:00Z"
                    if apps == ["Claude Code"]:
                        ids = ["A", "A", "unassigned"] if first_day else []
                    elif apps == ["Codex CLI", "Codex Desktop"]:
                        ids = ["A"] + [f"key-{i}" for i in range(100)] if first_day else ["A", "B"]
                    elif apps == ["Cursor"]:
                        ids = [] if first_day else ["C"]
                    else:
                        ids = ["unexpected-app-filter"]
                payload = {"dimension": dimension, "rankings": [
                    {"id": key, "total_requests": 500} for key in ids
                ]}
                if owner.malformed:
                    payload = {"dimension": dimension, "rankings": [{"id": "A"}]}
                self.wfile.write(json.dumps(payload).encode())

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base_url = f"http://127.0.0.1:{self.server.server_port}"
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.addCleanup(self.stop_server)

    def stop_server(self):
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()

    def run_report(self):
        env = {key: value for key, value in os.environ.items() if not key.startswith("BIFROST_")}
        env["BIFROST_AUTHORIZATION"] = "Bearer mock-session"
        return subprocess.run([
            sys.executable, str(Path(report.__file__)),
            "--base-url", self.base_url,
            "--start", "2026-09-15", "--end", "2026-09-16",
            "--timezone", "Asia/Shanghai", "--output-dir", self.temp.name,
        ], capture_output=True, text=True, env=env, timeout=15)

    def read_csv(self, name):
        with (Path(self.temp.name) / name).open(newline="") as source:
            return list(csv.DictReader(source))

    def test_cli_deduplicates_daily_and_overall_with_more_than_100_vks(self):
        result = self.run_report()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.read_csv("overall.csv"), [
            {"client": "claude-code", "unique_vks": "1"},
            {"client": "codex", "unique_vks": "102"},
            {"client": "cursor", "unique_vks": "1"},
        ])
        daily = self.read_csv("daily.csv")
        self.assertEqual(len(daily), 6)
        self.assertEqual(daily[0]["active_vks"], "1")
        self.assertEqual(daily[0]["change_from_previous_day"], "")
        self.assertEqual(daily[1]["active_vks"], "101")
        self.assertEqual(daily[3]["active_vks"], "0")
        self.assertEqual(daily[3]["change_from_previous_day"], "-1")
        self.assertEqual(daily[4], {
            "date": "2026-09-16", "client": "codex", "active_vks": "2",
            "change_from_previous_day": "-99", "cumulative_vks": "102",
        })
        self.assertEqual(len(self.calls), 7)
        for path, params, auth in self.calls:
            self.assertEqual(path, "/api/logs/rankings/by-dimension")
            self.assertEqual(params["all"], "true")
            self.assertEqual(auth, "Bearer mock-session")
            if params["dimension"] == "virtual_key":
                if params["start_time"] == "2026-09-14T16:00:00Z":
                    self.assertEqual(params["end_time"], "2026-09-15T15:59:59.999999999Z")
                else:
                    self.assertEqual(params["start_time"], "2026-09-15T16:00:00Z")
                    self.assertEqual(params["end_time"], "2026-09-16T15:59:59.999999999Z")
        self.assertNotIn("mock-session", result.stdout + result.stderr)

    def test_auth_failure_does_not_write_reports_or_expose_response(self):
        self.status = 401
        result = self.run_report()
        self.assertEqual(result.returncode, 1)
        self.assertIn("HTTP 401", result.stderr)
        self.assertNotIn("private server detail", result.stderr)
        self.assertEqual(list(Path(self.temp.name).iterdir()), [])

    def test_invalid_response_does_not_become_a_zero_count(self):
        self.malformed = True
        result = self.run_report()
        self.assertEqual(result.returncode, 1)
        self.assertIn("invalid ranking row", result.stderr)
        self.assertEqual(list(Path(self.temp.name).iterdir()), [])

    def test_calendar_boundaries_handle_short_and_long_dst_days(self):
        tz = ZoneInfo("America/New_York")
        spring = report.window_params(date(2026, 3, 8), date(2026, 3, 8), tz)
        self.assertEqual(spring, {
            "start_time": "2026-03-08T05:00:00Z", "end_time": "2026-03-09T03:59:59.999999999Z",
        })
        autumn = report.window_params(date(2026, 11, 1), date(2026, 11, 1), tz)
        self.assertEqual(autumn, {
            "start_time": "2026-11-01T04:00:00Z", "end_time": "2026-11-02T04:59:59.999999999Z",
        })

    def test_custom_group_keeps_exact_filters_and_other_clients(self):
        self.assertEqual(report.app_groups(
            ["Codex CLI", "Internal Codex", "Cursor", "", "Other"],
            ["codex=Codex CLI,Internal Codex"],
        ), {"codex": ["Codex CLI", "Internal Codex"], "cursor": ["Cursor"], "other": ["Other"]})
        with self.assertRaises(report.ReportError):
            report.app_groups([], ["codex="])


if __name__ == "__main__":
    unittest.main()
