#!/usr/bin/env python3
"""Local-only Feishu webhook recorder with signature checks and fault injection."""

import base64
import hashlib
import hmac
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


SECRET = os.getenv("MOCK_FEISHU_SIGNING_SECRET", "local-feishu-secret")
LOCK = threading.Lock()
HOOKS = {}


def hook_state(name):
    return HOOKS.setdefault(name, {"attempts": 0, "messages": [], "remaining_failures": 0, "http_status": 200, "code": 11232})


class Handler(BaseHTTPRequestHandler):
    def respond(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            self.respond(200, {"status": "ok"})
        elif self.path.startswith("/messages/"):
            with LOCK:
                self.respond(200, hook_state(self.path.removeprefix("/messages/")))
        else:
            self.respond(404, {"error": "not found"})

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 20_000:
                self.respond(400, {"code": 9499})
                return
            payload = json.loads(self.rfile.read(length))
            if not isinstance(payload, dict):
                raise ValueError()
            if self.path.startswith("/control/"):
                with LOCK:
                    state = hook_state(self.path.removeprefix("/control/"))
                    state.update(remaining_failures=int(payload.get("remaining_failures", 0)),
                                 http_status=int(payload.get("http_status", 200)), code=int(payload.get("code", 11232)))
                    self.respond(200, {"ok": True})
                return
            if not self.path.startswith("/hooks/"):
                self.respond(404, {"error": "not found"})
                return
            timestamp = str(payload.get("timestamp", "0"))
            expected = base64.b64encode(hmac.new(f"{timestamp}\n{SECRET}".encode(), b"", hashlib.sha256).digest()).decode()
            if abs(time.time() - int(timestamp)) > 3600 or not hmac.compare_digest(expected, payload.get("sign", "")):
                self.respond(200, {"code": 19021})
                return
            if payload.get("msg_type") != "interactive" or payload.get("card", {}).get("schema") != "2.0":
                self.respond(200, {"code": 9499})
                return
            with LOCK:
                state = hook_state(self.path.removeprefix("/hooks/"))
                state["attempts"] += 1
                if state["remaining_failures"]:
                    state["remaining_failures"] -= 1
                    self.respond(state["http_status"], {"code": state["code"]})
                    return
                state["messages"].append(payload["card"])
                state["messages"] = state["messages"][-1000:]
                self.respond(200, {"code": 0, "msg": "success", "data": {}})
        except (ValueError, TypeError, KeyError):
            self.respond(400, {"code": 9499})

    def log_message(self, message_format, *args):
        return


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8092), Handler).serve_forever()
